package signer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/proposal"
	"github.com/mishmar-io/mishmar/venues/binance"
)

// One-shot signing: the signer builds the venue request itself and signs its
// half before the proposal leaves the agent's machine
// (ProposalSubmit.venueMessage), so an allowed order needs no round trip from
// the engine back to the signer. The engine decodes the message, checks it
// against the proposal, decides, and only then adds its half and forwards.
// Anything that keeps a proposal from going one-shot sends it through the
// channel instead, which is slower and otherwise the same.

const (
	// rulesTTL is how long a symbol's venue rules are kept.
	rulesTTL = 10 * time.Minute
	// commitmentMargin: a commitment this close to its expiry is not used.
	commitmentMargin = 30 * time.Second
)

// NoOneShot is why a proposal could not go one-shot; it goes through the
// channel instead.
type NoOneShot struct{ Reason string }

func (n *NoOneShot) Error() string { return "signer: not one-shot: " + n.Reason }

func noOneShot(format string, a ...any) error { return &NoOneShot{fmt.Sprintf(format, a...)} }

// oneShotState is the signer's cache for one-shot signing: venue rules per
// trading account and symbol, and the engine's unused commitments per venue
// key with the engine instance that issued them.
type oneShotState struct {
	mu          sync.Mutex
	rules       map[string]cachedRules
	commitments map[string][]api.EngineCommitment
	instance    string
	refilling   map[string]bool
}

type cachedRules struct {
	rules   binance.Rules
	fetched time.Time
}

// Prepare builds the venue message for an unsigned proposal and adds the
// agent's half of the signature, so the proposal goes out one-shot. It
// returns a *NoOneShot, leaving the proposal as it was, when the proposal is
// already signed or carries a message, when the agent holds no venue key on
// the trading account or the venue is not one the signer builds for
// (Binance today), when the config names no agent, when the engine has no
// commitment or rules to give, or when the hook refuses. Propose signs the
// proposal afterwards.
func (s *Signer) Prepare(ctx context.Context, p *api.ProposalSubmit) error {
	switch {
	case p.Signature != "":
		return noOneShot("the proposal is signed already")
	case p.VenueMessage != nil:
		return noOneShot("the proposal carries a venue message already")
	case p.Kind != api.ProposalKindOrder && p.Kind != api.ProposalKindCancel:
		return noOneShot("a %s is not built by the signer", p.Kind)
	case s.Config.AgentID == "":
		return noOneShot("the config names no agentId")
	}
	vkID, vk, ok := s.venueKeyOn(p.TradingAccountId)
	if !ok {
		return noOneShot("no venue key on %s in the config", p.TradingAccountId)
	}
	if api.Venue(vk.Venue) != api.VenueBinance || vk.Scheme != SchemeFrostEd25519 {
		return noOneShot("%s is a %s key; one-shot builds Binance FROST orders only", vkID, vk.Venue)
	}
	now := s.now()
	var message []byte
	var err error
	req := api.SignRequest{
		AgentId: s.Config.AgentID, TradingAccountId: p.TradingAccountId, VenueKeyId: vkID, Venue: api.VenueBinance,
		Scheme: api.SigningSchemeFrostEd25519, Proposal: p, IssuedAt: now,
	}
	if p.Kind == api.ProposalKindCancel {
		if p.TargetClientOrderId == nil {
			return noOneShot("the cancel names no targetClientOrderId")
		}
		if message, err = binance.BuildCancel(p.Symbol, *p.TargetClientOrderId, now); err != nil {
			return noOneShot("%v", err)
		}
		req.Purpose = api.SignRequestPurposeCancel
		req.VenueRequest = &api.VenueRequest{Method: http.MethodDelete, Url: binance.OrderPath}
	} else {
		rules, err := s.symbolRules(ctx, p.TradingAccountId, p.Symbol)
		if err != nil {
			return noOneShot("venue rules for %s: %v", p.Symbol, err)
		}
		clientOrderID := binance.ClientOrderID(s.Config.AgentID)
		if message, err = binance.BuildOrder(intentOf(*p), rules, clientOrderID, now); err != nil {
			return noOneShot("%v", err)
		}
		o, err := binance.DecodeOrder(message)
		if err == nil {
			err = binance.CheckOrder(o, intentOf(*p), o.Symbol, clientOrderID)
		}
		if err != nil {
			return fmt.Errorf("signer: the order built for the proposal fails its own check: %w", err)
		}
		req.Purpose, req.ClientOrderId = api.SignRequestPurposeOrder, &clientOrderID
		req.Order = &api.SignRequestOrder{
			Symbol: o.Symbol, Side: p.Side, Size: o.Quantity, OrderType: api.SignRequestOrderOrderType(p.OrderType), ReduceOnly: o.ReduceOnly,
		}
		if o.Price != "" {
			req.Order.Price = &o.Price
		}
		req.VenueRequest = &api.VenueRequest{Method: http.MethodPost, Url: binance.OrderPath}
	}
	req.Message = base64.StdEncoding.EncodeToString(message)
	req.ExpiresAt = now.Add(binance.RecvWindow * time.Millisecond)
	if s.Config.Hook != "" {
		if r := RunHook(ctx, s.Config.Hook, req, s.hookTimeout()); r != nil {
			return r
		}
	}
	ec, err := s.takeCommitment(ctx, vkID, now)
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(ec.Commitment)
	if err != nil {
		return noOneShot("the engine's commitment %s is not base64", ec.Id)
	}
	engineCommitment, err := frost.ParseCommitment(raw)
	if err != nil || engineCommitment.Party() != frost.EngineID {
		return noOneShot("the engine's commitment %s is not one of its FROST commitments", ec.Id)
	}
	share, err := s.share(vkID)
	if err != nil {
		return err
	}
	party, err := share.Party()
	if err != nil {
		return err
	}
	ac := party.Commit()
	as, err := party.Sign(message, engineCommitment, ac)
	if err != nil {
		return err
	}
	p.VenueMessage = &api.ProposalVenueMessage{
		VenueKeyId: vkID, Scheme: api.SigningSchemeFrostEd25519, Message: req.Message, EngineCommitmentId: ec.Id,
		Commitment: ptr(base64.StdEncoding.EncodeToString(ac.Bytes())), Share: base64.StdEncoding.EncodeToString(as.Bytes()),
	}
	return nil
}

