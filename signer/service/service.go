// Package service is the signer in the engine's role: the one process on
// Mishmar's side that holds the engine's share of every venue key (FROST for
// Ed25519 venues, two-party ECDSA for Hyperliquid), the Paillier keys and
// the presigned nonces. The engine holds no signing secret; it runs the
// sessions with the agent's signer, relays the protocol messages here, and
// calls Finish once per order. The API is spec/signer-service.yaml
// (signerapi); `mishmar signer serve` runs it.
//
// The service checks the kind of action, never the policy: it decodes the
// action itself, signs orders and cancels only, rebuilds the venue digest
// from the action, and matches the action against the decision the engine
// signed with its own key, so an engine that was broken into can at most
// trade what its own decisions say. Presigned slots and commitments live in
// memory only and are spent on first use, whatever the outcome.
package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/ecdsa2p"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/signerapi"
	"github.com/mishmar-io/mishmar/venues/binance"
	"github.com/mishmar-io/mishmar/venues/hyperliquid"
)

// Lifetimes.
const (
	// SessionTTL is how long a key generation or presigning session may
	// stay open between its steps.
	SessionTTL = time.Minute
	// NonceTTL is how long a presigned slot or a FROST commitment stays usable.
	NonceTTL = 24 * time.Hour
	// MaxCommitments bounds one issue of FROST commitments.
	MaxCommitments = 1024
)

// Options configure a Service.
type Options struct {
	// Store holds the engine's shares, opened under the master key. Required.
	Store *Store
	// EnginePublicKey verifies the decisions the engine sends with each
	// signature; nil skips that check, for development only, and is logged.
	EnginePublicKey ed25519.PublicKey
	// Schemes limits what this instance holds; nil means both.
	Schemes []signerapi.Scheme
	Logger  *slog.Logger
	Now     func() time.Time
}

// Service implements the signer service's API.
type Service struct {
	opts      Options
	instance  string
	startedAt time.Time

	mu          sync.Mutex
	ecdsa       map[string]*ecdsa2p.Key
	frost       map[string]*frost.Share
	keygens     map[string]*keygenSession
	presigns    map[string]*presignSession
	slots       map[string]*slot
	commitments map[string]*commitment
}

type keygenSession struct {
	keyID   string
	run     *ecdsa2p.Keygen // secp256k1: two-party ECDSA
	frost   *frost.Keygen   // ed25519: two-party FROST
	started time.Time
}

func (k *keygenSession) abandon() {
	if k.run != nil {
		k.run.Abandon()
	}
	if k.frost != nil {
		k.frost.Abandon()
	}
}

type presignSession struct {
	keyID   string
	run     *ecdsa2p.Presign
	started time.Time
}

type slot struct {
	keyID   string
	presig  *ecdsa2p.Presig
	index   int
	expires time.Time
}

type commitment struct {
	keyID   string
	party   *frost.Party
	c       frost.Commitment
	expires time.Time
}

// New makes a service over the store.
func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("signer service: a store is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Schemes == nil {
		opts.Schemes = []signerapi.Scheme{signerapi.SchemeFrostEd25519, signerapi.SchemeEcdsa2p}
	}
	if opts.EnginePublicKey == nil {
		opts.Logger.Warn("signer service: no engine public key; decisions are not verified")
	}
	return &Service{
		opts: opts, instance: randomID("", 12), startedAt: opts.Now(),
		ecdsa: map[string]*ecdsa2p.Key{}, frost: map[string]*frost.Share{},
		keygens: map[string]*keygenSession{}, presigns: map[string]*presignSession{},
		slots: map[string]*slot{}, commitments: map[string]*commitment{},
	}, nil
}

// Instance is this run's id; everything held in memory is gone with it.
func (s *Service) Instance() string { return s.instance }

// Handler serves the API.
func (s *Service) Handler() http.Handler {
	h := signerapi.Handler(signerapi.NewStrictHandler(s, nil))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(signerapi.InstanceHeader, s.instance)
		h.ServeHTTP(w, r)
	})
}

// ---- errors ----

type failure struct {
	code   signerapi.ErrorCode
	status int
	msg    string
}

func (f *failure) Error() string { return string(f.code) + ": " + f.msg }

func notFound(format string, a ...any) error {
	return &failure{signerapi.ErrorCodeNotFound, http.StatusNotFound, fmt.Sprintf(format, a...)}
}

func conflict(format string, a ...any) error {
	return &failure{signerapi.ErrorCodeConflict, http.StatusConflict, fmt.Sprintf(format, a...)}
}

