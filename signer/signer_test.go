package signer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/proposal"
)

// fakeEngine is the engine's side of the channel as far as a signer can
// tell: it verifies agentAuth on every /agent call the way the engine's
// middleware does, hands pending requests out once, and collects answers.
type fakeEngine struct {
	pub, keyID string
	mu         sync.Mutex
	pending    []api.SignRequest
	open       map[string]bool
	nonces     map[string]bool
	answers    chan answered
	calls      atomic.Int32
	authFailed atomic.Int32
	proposals  atomic.Int32
}

type answered struct {
	id     string
	answer api.SignerAnswer
}

func newFakeEngine(pub, keyID string) *fakeEngine {
	return &fakeEngine{pub: pub, keyID: keyID, open: map[string]bool{}, nonces: map[string]bool{}, answers: make(chan answered, 16)}
}

func (f *fakeEngine) add(reqs ...api.SignRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, reqs...)
}

func (f *fakeEngine) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agent/signer/poll", f.auth(f.poll))
	mux.HandleFunc("POST /v1/agent/signer/requests/{id}/answer", f.auth(f.answer))
	mux.HandleFunc("GET /v1/agent/positions", f.auth(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, http.StatusOK, []api.Position{})
	}))
	mux.HandleFunc("POST /v1/proposals", f.propose)
	return mux
}

// auth is the engine's agentAuth check: the path without /v1, the raw query,
// the body's hash, a fresh signedAt, an unseen nonce.
func (f *fakeEngine) auth(next func(http.ResponseWriter, *http.Request, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		req := agentkey.Request{
			Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/v1"), Query: r.URL.RawQuery, Body: body,
			KeyID: r.Header.Get(agentkey.HeaderKeyID), Nonce: r.Header.Get(agentkey.HeaderNonce), SignedAt: r.Header.Get(agentkey.HeaderSignedAt),
		}
		signedAt, err := time.Parse(time.RFC3339Nano, req.SignedAt)
		f.mu.Lock()
		replay := f.nonces[req.Nonce]
		f.nonces[req.Nonce] = true
		f.mu.Unlock()
		if req.KeyID != f.keyID || err != nil || time.Since(signedAt).Abs() > time.Minute || replay ||
			!agentkey.VerifyRequest(f.pub, req, r.Header.Get(agentkey.HeaderSignature)) {
			f.authFailed.Add(1)
			writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Invalid signature"})
			return
		}
		next(w, r, body)
	}
}

