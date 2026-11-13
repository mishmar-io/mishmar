package signer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/ecdsa2p"
	"github.com/mishmar-io/mishmar/frost"
)

// Defaults of the loop.
const (
	DefaultPollWait   = 25 * time.Second
	DefaultWorkers    = 8
	minBackoff        = time.Second
	maxBackoff        = 30 * time.Second
	answeredRemembers = 4096
)

// Signer holds the agent's shares, polls the engine and answers its sign
// requests. Run is the loop (for a read agent, only a slow GET /agent/me:
// permission.go); Handle answers one request without it.
type Signer struct {
	Config *Config
	Client *Client
	Logger *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// PollWait is how long each poll may be held open (DefaultPollWait).
	PollWait time.Duration
	// Workers bounds the requests handled at once (DefaultWorkers).
	Workers int
	// HookTimeout bounds the config's hook (DefaultHookTimeout).
	HookTimeout time.Duration
	// CommitmentBatch is how many engine commitments the signer keeps for
	// one-shot signing per venue key, refilled when half are spent; 0 or 1
	// takes one per proposal, which suits a process that sends one and exits.
	CommitmentBatch int
	// ConfigPath is the file the config came from. Set, Run keeps the
	// signer's trading keys in line with the engine's (SyncKeys: keys the
	// console added are generated here and saved there, keys another command
	// wrote into the file are picked up, revoked ones dropped) every
	// KeySyncInterval (DefaultKeySyncInterval). Empty, Run syncs nothing.
	ConfigPath      string
	KeySyncInterval time.Duration
	// PermissionInterval is how often Run asks GET /agent/me again whether
	// the agent reads or trades (DefaultPermissionInterval).
	PermissionInterval time.Duration
	// DrainTimeout, when set, lets the answers in flight when Run's context
	// ends finish for up to this long (the background signer's clean stop:
	// it stops polling, then finishes what it holds). Zero ends them with
	// the context, as before.
	DrainTimeout time.Duration

	perm      *permissionState
	signed    orderMemory
	key       *agentkey.Key
	pub       string
	mu        sync.Mutex
	shares    map[string]*frost.Share
	ecdsaKeys map[string]*ecdsa2p.Key
	answered  *idSet
	os        *oneShotState
	warned    map[string]bool
	fileSeen  fileStamp
}

// New makes a signer for the config. A nil client is made from the config;
// a nil logger discards.
func New(cfg *Config, client *Client, log *slog.Logger) (*Signer, error) {
	if cfg == nil {
		return nil, errors.New("signer: no config")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("signer: config: %w", err)
	}
	key, err := cfg.Key()
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = NewClient(cfg.Engine, cfg.KeyID, key)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Signer{
		Config: cfg, Client: client, Logger: log,
		key: key, pub: key.PublicKeyB64(), shares: map[string]*frost.Share{}, answered: newIDSet(answeredRemembers),
	}, nil
}

// Policy is what the signer checks a request against.
func (s *Signer) Policy() Policy {
	return Policy{KeyID: s.Config.KeyID, PublicKey: s.pub, VenueKeys: s.venueKeys(), Now: s.now, Answered: s.answered.has, Rules: s.cachedHLRules}
}

// Handle answers one request: the checks, then the hook, then the agent's
// half: the FROST round with its share, or for two-party ECDSA its last
// message from a presigned slot. A request is answered once in a session;
// the same id again is refused as a replay.
func (s *Signer) Handle(ctx context.Context, req api.SignRequest) api.SignerAnswer {
	if req.Venue == api.VenueHyperliquid && req.Proposal != nil {
		// The checks need the coin's rules; fetched here, read in Check.
		if _, err := s.hlRules(ctx, req.ConnectedAccountId, req.Proposal.Symbol); err != nil {
			s.Logger.Warn("signer: venue rules", "connectedAccount", req.ConnectedAccountId, "symbol", req.Proposal.Symbol, "err", err)
		}
	}
	message, refusal := s.Policy().Check(req)
	if refusal == nil {
		refusal = s.signedAlready(req)
	}
	if refusal == nil && s.Config.Hook != "" {
		refusal = RunHook(ctx, s.Config.Hook, req, s.hookTimeout())
	}
	var answer api.SignerAnswer
	if refusal == nil {
		answer, refusal = s.sign(ctx, req, message)
	}
	s.answered.add(req.Id)
	if refusal != nil {
		return Refuse(refusal)
	}
	return answer
}

// Refuse is the answer that carries a refusal.
func Refuse(r *Refusal) api.SignerAnswer {
	return api.SignerAnswer{Kind: api.SignerAnswerKindRefusal, Refusal: &struct {
		Reason string `json:"reason"`
	}{Reason: r.Reason()}}
}

