package signer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer/configlock"
)

// The signer's record of the proposals it has signed an order for. A trading
// key is split in two, so Mishmar alone can never trade, and this signer signs
// only a venue message that matches a proposal the agent's own key signed. That
// does not stop an engine that is compromised, or wrong, from asking for a
// second order for the same proposal, under a new request id with a fresh venue
// message: each copy would pass the checks. So the signer signs an order for a
// proposal once, for the whole of the proposal's life, across restarts.
//
// A proposal is known by its agent key and its nonce: both are in the bytes the
// agent signed (proposal.SigningBytes), so an engine cannot change them and
// still pass Policy.Check. The client order id and the venue message are the
// engine's to choose, so neither can be the key.
//
// The record is written, the file replaced atomically and synced, before the
// share leaves the signer: a crash between the two refuses the order later
// rather than signing it twice. A record that cannot be read or written stops
// every order (RefuseOrderRecord) until it can, and never starts empty in its
// place. An entry is kept until the proposal's expiresAt plus
// orderRecordGrace, after which Policy.Check refuses the proposal anyway.
//
// Cancels are not recorded: pulling an order twice is harmless.
//
// A one-shot order (Prepare) is recorded too, before the agent's half of its
// message is made: the proposal, the client order id the message carries and
// the moment the message stops being good at the venue (oneShotUntil). When
// the rules park such an order, the engine asks for it again through the
// channel once a person approves. That request is refused, with a code of
// its own, until oneShotUntil plus oneShotGrace has passed, so the proposal
// never has two messages the venue would take at the same time; after that
// it is signed once, and only under the client order id the one-shot message
// carried. A second Prepare for the same proposal is refused.

// orderRecordGrace is how long past a proposal's expiresAt its entry is kept:
// the clock difference the checks allow elsewhere.
const orderRecordGrace = BinanceClockTolerance

// oneShotGrace is how long past a one-shot message's end at the venue the
// channel still refuses its proposal: how far the venue's clock may be from
// this machine's, the same allowance the message checks use.
const oneShotGrace = BinanceClockTolerance

// orderRecordLockWait bounds the wait for another process that holds the
// record's lock.
const orderRecordLockWait = 5 * time.Second

// ordersVersion is the file's format; another is refused rather than guessed at.
const ordersVersion = 1

// OrdersPathFor is where the record of signed orders lives for the config at
// config: beside it, agent.json's in agent.signed.json.
func OrdersPathFor(config string) string {
	return strings.TrimSuffix(config, ".json") + ".signed.json"
}

// signedOrder is one proposal the signer signed an order for: the proposal (its
// key and nonce, its connected account, when it ends) and the request that was
// signed. An entry Prepare wrote for a one-shot message has oneShotUntil, the
// moment that message stops being good at the venue, and no request id until
// a channel request is signed for the proposal. A signer from before the
// field reads such an entry as a signed order and signs nothing more for it.
type signedOrder struct {
	KeyID              string     `json:"keyId"`
	Nonce              string     `json:"nonce"`
	ConnectedAccountID string     `json:"connectedAccountId"`
	ExpiresAt          time.Time  `json:"expiresAt"`
	RequestID          string     `json:"requestId"`
	DecisionID         string     `json:"decisionId,omitempty"`
	ClientOrderID      string     `json:"clientOrderId,omitempty"`
	SignedAt           time.Time  `json:"signedAt"`
	OneShotUntil       *time.Time `json:"oneShotUntil,omitempty"`
}

type orderKey struct{ keyID, nonce string }

func (o signedOrder) key() orderKey { return orderKey{o.KeyID, o.Nonce} }

// oneShotOnly says the entry is Prepare's: a one-shot message went out for
// the proposal and no channel request has been signed for it.
func (o signedOrder) oneShotOnly() bool { return o.OneShotUntil != nil && o.RequestID == "" }

// channelMay says whether a channel request with this client order id may be
// signed now for a proposal whose entry is Prepare's: the one-shot message
// is past its end at the venue by more than oneShotGrace, and the request
// carries the id that message carried.
func (o signedOrder) channelMay(clientOrderID string, now time.Time) bool {
	return o.oneShotOnly() && now.After(o.OneShotUntil.Add(oneShotGrace)) && o.ClientOrderID != "" && clientOrderID == o.ClientOrderID
}

// ordersFile is the file's shape.
type ordersFile struct {
	Version int           `json:"version"`
	Orders  []signedOrder `json:"orders"`
}