// Propose sends an unsigned proposal one-shot when Prepare can build it and
// through the channel when it cannot. When the engine refuses the one-shot
// message because its commitment is unknown or spent (409, as after an
// engine restart) or because it does not run one-shot (501), the same
// proposal goes again without it, with a fresh nonce; a decision, or any other
// failure, is returned as it came.
func (s *Signer) Propose(ctx context.Context, p api.ProposalSubmit) (api.Decision, error) {
	plain := p
	if err := s.Prepare(ctx, &p); err != nil {
		var refused *Refusal
		if errors.As(err, &refused) {
			return api.Decision{}, err
		}
		s.Logger.Info("signer: proposal through the channel", "why", err.Error())
		return s.Client.Propose(ctx, plain)
	}
	d, err := s.Client.Propose(ctx, p)
	var e *Error
	if errors.As(err, &e) && (e.Status == http.StatusConflict || e.Status == http.StatusNotImplemented) {
		if e.Status == http.StatusConflict {
			s.forgetCommitments(p.VenueMessage.VenueKeyId)
		}
		s.Logger.Info("signer: one-shot refused, sending through the channel", "status", e.Status, "code", e.Code, "message", e.Message)
		nonce, nerr := proposal.NewNonce()
		if nerr != nil {
			return api.Decision{}, nerr
		}
		plain.Nonce = nonce
		return s.Client.Propose(ctx, plain)
	}
	return d, err
}

// venueKeyOn finds the config's venue key on a trading account.
func (s *Signer) venueKeyOn(tradingAccountID string) (string, VenueKey, bool) {
	for id, k := range s.Config.VenueKeys {
		if k.TradingAccountID == tradingAccountID {
			return id, k, true
		}
	}
	return "", VenueKey{}, false
}

// symbolRules returns a symbol's rules on a trading account, from the engine
// once per rulesTTL.
func (s *Signer) symbolRules(ctx context.Context, tradingAccountID, symbol string) (binance.Rules, error) {
	key := tradingAccountID + "/" + strings.ToUpper(symbol)
	st := s.oneShot()
	st.mu.Lock()
	c, ok := st.rules[key]
	st.mu.Unlock()
	if ok && s.now().Sub(c.fetched) < rulesTTL {
		return c.rules, nil
	}
	vr, err := s.Client.VenueRules(ctx, tradingAccountID, strings.ToUpper(symbol))
	if err != nil {
		return binance.Rules{}, err
	}
	for _, sr := range vr.Symbols {
		if !strings.EqualFold(sr.Symbol, symbol) {
			continue
		}
		r := binance.Rules{Tick: deref(sr.TickSize), Step: deref(sr.StepSize), MinQty: deref(sr.MinSize), MinNotional: deref(sr.MinNotional)}
		st.mu.Lock()
		st.rules[key] = cachedRules{rules: r, fetched: s.now()}
		st.mu.Unlock()
		return r, nil
	}
	return binance.Rules{}, fmt.Errorf("the engine lists no rules for %s on %s", symbol, tradingAccountID)
}