// sign makes the agent's half: for two-party ECDSA its last message from a
// presigned slot over the digest; for FROST, the round: parse the engine's
// commitment, commit, make the share of the signature over the message.
func (s *Signer) sign(ctx context.Context, req api.SignRequest, message []byte) (api.SignerAnswer, *Refusal) {
	if req.Scheme == api.SigningSchemeEcdsa2p {
		return s.signECDSA(ctx, req, message)
	}
	if req.Engine == nil || req.Engine.Commitment == nil || *req.Engine.Commitment == "" {
		return api.SignerAnswer{}, &Refusal{RefuseMalformed, "the request carries no engine commitment"}
	}
	raw, err := base64.StdEncoding.DecodeString(*req.Engine.Commitment)
	if err != nil {
		return api.SignerAnswer{}, &Refusal{RefuseMalformed, "the engine's commitment is not base64"}
	}
	ec, err := frost.ParseCommitment(raw)
	if err != nil {
		return api.SignerAnswer{}, &Refusal{RefuseMalformed, err.Error()}
	}
	if ec.Party() != frost.EngineID {
		return api.SignerAnswer{}, &Refusal{RefuseMalformed, fmt.Sprintf("the commitment is party %d's, not the engine's", ec.Party())}
	}
	share, err := s.share(req.VenueKeyId)
	if err != nil {
		return api.SignerAnswer{}, &Refusal{RefuseMalformed, err.Error()}
	}
	party, err := share.Party()
	if err != nil {
		return api.SignerAnswer{}, &Refusal{RefuseMalformed, err.Error()}
	}
	ac := party.Commit()
	as, err := party.Sign(message, ec, ac)
	if err != nil {
		return api.SignerAnswer{}, &Refusal{RefuseMalformed, err.Error()}
	}
	return api.SignerAnswer{
		Kind:       api.SignerAnswerKindShare,
		Commitment: ptr(base64.StdEncoding.EncodeToString(ac.Bytes())),
		Share:      ptr(base64.StdEncoding.EncodeToString(as.Bytes())),
	}, nil
}

// signECDSA spends a presigned slot on the digest: the slot is gone whatever
// the engine does with the answer.
func (s *Signer) signECDSA(ctx context.Context, req api.SignRequest, digest []byte) (api.SignerAnswer, *Refusal) {
	key, err := s.ecdsaKey(req.VenueKeyId)
	if err != nil {
		return api.SignerAnswer{}, &Refusal{RefuseMalformed, err.Error()}
	}
	slotID, presig, index, err := s.takeSlot(ctx, req.VenueKeyId, s.now())
	if err != nil {
		return api.SignerAnswer{}, &Refusal{RefuseNoSlot, err.Error()}
	}
	m4, err := key.Complete(presig, index, digest)
	if err != nil {
		return api.SignerAnswer{}, &Refusal{RefuseMalformed, err.Error()}
	}
	return api.SignerAnswer{
		Kind: api.SignerAnswerKindShare, Share: ptr(base64.StdEncoding.EncodeToString(m4)), EngineCommitmentId: &slotID,
	}, nil
}

// share parses the agent's FROST share of a venue key once and keeps it.
func (s *Signer) share(venueKeyID string) (*frost.Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.shares[venueKeyID]; ok {
		return sh, nil
	}
	k, ok := s.Config.VenueKeys[venueKeyID]
	if !ok {
		return nil, fmt.Errorf("no share of %s", venueKeyID)
	}
	sh, err := frost.ParseShare(k.Share)
	if err != nil {
		return nil, err
	}
	s.shares[venueKeyID] = sh
	return sh, nil
}

// runChannel is the signer channel of an agent that trades (Run starts it):
// it polls the engine until ctx ends and answers what it gets, each request
// in its own goroutine, at most Workers at once, and with ConfigPath keeps
// the trading keys in line with the engine's. A poll that fails is retried
// after a backoff that doubles from one second to thirty. The first poll
// after a start or a failure does not wait, so a new engine instance (a
// restart) is seen as soon as the engine answers.
func (s *Signer) runChannel(ctx context.Context) error {
	workers := s.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	wait := s.PollWait
	if wait <= 0 {
		wait = DefaultPollWait
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	defer wg.Wait()
	if s.ConfigPath != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.syncLoop(ctx)
		}()
	}
	backoff := minBackoff
	quick := true
	s.Logger.Info("signer: polling", "engine", s.Client.Engine, "keyId", s.Config.KeyID, "venueKeys", len(s.venueKeys()))
	for {
		w := wait
		if quick {
			w = 0
		}
		reqs, err := s.Client.Poll(ctx, w)
		if ctx.Err() != nil {
			return nil
		}
		quick = err != nil
		if err != nil {
			s.Logger.Warn("signer: poll failed", "err", err, "retryIn", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
		s.engineSeen(s.Client.EngineInstance())
		for _, req := range reqs {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return nil
			}
			wg.Add(1)
			go func(req api.SignRequest) {
				defer wg.Done()
				defer func() { <-sem }()
				actx, done := s.answerContext(ctx)
				defer done()
				s.serve(actx, req)
			}(req)
		}
	}
}