// orderRecord is the record: the file at path (0600, beside the config), or
// with no path this process's memory only.
type orderRecord struct {
	path string
	mu   sync.Mutex
	mem  map[orderKey]signedOrder
}

// orders is the signer's record, made on first use: at OrdersPath, else
// beside ConfigPath, else in memory.
func (s *Signer) orders() *orderRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil {
		path := s.OrdersPath
		if path == "" && s.ConfigPath != "" {
			path = OrdersPathFor(s.ConfigPath)
		}
		s.record = &orderRecord{path: path, mem: map[orderKey]signedOrder{}}
	}
	return s.record
}

// find is the entry for a proposal, if there is one.
func (r *orderRecord) find(k orderKey) (signedOrder, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, err := r.read()
	if err != nil {
		return signedOrder{}, false, err
	}
	o, ok := m[k]
	return o, ok, nil
}

// add records o unless its proposal is in the record already, in which case
// that entry comes back and nothing changes. The one entry o takes the place
// of is Prepare's, when o is a channel request the entry allows now
// (channelMay); o then keeps the entry's oneShotUntil. Entries past their
// proposal's end go at the same time. With a path, the file is replaced and
// synced before add returns, under a lock that other processes on the same
// file take too.
func (r *orderRecord) add(o signedOrder, now time.Time) (signedOrder, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path == "" {
		got, seen := put(r.mem, o, now)
		return got, seen, nil
	}
	unlock, err := lockOrders(r.path)
	if err != nil {
		return signedOrder{}, false, err
	}
	defer unlock()
	m, err := r.read()
	if err != nil {
		return signedOrder{}, false, err
	}
	got, seen := put(m, o, now)
	if seen {
		return got, true, nil
	}
	if err := writeOrders(r.path, m); err != nil {
		return signedOrder{}, false, err
	}
	return got, false, nil
}

// put places o in m as add describes: the entry that stands in its way and
// true, or o as it now stands in m and false.
func put(m map[orderKey]signedOrder, o signedOrder, now time.Time) (signedOrder, bool) {
	if had, ok := m[o.key()]; ok {
		if o.RequestID == "" || !had.channelMay(o.ClientOrderID, now) {
			return had, true
		}
		o.OneShotUntil = had.OneShotUntil
	}
	prune(m, now)
	m[o.key()] = o
	return o, false
}

// count is how many proposals the record holds; for the start's log line.
func (r *orderRecord) count() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, err := r.read()
	return len(m), err
}

// read is the record as it stands: the file read again (another process may
// have written it), or the memory. A missing file is an empty record; one
// that does not parse, has another version or an entry without its proposal
// is an error, never an empty record.
func (r *orderRecord) read() (map[orderKey]signedOrder, error) {
	if r.path == "" {
		return r.mem, nil
	}
	raw, err := os.ReadFile(r.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[orderKey]signedOrder{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the record of signed orders %s cannot be read: %w", r.path, err)
	}
	var f ordersFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("the record of signed orders %s is not the JSON this signer writes: %w", r.path, err)
	}
	if f.Version != ordersVersion {
		return nil, fmt.Errorf("the record of signed orders %s is version %d, not %d", r.path, f.Version, ordersVersion)
	}
	m := make(map[orderKey]signedOrder, len(f.Orders))
	for i, o := range f.Orders {
		switch {
		case o.KeyID == "" || o.Nonce == "":
			return nil, fmt.Errorf("the record of signed orders %s: entry %d names no proposal (keyId and nonce)", r.path, i)
		case o.ExpiresAt.IsZero():
			return nil, fmt.Errorf("the record of signed orders %s: entry %d has no expiresAt", r.path, i)
		}
		if _, dup := m[o.key()]; dup {
			return nil, fmt.Errorf("the record of signed orders %s: proposal %s of key %s is in it twice", r.path, o.Nonce, o.KeyID)
		}
		m[o.key()] = o
	}
	return m, nil
}

// prune drops the entries whose proposal ended more than orderRecordGrace ago.
func prune(m map[orderKey]signedOrder, now time.Time) {
	for k, o := range m {
		if now.After(o.ExpiresAt.Add(orderRecordGrace)) {
			delete(m, k)
		}
	}
}