// takeCommitment pops an unused engine commitment for the venue key, taking
// a batch from the engine when none is left, and tops the cache up in the
// background when it runs low and the signer keeps a batch.
func (s *Signer) takeCommitment(ctx context.Context, venueKeyID string, now time.Time) (api.EngineCommitment, error) {
	st := s.oneShot()
	pop := func() (api.EngineCommitment, int, bool) {
		st.mu.Lock()
		defer st.mu.Unlock()
		list := st.commitments[venueKeyID]
		for len(list) > 0 {
			c := list[0]
			list = list[1:]
			if now.Add(commitmentMargin).Before(c.ExpiresAt) {
				st.commitments[venueKeyID] = list
				return c, len(list), true
			}
		}
		st.commitments[venueKeyID] = list
		return api.EngineCommitment{}, 0, false
	}
	c, left, ok := pop()
	if !ok {
		if err := s.fetchCommitments(ctx, venueKeyID); err != nil {
			return api.EngineCommitment{}, noOneShot("engine commitments for %s: %v", venueKeyID, err)
		}
		if c, left, ok = pop(); !ok {
			return api.EngineCommitment{}, noOneShot("the engine has no commitment to spare for %s", venueKeyID)
		}
	}
	if batch := s.CommitmentBatch; batch > 1 && left < batch/2 {
		s.refill(venueKeyID)
	}
	return c, nil
}

// fetchCommitments takes the signer's batch (one when it keeps none) from
// the engine. A batch from another engine instance than the cached ones
// means the engine restarted: what was cached is dropped.
func (s *Signer) fetchCommitments(ctx context.Context, venueKeyID string) error {
	count := max(s.CommitmentBatch, 1)
	b, err := s.Client.Commitments(ctx, venueKeyID, count)
	if err != nil {
		return err
	}
	st := s.oneShot()
	st.mu.Lock()
	if b.EngineInstance != st.instance {
		if st.instance != "" {
			clear(st.commitments)
		}
		st.instance = b.EngineInstance
	}
	st.commitments[venueKeyID] = append(st.commitments[venueKeyID], b.Items...)
	st.mu.Unlock()
	return nil
}

// engineSeen takes note of the engine instance a poll named. When it is not
// the one the cached commitments came from, the engine has restarted and
// forgotten them: they are dropped, and a signer that keeps a batch takes a
// fresh one for each venue key it held commitments for, before a proposal
// needs it.
func (s *Signer) engineSeen(instance string) {
	if instance == "" {
		return
	}
	st := s.oneShot()
	st.mu.Lock()
	if st.instance == instance || st.instance == "" {
		st.instance = instance
		st.mu.Unlock()
		return
	}
	keys := make([]string, 0, len(st.commitments))
	for id := range st.commitments {
		keys = append(keys, id)
	}
	clear(st.commitments)
	st.instance = instance
	st.mu.Unlock()
	s.Logger.Info("signer: the engine restarted; dropped its commitments", "instance", instance, "venueKeys", len(keys))
	if s.CommitmentBatch > 1 {
		for _, id := range keys {
			s.refill(id)
		}
	}
}

// refill tops up a venue key's commitments in the background, one refill at a time.
func (s *Signer) refill(venueKeyID string) {
	st := s.oneShot()
	st.mu.Lock()
	if st.refilling[venueKeyID] {
		st.mu.Unlock()
		return
	}
	st.refilling[venueKeyID] = true
	st.mu.Unlock()
	go func() {
		defer func() {
			st.mu.Lock()
			delete(st.refilling, venueKeyID)
			st.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), s.Client.timeout())
		defer cancel()
		if err := s.fetchCommitments(ctx, venueKeyID); err != nil {
			s.Logger.Warn("signer: refilling commitments", "venueKey", venueKeyID, "err", err)
		}
	}()
}

// forgetCommitments drops a venue key's cached commitments, which the engine
// no longer knows (after its restart).
func (s *Signer) forgetCommitments(venueKeyID string) {
	st := s.oneShot()
	st.mu.Lock()
	delete(st.commitments, venueKeyID)
	st.mu.Unlock()
}

func (s *Signer) oneShot() *oneShotState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.os == nil {
		s.os = &oneShotState{rules: map[string]cachedRules{}, commitments: map[string][]api.EngineCommitment{}, refilling: map[string]bool{}}
	}
	return s.os
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