// serve answers one request and logs one line about it.
func (s *Signer) serve(ctx context.Context, req api.SignRequest) {
	answer := s.Handle(ctx, req)
	outcome := "signed"
	if answer.Kind == api.SignerAnswerKindRefusal && answer.Refusal != nil {
		outcome = "refused: " + answer.Refusal.Reason
	}
	attrs := []any{"id", req.Id, "purpose", req.Purpose, "venue", req.Venue, "venueKey", req.VenueKeyId, "attempt", req.Attempt, "outcome", outcome}
	if req.DecisionId != nil {
		attrs = append(attrs, "decision", *req.DecisionId)
	}
	if err := s.Client.Answer(ctx, req.Id, answer); err != nil {
		s.Logger.Warn("signer: request", append(attrs, "delivery", err.Error())...)
		return
	}
	if answer.Kind == api.SignerAnswerKindShare {
		s.rememberSigned(req)
	}
	s.Logger.Info("signer: request", attrs...)
}

// An order the engine sends late (a person approved it, and it waited for
// this signer) arrives as a request carrying the original signed proposal,
// however old its proposedAt, with a venue message built at that moment;
// the checks take it while the proposal has not expired. When an attempt
// goes unanswered the engine asks again under a new request id, for the
// same order and client order id, and that is signed too. What keeps two
// sends of one order from both being signed: once the engine has taken
// this signer's answer for an order (orderMemory), another request for the
// same client order id on the same connected account is refused
// (RefuseOrderSigned) for as long as the message signed first can reach the
// venue: on Binance until its timestamp plus recvWindow plus the clock
// difference allowed (binanceLiveUntil, the bound checkBinanceTime holds
// every message to), after which the venue refuses it; elsewhere
// (Hyperliquid's nonces stay usable for days) until the proposal's
// expiresAt, after which nothing is signed for it at all. An answer that did not reach the engine signed nothing it can send,
// so it is not remembered and the retry is signed.
type orderMemory struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func orderMemoryKey(req api.SignRequest) (string, bool) {
	if req.Purpose != api.SignRequestPurposeOrder || req.ClientOrderId == nil || *req.ClientOrderId == "" {
		return "", false
	}
	return req.ConnectedAccountId + "/" + *req.ClientOrderId, true
}

// liveUntil is until when the request's venue message can reach the venue.
func liveUntil(req api.SignRequest) time.Time {
	var until time.Time
	if req.Proposal != nil {
		until = req.Proposal.ExpiresAt
	}
	if req.Venue == api.VenueBinance {
		if raw, err := base64.StdEncoding.DecodeString(req.Message); err == nil {
			if ts, window, err := binanceTimes(req.Purpose, raw); err == nil {
				until = binanceLiveUntil(ts, window)
			}
		}
	}
	return until
}

// signedAlready refuses an order whose earlier message this signer signed
// and the engine took, while that message can still reach the venue.
func (s *Signer) signedAlready(req api.SignRequest) *Refusal {
	k, ok := orderMemoryKey(req)
	if !ok {
		return nil
	}
	s.signed.mu.Lock()
	until, seen := s.signed.until[k]
	s.signed.mu.Unlock()
	if !seen || s.now().After(until) {
		return nil
	}
	return &Refusal{RefuseOrderSigned, fmt.Sprintf("client order id %s was signed already and the engine took the answer; that message can reach the venue until %s, so no second one is signed before then", *req.ClientOrderId, until.UTC().Format(time.RFC3339Nano))}
}

// rememberSigned records an order whose answer the engine took.
func (s *Signer) rememberSigned(req api.SignRequest) {
	k, ok := orderMemoryKey(req)
	if !ok {
		return
	}
	now := s.now()
	s.signed.mu.Lock()
	defer s.signed.mu.Unlock()
	if s.signed.until == nil {
		s.signed.until = map[string]time.Time{}
	}
	for key, until := range s.signed.until {
		if now.After(until) {
			delete(s.signed.until, key)
		}
	}
	s.signed.until[k] = liveUntil(req)
}

// answerContext is the context one answer runs under: ctx, or with a
// DrainTimeout, one that outlives ctx by that long.
func (s *Signer) answerContext(ctx context.Context) (context.Context, func()) {
	if s.DrainTimeout <= 0 {
		return ctx, func() {}
	}
	actx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var timer *time.Timer
	var mu sync.Mutex
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		timer = time.AfterFunc(s.DrainTimeout, cancel)
		mu.Unlock()
	})
	return actx, func() {
		stop()
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		cancel()
	}
}

func (s *Signer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Signer) hookTimeout() time.Duration {
	if s.HookTimeout > 0 {
		return s.HookTimeout
	}
	return DefaultHookTimeout
}

func ptr[T any](v T) *T { return &v }

// idSet remembers the last n ids; the oldest are forgotten first.
type idSet struct {
	mu   sync.Mutex
	set  map[string]struct{}
	ring []string
	next int
	full bool
}

func newIDSet(n int) *idSet {
	if n <= 0 {
		n = 1
	}
	return &idSet{set: make(map[string]struct{}, n), ring: make([]string, n)}
}

func (s *idSet) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.set[id]; ok {
		return
	}
	if s.full {
		delete(s.set, s.ring[s.next])
	}
	s.ring[s.next] = id
	s.set[id] = struct{}{}
	s.next++
	if s.next == len(s.ring) {
		s.next, s.full = 0, true
	}
}

func (s *idSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.set[id]
	return ok
}