func invalid(format string, a ...any) error {
	return &failure{signerapi.ErrorCodeValidation, http.StatusUnprocessableEntity, fmt.Sprintf(format, a...)}
}

func refused(format string, a ...any) error {
	return &failure{signerapi.ErrorCodeRefused, http.StatusUnprocessableEntity, fmt.Sprintf(format, a...)}
}

// errBody is the error's API shape and status; anything not a failure is internal.
func errBody(err error) (signerapi.Error, int) {
	var f *failure
	if errors.As(err, &f) {
		return signerapi.Error{Code: f.code, Message: f.msg}, f.status
	}
	return signerapi.Error{Code: signerapi.ErrorCodeInternal, Message: err.Error()}, http.StatusInternalServerError
}

func (s *Service) holds(scheme signerapi.Scheme) bool {
	for _, x := range s.opts.Schemes {
		if x == scheme {
			return true
		}
	}
	return false
}

// ---- keys ----

// ecdsaKey is the engine's ECDSA key for a venue key, parsed once.
func (s *Service) ecdsaKey(id string) (*ecdsa2p.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.ecdsa[id]; ok {
		return k, nil
	}
	e, ok := s.opts.Store.Get(id)
	if !ok {
		return nil, notFound("no key %s", id)
	}
	if e.Scheme != signerapi.SchemeEcdsa2p {
		return nil, conflict("%s is a %s key", id, e.Scheme)
	}
	k, err := ecdsa2p.ParseKey(e.Share)
	if err != nil {
		return nil, err
	}
	if k.Role() != ecdsa2p.RoleEngine {
		return nil, conflict("%s holds the agent's key, not the engine's", id)
	}
	s.ecdsa[id] = k
	return k, nil
}

// frostShare is the engine's FROST share of a venue key, parsed once.
func (s *Service) frostShare(id string) (*frost.Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.frost[id]; ok {
		return sh, nil
	}
	e, ok := s.opts.Store.Get(id)
	if !ok {
		return nil, notFound("no key %s", id)
	}
	if e.Scheme != signerapi.SchemeFrostEd25519 {
		return nil, conflict("%s is a %s key", id, e.Scheme)
	}
	sh, err := frost.ParseShare(e.Share)
	if err != nil {
		return nil, err
	}
	if sh.ID() != frost.EngineID {
		return nil, conflict("%s holds the agent's share, not the engine's", id)
	}
	s.frost[id] = sh
	return sh, nil
}

func (s *Service) keyInfo(id string) (signerapi.Key, error) {
	e, ok := s.opts.Store.Get(id)
	if !ok {
		return signerapi.Key{}, notFound("no key %s", id)
	}
	out := signerapi.Key{KeyId: id, Scheme: e.Scheme}
	switch e.Scheme {
	case signerapi.SchemeEcdsa2p:
		k, err := s.ecdsaKey(id)
		if err != nil {
			return signerapi.Key{}, err
		}
		out.PublicKey = base64.StdEncoding.EncodeToString(k.PublicKey())
		addr := k.Address()
		out.Address = &addr
	case signerapi.SchemeFrostEd25519:
		sh, err := s.frostShare(id)
		if err != nil {
			return signerapi.Key{}, err
		}
		out.PublicKey = base64.StdEncoding.EncodeToString(sh.PublicKey())
	}
	n := s.unused(id)
	out.UnusedNonces = &n
	return out, nil
}

// unused counts the key's unexpired slots and commitments, dropping expired ones.
func (s *Service) unused(id string) int {
	now := s.opts.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for sid, sl := range s.slots {
		switch {
		case !now.Before(sl.expires):
			delete(s.slots, sid)
		case sl.keyID == id:
			n++
		}
	}
	for cid, c := range s.commitments {
		switch {
		case !now.Before(c.expires):
			delete(s.commitments, cid)
		case c.keyID == id:
			n++
		}
	}
	return n
}

func (s *Service) Health(context.Context, signerapi.HealthRequestObject) (signerapi.HealthResponseObject, error) {
	return signerapi.Health200JSONResponse(signerapi.Health{
		Instance: s.instance, Schemes: s.opts.Schemes, Keys: len(s.opts.Store.IDs()), StartedAt: s.startedAt,
	}), nil
}