func (f *fakeEngine) poll(w http.ResponseWriter, r *http.Request, body []byte) {
	var in api.SignerPoll
	if err := json.Unmarshal(body, &in); err != nil || in.WaitSeconds == nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: "waitSeconds"})
		return
	}
	f.mu.Lock()
	out := f.pending
	f.pending = nil
	for _, req := range out {
		f.open[req.Id] = true
	}
	f.mu.Unlock()
	if out == nil {
		time.Sleep(20 * time.Millisecond)
		out = []api.SignRequest{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeEngine) answer(w http.ResponseWriter, r *http.Request, body []byte) {
	id := r.PathValue("id")
	var a api.SignerAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: err.Error()})
		return
	}
	f.mu.Lock()
	ok := f.open[id]
	delete(f.open, id)
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Error{Code: "not_found", Message: "No such open sign request"})
		return
	}
	f.answers <- answered{id: id, answer: a}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeEngine) propose(w http.ResponseWriter, r *http.Request) {
	f.proposals.Add(1)
	var p api.ProposalSubmit
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: err.Error()})
		return
	}
	if p.KeyId != f.keyID || !proposal.Verify(f.pub, submitOf(p)) {
		writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Invalid signature"})
		return
	}
	writeJSON(w, http.StatusOK, api.Decision{Id: "dec_1", Verdict: api.VerdictAllow, TradingAccountId: p.TradingAccountId,
		Proposal: api.Proposal{Symbol: p.Symbol, Side: p.Side, Size: p.Size, Venue: api.VenueBinance, ClientOrderId: "msh-1"}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// engineRound is the engine's half of one FROST signature: the party and the
// commitment it sent with the request.
type engineRound struct {
	party      *frost.Party
	commitment frost.Commitment
}

func (f *fixture) engineRound(t *testing.T) (engineRound, *api.SignRequestEngine) {
	t.Helper()
	party, err := f.engineShare.Party()
	if err != nil {
		t.Fatal(err)
	}
	c := party.Commit()
	return engineRound{party, c}, &api.SignRequestEngine{Commitment: ptr(base64.StdEncoding.EncodeToString(c.Bytes()))}
}

// finish does what the engine does with the signer's answer: checks the
// share, adds its own, aggregates, and verifies the Ed25519 signature with
// the group's public key.
func (f *fixture) finish(t *testing.T, name string, a api.SignerAnswer, round engineRound, message []byte) {
	t.Helper()
	if a.Kind != api.SignerAnswerKindShare || a.Commitment == nil || a.Share == nil {
		t.Fatalf("%s: answered %+v, want a share", name, a)
	}
	acRaw, err := base64.StdEncoding.DecodeString(*a.Commitment)
	if err != nil {
		t.Fatal(err)
	}
	ac, err := frost.ParseCommitment(acRaw)
	if err != nil {
		t.Fatal(err)
	}
	asRaw, err := base64.StdEncoding.DecodeString(*a.Share)
	if err != nil {
		t.Fatal(err)
	}
	as, err := frost.ParseSignatureShare(asRaw)
	if err != nil {
		t.Fatal(err)
	}
	if ac.Party() != frost.AgentID || as.Party() != frost.AgentID {
		t.Fatalf("%s: the answer is party %d/%d's, want the agent's", name, ac.Party(), as.Party())
	}
	if err := f.engineShare.VerifyShare(message, as, round.commitment, ac); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	es, err := round.party.Sign(message, round.commitment, ac)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	sig, err := f.engineShare.Aggregate(message, []frost.Commitment{round.commitment, ac}, []frost.SignatureShare{as, es})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !ed25519.Verify(f.engineShare.PublicKey(), message, sig) {
		t.Fatalf("%s: the aggregated signature does not verify with crypto/ed25519", name)
	}
	if ed25519.Verify(f.engineShare.PublicKey(), append([]byte("x"), message...), sig) {
		t.Fatalf("%s: the signature verifies another message", name)
	}
}

func TestSignerEndToEnd(t *testing.T) {
	f := newFixture(t)
	cfg := &Config{Engine: "placeholder", KeyID: "key_a", Secret: agentkey.Secret("msk_abcdef", testSeed(1)), VenueKeys: map[string]VenueKey{"vk_1": f.policy.VenueKeys["vk_1"]}}
	fake := newFakeEngine(f.key.PublicKeyB64(), "key_a")
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	cfg.Engine = srv.URL + "/v1"
	s, err := New(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.PollWait = time.Second

	// Three requests: an order to sign, a verify read to sign, a foreign
	// proposal to refuse. Times are real: the checks use the clock.
	live := func(p *api.ProposalSubmit) {
		p.ProposedAt, p.ExpiresAt = time.Now().UTC(), time.Now().Add(time.Minute).UTC()
	}
	prop := f.proposal(t, f.key, live)
	orderMsg := binanceOrder(prop, "msh-ag1-abc123", nil)
	orderRound, orderEngine := f.engineRound(t)
	order := orderRequest(prop, orderMsg)
	order.Id, order.SessionId, order.Engine = "sr_order", "sr_order", orderEngine
	order.IssuedAt, order.ExpiresAt = time.Now(), time.Now().Add(10*time.Second)

	verifyRound, verifyEngine := f.engineRound(t)
	verify := verifyRequest(readMessage)
	verify.Id, verify.SessionId, verify.Engine = "sr_verify", "sr_verify", verifyEngine
	verify.IssuedAt, verify.ExpiresAt = time.Now(), time.Now().Add(10*time.Second)

	foreignProp := f.proposal(t, f.other, func(p *api.ProposalSubmit) { live(p); p.KeyId = "key_b" })
	_, foreignEngine := f.engineRound(t)
	foreign := orderRequest(foreignProp, binanceOrder(foreignProp, "msh-ag1-abc123", nil))
	foreign.Id, foreign.SessionId, foreign.Engine = "sr_foreign", "sr_foreign", foreignEngine
	foreign.IssuedAt, foreign.ExpiresAt = time.Now(), time.Now().Add(10*time.Second)
	fake.add(order, verify, foreign)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	answers := map[string]api.SignerAnswer{}
	timeout := time.After(15 * time.Second)
	for len(answers) < 3 {
		select {
		case a := <-fake.answers:
			answers[a.id] = a.answer
		case <-timeout:
			cancel()
			t.Fatalf("got %d answers in time: %v", len(answers), answers)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	f.finish(t, "order", answers["sr_order"], orderRound, []byte(orderMsg))
	f.finish(t, "verify", answers["sr_verify"], verifyRound, []byte(readMessage))
	if a := answers["sr_foreign"]; a.Kind != api.SignerAnswerKindRefusal || a.Refusal == nil || !strings.HasPrefix(a.Refusal.Reason, RefuseForeignProposal+": ") {
		t.Fatalf("the foreign proposal was answered %+v, want a refusal", a)
	}
	if fake.authFailed.Load() != 0 || fake.calls.Load() < 4 {
		t.Fatalf("%d calls, %d failed agentAuth", fake.calls.Load(), fake.authFailed.Load())
	}

	// The same request handed out again is a replay, not a second signature.
	order.Attempt = 2
	if a := s.Handle(context.Background(), order); a.Kind != api.SignerAnswerKindRefusal || !strings.HasPrefix(a.Refusal.Reason, RefuseReplay+": ") {
		t.Fatalf("a re-handout was answered %+v", a)
	}
	// A request whose engine commitment is missing is refused, not signed.
	fresh := order
	fresh.Id, fresh.Attempt, fresh.Engine = "sr_fresh", 1, nil
	if a := s.Handle(context.Background(), fresh); a.Kind != api.SignerAnswerKindRefusal || !strings.HasPrefix(a.Refusal.Reason, RefuseMalformed+": ") {
		t.Fatalf("a request without a commitment was answered %+v", a)
	}
	// The hook runs after the checks and can refuse.
	withHook := order
	withHook.Id = "sr_hooked"
	s.Config.Hook = "echo closed; exit 1"
	if a := s.Handle(context.Background(), withHook); a.Kind != api.SignerAnswerKindRefusal || a.Refusal.Reason != RefuseHook+": closed" {
		t.Fatalf("with a refusing hook: %+v", a)
	}
}

func TestClientAgainstFakeEngine(t *testing.T) {
	key := agentkey.FromSeed(testSeed(1))
	fake := newFakeEngine(key.PublicKeyB64(), "key_a")
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	c := NewClient(srv.URL+"/v1", "key_a", key)
	ctx := context.Background()

	if err := c.Answer(ctx, "sr_none", Refuse(&Refusal{RefuseExpired, "late"})); !errors.Is(err, ErrRequestGone) {
		t.Fatalf("answering a request that is not open: %v, want ErrRequestGone", err)
	}
	if reqs, err := c.Poll(ctx, 0); err != nil || len(reqs) != 0 {
		t.Fatalf("empty poll: %v, %v", reqs, err)
	}
	if pos, err := c.Positions(ctx); err != nil || len(pos) != 0 {
		t.Fatalf("positions: %v, %v", pos, err)
	}
	price := "60000"
	d, err := c.Propose(ctx, api.ProposalSubmit{
		TradingAccountId: "rt_1", Kind: api.ProposalKindOrder, Symbol: "BTCUSDT", Side: api.SideBuy, Size: "0.5", Price: &price,
		OrderType: api.ProposalSubmitOrderTypeLimit, Nonce: "n1", ProposedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil || d.Id != "dec_1" || d.Verdict != api.VerdictAllow {
		t.Fatalf("propose: %+v, %v", d, err)
	}
	// A wrong key is refused by the engine, and the error carries its code.
	wrong := NewClient(srv.URL+"/v1", "key_a", agentkey.FromSeed(testSeed(2)))
	_, err = wrong.Positions(ctx)
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusUnauthorized || e.Code != "unauthorized" {
		t.Fatalf("with the wrong key: %v", err)
	}
	if fake.authFailed.Load() != 1 {
		t.Fatalf("%d auth failures, want the one", fake.authFailed.Load())
	}
}

func TestIDSet(t *testing.T) {
	s := newIDSet(3)
	for _, id := range []string{"a", "b", "c"} {
		s.add(id)
	}
	s.add("a")
	if !s.has("a") || !s.has("b") || !s.has("c") {
		t.Fatal("ids lost")
	}
	s.add("d")
	if s.has("a") || !s.has("d") || !s.has("b") {
		t.Fatal("the oldest was not the one forgotten")
	}
}