// writeOrders replaces the file with the record: a temporary file of mode 0600
// in the same directory, written and synced, renamed over it, and the
// directory synced, so the file is the old record or the new one, whole, after
// a crash.
func writeOrders(path string, m map[orderKey]signedOrder) error {
	f := ordersFile{Version: ordersVersion, Orders: make([]signedOrder, 0, len(m))}
	for _, o := range m {
		f.Orders = append(f.Orders, o)
	}
	sort.Slice(f.Orders, func(i, j int) bool {
		a, b := f.Orders[i], f.Orders[j]
		if !a.SignedAt.Equal(b.SignedAt) {
			return a.SignedAt.Before(b.SignedAt)
		}
		if a.KeyID != b.KeyID {
			return a.KeyID < b.KeyID
		}
		return a.Nonce < b.Nonce
	})
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	fail := func(err error) error {
		return fmt.Errorf("the record of signed orders %s cannot be written: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, ".signed-*.json")
	if err != nil {
		return fail(err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fail(err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fail(err)
	}
	if err := syncDir(dir); err != nil {
		return fail(err)
	}
	return nil
}

// lockOrders takes the record's lock (configlock on <path>.lock), waiting up
// to orderRecordLockWait for another process that holds it.
func lockOrders(path string) (func(), error) {
	deadline := time.Now().Add(orderRecordLockWait)
	wait := 5 * time.Millisecond
	for {
		l, err := configlock.Acquire(path)
		if err == nil {
			return func() { _ = l.Close() }, nil
		}
		var held *configlock.LockedError
		if !errors.As(err, &held) {
			return nil, fmt.Errorf("the record of signed orders %s cannot be locked: %w", path, err)
		}
		if time.Now().Add(wait).After(deadline) {
			return nil, fmt.Errorf("the record of signed orders %s is locked by another process (pid %d) for more than %s", path, held.PID, orderRecordLockWait)
		}
		time.Sleep(wait)
		wait = min(wait*2, 100*time.Millisecond)
	}
}

// logOrderRecord reads the record as the signer channel starts and says what
// it found: how many proposals it holds, or why no order will be signed.
func (s *Signer) logOrderRecord() {
	r := s.orders()
	n, err := r.count()
	switch {
	case err != nil:
		s.Logger.Error("signer: no order is signed until the record of signed orders is fixed; cancels still are", "err", err)
	case r.path == "":
		s.Logger.Info("signer: the record of signed orders is kept in memory, for this process only", "proposals", n)
	default:
		s.Logger.Info("signer: record of signed orders", "path", r.path, "proposals", n)
	}
}

// orderKeyOf is the proposal an order request signs for; false for anything
// that is not an order.
func orderKeyOf(req api.SignRequest) (orderKey, bool) {
	if req.Purpose != api.SignRequestPurposeOrder || req.Proposal == nil {
		return orderKey{}, false
	}
	return orderKey{req.Proposal.KeyId, req.Proposal.Nonce}, true
}

// signedAlready refuses an order for a proposal this signer signed an order
// for already, under any request id and whatever the message, and every
// order while the record cannot be read. The same request id as the one
// signed is the replay refusal, as within a session; another is
// RefuseOrderSigned.
func (s *Signer) signedAlready(req api.SignRequest) *Refusal {
	k, ok := orderKeyOf(req)
	if !ok {
		return nil
	}
	if k.nonce == "" {
		return &Refusal{RefuseMalformed, "the proposal carries no nonce, which is how this signer knows the orders it signed"}
	}
	had, seen, err := s.orders().find(k)
	if err != nil {
		return &Refusal{RefuseOrderRecord, err.Error() + "; no order is signed until it is fixed"}
	}
	if !seen {
		return nil
	}
	return signedRefusal(had, req, s.now())
}

// recordOneShot records the one-shot order Prepare is about to give its half
// for, before it does: the proposal, the client order id the message
// carries, and until, the moment the message stops being good at the venue.
// A proposal in the record already, signed or prepared, gets no second
// message, and none is given while the record cannot be written. A cancel is
// not recorded.
func (s *Signer) recordOneShot(p *api.ProposalSubmit, clientOrderID *string, until, now time.Time) *Refusal {
	if p.Kind != api.ProposalKindOrder {
		return nil
	}
	// The entry is filed under the config's key, the one the channel's checks
	// hold a proposal to. A config that names none files it under the
	// proposal's, and a proposal that names none is sent under the client's
	// (Client.Propose).
	keyID := s.Config.KeyID
	if keyID == "" {
		keyID = p.KeyId
	}
	if keyID == "" && s.Client != nil {
		keyID = s.Client.KeyID
	}
	if keyID == "" || p.Nonce == "" || clientOrderID == nil || *clientOrderID == "" {
		return &Refusal{RefuseMalformed, "the proposal carries no key id, no nonce or no client order id, which is how this signer knows the orders it signed"}
	}
	until = until.UTC()
	o := signedOrder{
		KeyID: keyID, Nonce: p.Nonce, ConnectedAccountID: p.ConnectedAccountId, ExpiresAt: p.ExpiresAt.UTC(),
		ClientOrderID: *clientOrderID, SignedAt: now.UTC(), OneShotUntil: &until,
	}
	had, seen, err := s.orders().add(o, now)
	if err != nil {
		return &Refusal{RefuseOrderRecord, err.Error() + "; no one-shot message was made"}
	}
	if seen {
		at := had.SignedAt.UTC().Format(time.RFC3339)
		return &Refusal{RefuseOrderSigned, fmt.Sprintf("an order for proposal %s of key %s was signed or prepared already, at %s; a proposal gets one message, so no second one is made for it", had.Nonce, had.KeyID, at)}
	}
	return nil
}

// recordSigned records the order about to be released, before it is: an
// order whose request expired while it was signed is not released (the
// engine no longer takes it, and recording it would refuse the engine's next
// try); one whose proposal another request was signed for meanwhile is
// refused; one the record cannot take is refused.
func (s *Signer) recordSigned(req api.SignRequest) *Refusal {
	k, ok := orderKeyOf(req)
	if !ok {
		return nil
	}
	now := s.now()
	if !now.Before(req.ExpiresAt) {
		return &Refusal{RefuseExpired, fmt.Sprintf("expired at %s while it was signed; nothing was released", req.ExpiresAt.UTC().Format(time.RFC3339))}
	}
	o := signedOrder{
		KeyID: k.keyID, Nonce: k.nonce, ConnectedAccountID: req.ConnectedAccountId, ExpiresAt: req.Proposal.ExpiresAt.UTC(),
		RequestID: req.Id, SignedAt: now.UTC(),
	}
	if req.DecisionId != nil {
		o.DecisionID = *req.DecisionId
	}
	if req.ClientOrderId != nil {
		o.ClientOrderID = *req.ClientOrderId
	}
	had, seen, err := s.orders().add(o, now)
	if err != nil {
		return &Refusal{RefuseOrderRecord, err.Error() + "; the signature was not released"}
	}
	if seen {
		if r := signedRefusal(had, req, now); r != nil {
			return r
		}
		// The entry allowed this request when it was checked and no longer
		// does: another request took its place meanwhile.
		return &Refusal{RefuseOrderSigned, fmt.Sprintf("an order for proposal %s of key %s was signed meanwhile; the signature was not released", had.Nonce, had.KeyID)}
	}
	return nil
}

// signedRefusal is the refusal of an order for a proposal in the record, or
// nil for the one request the record allows: a channel request for a
// proposal that went out one-shot, once that message is past its end at the
// venue by more than oneShotGrace, under the client order id it carried.
func signedRefusal(had signedOrder, req api.SignRequest, now time.Time) *Refusal {
	if had.oneShotOnly() {
		until := had.OneShotUntil.UTC()
		after := until.Add(oneShotGrace)
		if !now.After(after) {
			return &Refusal{RefuseOneShotLive, fmt.Sprintf("proposal %s of key %s went out with a one-shot message the venue takes until %s; an order for it is signed through the channel after %s, not before", had.Nonce, had.KeyID, until.Format(time.RFC3339), after.Format(time.RFC3339))}
		}
		cid := ""
		if req.ClientOrderId != nil {
			cid = *req.ClientOrderId
		}
		if !had.channelMay(cid, now) {
			return &Refusal{RefuseClientOrderID, fmt.Sprintf("proposal %s of key %s went out with a one-shot message under client order id %q; an order for it is signed through the channel under that id only, and this request carries %q", had.Nonce, had.KeyID, had.ClientOrderID, cid)}
		}
		return nil
	}
	at := had.SignedAt.UTC().Format(time.RFC3339)
	if had.RequestID == req.Id {
		return &Refusal{RefuseReplay, fmt.Sprintf("%s was answered already, at %s (attempt %d)", req.Id, at, req.Attempt)}
	}
	cid := ""
	if had.ClientOrderID != "" {
		cid = ", client order id " + had.ClientOrderID
	}
	return &Refusal{RefuseOrderSigned, fmt.Sprintf("an order for proposal %s of key %s was signed already (request %s%s, at %s); a proposal is signed for once, so no second order is signed for it", had.Nonce, had.KeyID, had.RequestID, cid, at)}
}