func (s *Service) GetKey(_ context.Context, r signerapi.GetKeyRequestObject) (signerapi.GetKeyResponseObject, error) {
	k, err := s.keyInfo(r.KeyId)
	if err != nil {
		body, st := errBody(err)
		if st == http.StatusNotFound {
			return signerapi.GetKey404JSONResponse{ErrorJSONResponse: signerapi.ErrorJSONResponse(body)}, nil
		}
		return signerapi.GetKeydefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	return signerapi.GetKey200JSONResponse(k), nil
}

func (s *Service) ImportKey(_ context.Context, r signerapi.ImportKeyRequestObject) (signerapi.ImportKeyResponseObject, error) {
	fail := func(err error) (signerapi.ImportKeyResponseObject, error) {
		body, st := errBody(err)
		return signerapi.ImportKeydefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	if r.Body == nil || r.Body.Share == "" {
		return fail(invalid("a share is required"))
	}
	if !s.holds(r.Body.Scheme) {
		return fail(conflict("this instance holds no %s keys", r.Body.Scheme))
	}
	share := json.RawMessage(r.Body.Share)
	switch r.Body.Scheme {
	case signerapi.SchemeFrostEd25519:
		sh, err := frost.ParseShare(share)
		if err != nil {
			return fail(invalid("share: %v", err))
		}
		if sh.ID() != frost.EngineID {
			return fail(invalid("the share is the agent's, not the engine's"))
		}
	case signerapi.SchemeEcdsa2p:
		k, err := ecdsa2p.ParseKey(share)
		if err != nil {
			return fail(invalid("share: %v", err))
		}
		if k.Role() != ecdsa2p.RoleEngine {
			return fail(invalid("the key is the agent's, not the engine's"))
		}
	default:
		return fail(invalid("scheme %q is unknown", r.Body.Scheme))
	}
	if err := s.opts.Store.Put(r.KeyId, Entry{Scheme: r.Body.Scheme, Share: share}); err != nil {
		return fail(conflict("%v", err))
	}
	k, err := s.keyInfo(r.KeyId)
	if err != nil {
		return fail(err)
	}
	s.opts.Logger.Info("signer service: key imported", "key", r.KeyId, "scheme", r.Body.Scheme)
	return signerapi.ImportKey201JSONResponse(k), nil
}

func (s *Service) ForgetKey(_ context.Context, r signerapi.ForgetKeyRequestObject) (signerapi.ForgetKeyResponseObject, error) {
	if err := s.opts.Store.Delete(r.KeyId); err != nil {
		body, st := errBody(err)
		return signerapi.ForgetKeydefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	s.forget(r.KeyId)
	s.opts.Logger.Info("signer service: key forgotten", "key", r.KeyId)
	return signerapi.ForgetKey204Response{}, nil
}

// forget lets go of everything held in memory for a key the store no
// longer has: the parsed key, open sessions, slots and commitments.
func (s *Service) forget(keyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ecdsa, keyID)
	delete(s.frost, keyID)
	for id, k := range s.keygens {
		if k.keyID == keyID {
			k.abandon()
			delete(s.keygens, id)
		}
	}
	for id, p := range s.presigns {
		if p.keyID == keyID {
			p.run.Abandon()
			delete(s.presigns, id)
		}
	}
	for id, sl := range s.slots {
		if sl.keyID == keyID {
			delete(s.slots, id)
		}
	}
	for id, c := range s.commitments {
		if c.keyID == keyID {
			delete(s.commitments, id)
		}
	}
}

// Sync reconciles the store with its backend, so a replica holds the keys
// another replica generated and lets go of the ones it forgot. Run it on a
// timer when replicas share a backend; on a file it is a no-op.
func (s *Service) Sync() error {
	added, dropped, err := s.opts.Store.Sync()
	if err != nil {
		return err
	}
	for _, id := range dropped {
		s.forget(id)
	}
	if len(added) > 0 || len(dropped) > 0 {
		s.opts.Logger.Info("signer service: keys synced", "added", added, "dropped", dropped)
	}
	return nil
}

// ---- two-party ECDSA ----

func (s *Service) KeygenStep(_ context.Context, r signerapi.KeygenStepRequestObject) (signerapi.KeygenStepResponseObject, error) {
	fail := func(err error) (signerapi.KeygenStepResponseObject, error) {
		body, st := errBody(err)
		return signerapi.KeygenStepdefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	if r.Body == nil || r.Body.SessionId == "" {
		return fail(invalid("a session id is required"))
	}
	now := s.opts.Now()
	s.sweep(now)
	switch r.Body.Step {
	case signerapi.KeygenStepStepN1:
		curve := signerapi.KeygenStepCurveSecp256k1
		if r.Body.Curve != nil {
			curve = *r.Body.Curve
		}
		var scheme signerapi.Scheme
		switch curve {
		case signerapi.KeygenStepCurveSecp256k1:
			scheme = signerapi.SchemeEcdsa2p
		case signerapi.KeygenStepCurveEd25519:
			scheme = signerapi.SchemeFrostEd25519
		default:
			return fail(invalid("curve %s is not secp256k1 or ed25519", curve))
		}
		if !s.holds(scheme) {
			return fail(conflict("this instance holds no %s keys", scheme))
		}
		if _, held := s.opts.Store.Get(r.KeyId); held {
			return fail(conflict("key %s is held already", r.KeyId))
		}
		ses := &keygenSession{keyID: r.KeyId, started: now}
		switch scheme {
		case signerapi.SchemeEcdsa2p:
			run, err := ecdsa2p.NewKeygen(ecdsa2p.RoleEngine)
			if err != nil {
				return fail(err)
			}
			ses.run = run
		case signerapi.SchemeFrostEd25519:
			fk, err := frost.NewKeygen(frost.EngineID)
			if err != nil {
				return fail(err)
			}
			ses.frost = fk
		}
		s.mu.Lock()
		if _, dup := s.keygens[r.Body.SessionId]; dup {
			s.mu.Unlock()
			ses.abandon()
			return fail(conflict("session %s is open already", r.Body.SessionId))
		}
		s.keygens[r.Body.SessionId] = ses
		s.mu.Unlock()
		var m1 []byte
		var err error
		if ses.run != nil {
			m1, err = ses.run.First()
		} else {
			m1, err = ses.frost.First()
		}
		if err != nil {
			s.dropKeygen(r.Body.SessionId)
			return fail(err)
		}
		return signerapi.KeygenStep200JSONResponse(signerapi.KeygenStepResult{Message: b64(m1)}), nil
	case signerapi.KeygenStepStepN2:
		s.mu.Lock()
		ses, ok := s.keygens[r.Body.SessionId]
		if ok {
			delete(s.keygens, r.Body.SessionId)
		}
		s.mu.Unlock()
		if !ok || ses.keyID != r.KeyId {
			return fail(notFound("no open key generation session %s for %s", r.Body.SessionId, r.KeyId))
		}
		m2, err := decodeB64(r.Body.AgentMessage)
		if err != nil {
			ses.abandon()
			return fail(invalid("agentMessage: %v", err))
		}
		if ses.frost != nil {
			last, share, err := ses.frost.Last(m2)
			if err != nil {
				return fail(refused("key generation failed: %v", err))
			}
			raw, err := json.Marshal(share)
			if err != nil {
				return fail(err)
			}
			if err := s.opts.Store.Put(r.KeyId, Entry{Scheme: signerapi.SchemeFrostEd25519, Share: raw}); err != nil {
				return fail(conflict("%v", err))
			}
			s.mu.Lock()
			s.frost[r.KeyId] = share
			s.mu.Unlock()
			s.opts.Logger.Info("signer service: key generated", "key", r.KeyId, "scheme", signerapi.SchemeFrostEd25519)
			return signerapi.KeygenStep200JSONResponse(signerapi.KeygenStepResult{
				Message: b64(last), PublicKey: ptr(b64(share.PublicKey())),
			}), nil
		}
		m3, key, err := ses.run.Last(m2)
		if err != nil {
			return fail(refused("key generation failed: %v", err))
		}
		raw, err := json.Marshal(key)
		if err != nil {
			return fail(err)
		}
		if err := s.opts.Store.Put(r.KeyId, Entry{Scheme: signerapi.SchemeEcdsa2p, Share: raw}); err != nil {
			return fail(conflict("%v", err))
		}
		s.mu.Lock()
		s.ecdsa[r.KeyId] = key
		s.mu.Unlock()
		addr := key.Address()
		s.opts.Logger.Info("signer service: key generated", "key", r.KeyId, "scheme", signerapi.SchemeEcdsa2p, "address", addr)
		return signerapi.KeygenStep200JSONResponse(signerapi.KeygenStepResult{
			Message: b64(m3), PublicKey: ptr(b64(key.PublicKey())), Address: &addr,
		}), nil
	}
	return fail(invalid("step %d is not 1 or 2", r.Body.Step))
}

func (s *Service) dropKeygen(sessionID string) {
	s.mu.Lock()
	if k, ok := s.keygens[sessionID]; ok {
		k.run.Abandon()
		delete(s.keygens, sessionID)
	}
	s.mu.Unlock()
}

func (s *Service) PresignStep(_ context.Context, r signerapi.PresignStepRequestObject) (signerapi.PresignStepResponseObject, error) {
	fail := func(err error) (signerapi.PresignStepResponseObject, error) {
		body, st := errBody(err)
		return signerapi.PresignStepdefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	if r.Body == nil || r.Body.SessionId == "" {
		return fail(invalid("a session id is required"))
	}
	key, err := s.ecdsaKey(r.KeyId)
	if err != nil {
		return fail(err)
	}
	now := s.opts.Now()
	s.sweep(now)
	switch r.Body.Step {
	case signerapi.PresignStepStepN1:
		if r.Body.Count == nil || *r.Body.Count < 1 || *r.Body.Count > ecdsa2p.MaxPresign {
			return fail(invalid("count must be 1 to %d", ecdsa2p.MaxPresign))
		}
		if r.Body.Sid == nil {
			return fail(invalid("the session id's bytes (sid) are required in step 1"))
		}
		sid, err := decodeB64(r.Body.Sid)
		if err != nil || len(sid) != 32 {
			return fail(invalid("sid must be 32 bytes, base64"))
		}
		run, err := key.NewPresign(*r.Body.Count, sid)
		if err != nil {
			return fail(invalid("%v", err))
		}
		s.mu.Lock()
		if _, dup := s.presigns[r.Body.SessionId]; dup {
			s.mu.Unlock()
			run.Abandon()
			return fail(conflict("session %s is open already", r.Body.SessionId))
		}
		s.presigns[r.Body.SessionId] = &presignSession{keyID: r.KeyId, run: run, started: now}
		s.mu.Unlock()
		m1, err := run.First()
		if err != nil {
			s.mu.Lock()
			delete(s.presigns, r.Body.SessionId)
			s.mu.Unlock()
			run.Abandon()
			return fail(err)
		}
		return signerapi.PresignStep200JSONResponse(signerapi.PresignStepResult{Message: b64(m1)}), nil
	case signerapi.PresignStepStepN2:
		s.mu.Lock()
		ses, ok := s.presigns[r.Body.SessionId]
		if ok {
			delete(s.presigns, r.Body.SessionId)
		}
		s.mu.Unlock()
		if !ok || ses.keyID != r.KeyId {
			return fail(notFound("no open presigning session %s for %s", r.Body.SessionId, r.KeyId))
		}
		m2, err := decodeB64(r.Body.AgentMessage)
		if err != nil {
			ses.run.Abandon()
			return fail(invalid("agentMessage: %v", err))
		}
		m3, presig, err := ses.run.Last(m2)
		if err != nil {
			return fail(refused("presigning failed: %v", err))
		}
		expires := now.Add(NonceTTL)
		slots := make([]signerapi.Slot, presig.Len())
		s.mu.Lock()
		for i := range slots {
			id := randomID("ps_", 12)
			s.slots[id] = &slot{keyID: r.KeyId, presig: presig, index: i, expires: expires}
			slots[i] = signerapi.Slot{Id: id, ExpiresAt: expires}
		}
		s.mu.Unlock()
		return signerapi.PresignStep200JSONResponse(signerapi.PresignStepResult{Message: b64(m3), Slots: &slots}), nil
	}
	return fail(invalid("step %d is not 1 or 2", r.Body.Step))
}

// sweep drops sessions past their time.
func (s *Service) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, k := range s.keygens {
		if now.Sub(k.started) > SessionTTL {
			k.abandon()
			delete(s.keygens, id)
		}
	}
	for id, p := range s.presigns {
		if now.Sub(p.started) > SessionTTL {
			p.run.Abandon()
			delete(s.presigns, id)
		}
	}
}

func (s *Service) Finish(_ context.Context, r signerapi.FinishRequestObject) (signerapi.FinishResponseObject, error) {
	fail := func(err error) (signerapi.FinishResponseObject, error) {
		body, st := errBody(err)
		s.opts.Logger.Info("signer service: finish refused", "key", r.KeyId, "reason", body.Message)
		return signerapi.FinishdefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	if r.Body == nil || r.Body.SlotId == "" {
		return fail(invalid("a slot id is required"))
	}
	key, err := s.ecdsaKey(r.KeyId)
	if err != nil {
		return fail(err)
	}
	now := s.opts.Now()
	// The slot is spent first, whatever comes next.
	s.mu.Lock()
	sl, ok := s.slots[r.Body.SlotId]
	delete(s.slots, r.Body.SlotId)
	s.mu.Unlock()
	switch {
	case !ok || sl.keyID != r.KeyId:
		return fail(conflict("slot %s is unknown, spent or not %s's", r.Body.SlotId, r.KeyId))
	case !now.Before(sl.expires):
		return fail(conflict("slot %s has expired", r.Body.SlotId))
	}
	purpose := signerapi.FinishRequestPurposeOrder
	if r.Body.Purpose != nil {
		purpose = *r.Body.Purpose
	}
	if purpose == signerapi.FinishRequestPurposeVerify {
		return fail(refused("a Hyperliquid key is verified by its approval; there is nothing to sign"))
	}
	if r.Body.VenueAction.Venue != signerapi.VenueActionVenueHyperliquid {
		return fail(refused("a two-party ECDSA key signs Hyperliquid actions, not %s", r.Body.VenueAction.Venue))
	}
	if r.Body.VenueAction.Network == nil {
		return fail(invalid("the Hyperliquid network is required"))
	}
	network, err := hyperliquid.ParseNetwork(*r.Body.VenueAction.Network)
	if err != nil {
		return fail(invalid("%v", err))
	}
	raw, err := decodeB64(&r.Body.VenueAction.Action)
	if err != nil {
		return fail(invalid("action: %v", err))
	}
	m, action, err := hyperliquid.Decode(raw)
	if err != nil {
		return fail(refused("%v", err))
	}
	if err := hyperliquid.CheckFresh(m.Nonce, now); err != nil {
		return fail(refused("%v", err))
	}
	rec, err := s.decision(r.Body.Decision)
	if err != nil {
		return fail(err)
	}
	if err := matchHyperliquid(action, m, rec, string(purpose)); err != nil {
		return fail(err)
	}
	digest, err := hyperliquid.Digest(action, m.Nonce, network)
	if err != nil {
		return fail(err)
	}
	agentMessage, err := decodeB64(&r.Body.AgentMessage)
	if err != nil {
		return fail(invalid("agentMessage: %v", err))
	}
	sig, err := key.Finish(sl.presig, sl.index, digest, agentMessage)
	if err != nil {
		return fail(refused("%v", err))
	}
	var out signerapi.FinishResult
	out.Signature.R = minimalHex(sig.R[:])
	out.Signature.S = minimalHex(sig.S[:])
	out.Signature.V = 27 + int(sig.RecoveryID)
	s.opts.Logger.Info("signer service: signed", "key", r.KeyId, "venue", "hyperliquid", "purpose", purpose, "slot", r.Body.SlotId)
	return signerapi.Finish200JSONResponse(out), nil
}

// decisionRecord is what the service reads of the decision the engine signed.
type decisionRecord struct {
	Verdict  api.Verdict  `json:"verdict"`
	Proposal api.Proposal `json:"proposal"`
}

// decision verifies the engine's signature over the decision record and
// reads it. Without a decision, or without an engine key to check it with,
// nil: the action is then checked for its kind alone.
func (s *Service) decision(d *signerapi.Decision) (*decisionRecord, error) {
	if d == nil {
		if s.opts.EnginePublicKey != nil {
			return nil, refused("the request carries no decision")
		}
		return nil, nil
	}
	record, err := decodeB64(&d.Record)
	if err != nil {
		return nil, invalid("decision.record: %v", err)
	}
	sig, err := decodeB64(&d.Signature)
	if err != nil {
		return nil, invalid("decision.signature: %v", err)
	}
	if s.opts.EnginePublicKey != nil && !ed25519.Verify(s.opts.EnginePublicKey, record, sig) {
		return nil, refused("the decision's engine signature does not verify")
	}
	var rec decisionRecord
	if err := json.Unmarshal(record, &rec); err != nil {
		return nil, invalid("decision.record: %v", err)
	}
	if rec.Verdict != api.VerdictAllow {
		return nil, refused("the decision's verdict is %s, not allow", rec.Verdict)
	}
	return &rec, nil
}

// matchHyperliquid checks the action is the decision's order or cancel:
// the cloid derives from the decision's client order id (or the cancel's
// target), the side, size, reduce-only and limit price are the proposal's.
// The asset index is not checked here: the agent's signer checked it when
// it built the action, and its message binds the action.
func matchHyperliquid(action hyperliquid.Action, m hyperliquid.Message, rec *decisionRecord, purpose string) error {
	if rec == nil {
		return nil
	}
	p := rec.Proposal
	if purpose == "cancel" {
		if p.Kind != api.ProposalKindCancel || p.TargetClientOrderId == nil {
			return refused("the decision is not a cancel")
		}
		c, err := hyperliquid.DecodeCancel(action)
		if err != nil {
			return refused("%v", err)
		}
		if c.Cloid != hyperliquid.CloidFromString(*p.TargetClientOrderId) {
			return refused("the cancel is not for the decision's target %s", *p.TargetClientOrderId)
		}
		return nil
	}
	o, err := hyperliquid.DecodeOrder(action)
	if err != nil {
		return refused("%v", err)
	}
	switch {
	case p.Kind != api.ProposalKindOrder:
		return refused("the decision is a %s, not an order", p.Kind)
	case m.ClientOrderID != p.ClientOrderId || o.Cloid != hyperliquid.CloidFromString(p.ClientOrderId):
		return refused("the order is not the decision's client order id %s", p.ClientOrderId)
	case o.Side != string(p.Side):
		return refused("side %s is not the decision's %s", o.Side, p.Side)
	case o.ReduceOnly != p.ReduceOnly:
		return refused("reduceOnly is %t, the decision says %t", o.ReduceOnly, p.ReduceOnly)
	}
	size, err := decimal.NewFromString(o.Size)
	want, wantErr := decimal.NewFromString(p.Size)
	if err != nil || wantErr != nil || !size.IsPositive() || size.GreaterThan(want) {
		return refused("size %s is not within the decision's %s", o.Size, p.Size)
	}
	price, err := decimal.NewFromString(o.Price)
	if err != nil || !price.IsPositive() {
		return refused("price %q is not a positive decimal", o.Price)
	}
	switch p.OrderType {
	case api.ProposalOrderTypeLimit:
		if o.TIF != string(hyperliquid.Gtc) {
			return refused("a limit order is sent Gtc, not %s", o.TIF)
		}
		if p.Price == nil {
			return refused("the decision's limit order has no price")
		}
		limit, err := decimal.NewFromString(*p.Price)
		switch {
		case err != nil:
			return refused("the decision's price %q is not a decimal", *p.Price)
		case o.Side == "buy" && price.GreaterThan(limit), o.Side == "sell" && price.LessThan(limit):
			return refused("price %s is past the decision's %s", o.Price, *p.Price)
		}
	default:
		if o.TIF != string(hyperliquid.Ioc) {
			return refused("a market order is sent Ioc, not %s", o.TIF)
		}
	}
	return nil
}

// ---- FROST ----

func (s *Service) IssueCommitments(_ context.Context, r signerapi.IssueCommitmentsRequestObject) (signerapi.IssueCommitmentsResponseObject, error) {
	fail := func(err error) (signerapi.IssueCommitmentsResponseObject, error) {
		body, st := errBody(err)
		return signerapi.IssueCommitmentsdefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	if r.Body == nil || r.Body.Count < 1 || r.Body.Count > MaxCommitments {
		return fail(invalid("count must be 1 to %d", MaxCommitments))
	}
	share, err := s.frostShare(r.KeyId)
	if err != nil {
		return fail(err)
	}
	expires := s.opts.Now().Add(NonceTTL)
	out := make([]signerapi.Commitment, 0, r.Body.Count)
	s.mu.Lock()
	defer s.mu.Unlock()
	for range r.Body.Count {
		party, err := share.Party()
		if err != nil {
			return fail(err)
		}
		c := party.Commit()
		id := randomID("cm_", 12)
		s.commitments[id] = &commitment{keyID: r.KeyId, party: party, c: c, expires: expires}
		out = append(out, signerapi.Commitment{Id: id, Commitment: b64(c.Bytes()), ExpiresAt: expires})
	}
	return signerapi.IssueCommitments200JSONResponse(out), nil
}

func (s *Service) FrostSign(_ context.Context, r signerapi.FrostSignRequestObject) (signerapi.FrostSignResponseObject, error) {
	fail := func(err error) (signerapi.FrostSignResponseObject, error) {
		body, st := errBody(err)
		s.opts.Logger.Info("signer service: frost sign refused", "key", r.KeyId, "reason", body.Message)
		return signerapi.FrostSigndefaultJSONResponse{Body: body, StatusCode: st}, nil
	}
	if r.Body == nil || r.Body.CommitmentId == "" {
		return fail(invalid("a commitment id is required"))
	}
	share, err := s.frostShare(r.KeyId)
	if err != nil {
		return fail(err)
	}
	now := s.opts.Now()
	s.mu.Lock()
	c, ok := s.commitments[r.Body.CommitmentId]
	delete(s.commitments, r.Body.CommitmentId)
	s.mu.Unlock()
	switch {
	case !ok || c.keyID != r.KeyId:
		return fail(conflict("commitment %s is unknown, spent or not %s's", r.Body.CommitmentId, r.KeyId))
	case !now.Before(c.expires):
		return fail(conflict("commitment %s has expired", r.Body.CommitmentId))
	}
	if r.Body.VenueAction.Venue != signerapi.VenueActionVenueBinance {
		return fail(refused("a FROST key signs Binance requests, not %s", r.Body.VenueAction.Venue))
	}
	message, err := decodeB64(&r.Body.VenueAction.Action)
	if err != nil {
		return fail(invalid("action: %v", err))
	}
	purpose := signerapi.FrostSignRequestPurposeOrder
	if r.Body.Purpose != nil {
		purpose = *r.Body.Purpose
	}
	if purpose == signerapi.FrostSignRequestPurposeVerify {
		if err := binance.CheckRead(message); err != nil {
			return fail(refused("%v", err))
		}
	} else {
		rec, err := s.decision(r.Body.Decision)
		if err != nil {
			return fail(err)
		}
		if err := matchBinance(message, rec, string(purpose), now); err != nil {
			return fail(err)
		}
	}
	acRaw, err := decodeB64(&r.Body.AgentCommitment)
	if err != nil {
		return fail(invalid("agentCommitment: %v", err))
	}
	ac, err := frost.ParseCommitment(acRaw)
	if err != nil {
		return fail(invalid("agentCommitment: %v", err))
	}
	asRaw, err := decodeB64(&r.Body.AgentShare)
	if err != nil {
		return fail(invalid("agentShare: %v", err))
	}
	as, err := frost.ParseSignatureShare(asRaw)
	if err != nil {
		return fail(invalid("agentShare: %v", err))
	}
	if err := share.VerifyShare(message, as, c.c, ac); err != nil {
		return fail(refused("the agent's share does not verify over the message: %v", err))
	}
	es, err := c.party.Sign(message, c.c, ac)
	if err != nil {
		return fail(err)
	}
	sig, err := share.Aggregate(message, []frost.Commitment{c.c, ac}, []frost.SignatureShare{as, es})
	if err != nil {
		return fail(err)
	}
	s.opts.Logger.Info("signer service: signed", "key", r.KeyId, "venue", "binance", "purpose", purpose, "commitment", r.Body.CommitmentId)
	return signerapi.FrostSign200JSONResponse{Signature: b64(sig)}, nil
}

// matchBinance checks a Binance request is the decision's order or cancel,
// with the public package's own check, and fresh.
func matchBinance(message []byte, rec *decisionRecord, purpose string, now time.Time) error {
	if purpose == "cancel" {
		c, err := binance.DecodeCancel(message)
		if err != nil {
			return refused("%v", err)
		}
		if err := binance.CheckFresh(c.Timestamp, c.RecvWindow, now); err != nil {
			return refused("%v", err)
		}
		if rec == nil {
			return nil
		}
		if rec.Proposal.Kind != api.ProposalKindCancel || rec.Proposal.TargetClientOrderId == nil || c.ClientOrderID != *rec.Proposal.TargetClientOrderId {
			return refused("the cancel is not for the decision's target")
		}
		return nil
	}
	o, err := binance.DecodeOrder(message)
	if err != nil {
		return refused("%v", err)
	}
	if err := binance.CheckFresh(o.Timestamp, o.RecvWindow, now); err != nil {
		return refused("%v", err)
	}
	if rec == nil {
		return nil
	}
	p := rec.Proposal
	if p.Kind != api.ProposalKindOrder {
		return refused("the decision is a %s, not an order", p.Kind)
	}
	in := binance.Intent{Symbol: p.Symbol, Side: string(p.Side), OrderType: string(p.OrderType), Size: p.Size, ReduceOnly: p.ReduceOnly}
	if p.Price != nil {
		in.Price = *p.Price
	}
	if err := binance.CheckOrder(o, in, o.Symbol, p.ClientOrderId); err != nil {
		return refused("%v", err)
	}
	return nil
}

// ---- helpers ----

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func decodeB64(s *string) ([]byte, error) {
	if s == nil || *s == "" {
		return nil, errors.New("missing")
	}
	raw, err := base64.StdEncoding.DecodeString(*s)
	if err != nil {
		return nil, errors.New("not base64")
	}
	return raw, nil
}

func minimalHex(b []byte) string { return "0x" + new(big.Int).SetBytes(b).Text(16) }

func ptr[T any](v T) *T { return &v }

const idAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// randomID is prefix plus n random base32 characters.
func randomID(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("signer service: " + err.Error())
	}
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	if prefix == "" {
		return hex.EncodeToString(b)[:n]
	}
	return prefix + string(b)
}
