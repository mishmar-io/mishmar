package signer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
)

// routingEngine is the engine of contract 0.21.0 as two signers of one agent
// see it: every /agent call is checked against the key that signed it, and
// sign requests wait in a queue per key (SignRequest.keyId), handed only to
// that key's poll. hand puts a request in any key's queue, so a test can
// also play an engine that routes wrong.
type routingEngine struct {
	pubs    map[string]string // key id → public key, base64
	mu      sync.Mutex
	queue   map[string][]api.SignRequest
	answers chan routed
	// gone are request ids whose answer the engine no longer takes (404):
	// the attempt went unanswered as far as the engine knows.
	gone map[string]bool
}

type routed struct {
	id, by string
	answer api.SignerAnswer
}

func (e *routingEngine) hand(keyID string, reqs ...api.SignRequest) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.queue[keyID] = append(e.queue[keyID], reqs...)
}

func (e *routingEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	keyID := r.Header.Get(agentkey.HeaderKeyID)
	req := agentkey.Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/v1"), Query: r.URL.RawQuery, Body: body,
		KeyID: keyID, Nonce: r.Header.Get(agentkey.HeaderNonce), SignedAt: r.Header.Get(agentkey.HeaderSignedAt)}
	pub, known := e.pubs[keyID]
	if !known || !agentkey.VerifyRequest(pub, req, r.Header.Get(agentkey.HeaderSignature)) {
		writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Unknown key"})
		return
	}
	switch path := req.Path; {
	case path == "/agent/signer/poll":
		e.mu.Lock()
		out := e.queue[keyID]
		e.queue[keyID] = nil
		e.mu.Unlock()
		if out == nil {
			time.Sleep(10 * time.Millisecond)
			out = []api.SignRequest{}
		}
		writeJSON(w, http.StatusOK, out)
	case strings.HasPrefix(path, "/agent/signer/requests/") && strings.HasSuffix(path, "/answer"):
		var a api.SignerAnswer
		_ = json.Unmarshal(body, &a)
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/agent/signer/requests/"), "/answer")
		e.mu.Lock()
		gone := e.gone[id]
		e.mu.Unlock()
		e.answers <- routed{id: id, by: keyID, answer: a}
		if gone {
			writeJSON(w, http.StatusNotFound, api.Error{Code: "not_found", Message: "No such open sign request"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusNotFound, api.Error{Code: "not_found", Message: "no"})
	}
}

// Two signers of one agent, each with its own key (a bot's issued key and a
// computer's registered one, say), poll the same engine. A request names the
// key whose signer must answer: that signer signs it, and a signer handed a
// request for another key refuses it before anything else is checked, so an
// engine that routes wrong gets a refusal, never a signature from the wrong
// machine.
func TestSignRequestsGoToTheKeyTheyName(t *testing.T) {
	f := newFixture(t)
	share := f.policy.VenueKeys["vk_1"]
	eng := &routingEngine{
		pubs:  map[string]string{"key_a": f.key.PublicKeyB64(), "key_b": f.other.PublicKeyB64()},
		queue: map[string][]api.SignRequest{}, answers: make(chan routed, 8),
	}
	srv := httptest.NewServer(eng)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	for keyID, seed := range map[string][]byte{"key_a": testSeed(1), "key_b": testSeed(2)} {
		s, err := New(&Config{Engine: srv.URL + "/v1", KeyID: keyID, Secret: agentkey.Secret("msk_abcdef", seed), VenueKeys: map[string]VenueKey{"vk_1": share}}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.PollWait = time.Second
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Run(ctx)
		}()
	}
	live := func(keyID string) func(p *api.ProposalSubmit) {
		return func(p *api.ProposalSubmit) {
			p.KeyId, p.ProposedAt, p.ExpiresAt = keyID, time.Now().UTC(), time.Now().Add(time.Minute).UTC()
		}
	}
	request := func(id, keyID string, by *agentkey.Key) (api.SignRequest, engineRound, string) {
		p := f.proposal(t, by, live(keyID))
		msg := binanceOrder(p, "msh-ag1-abc123", nil)
		round, commit := f.engineRound(t)
		r := orderRequest(p, msg)
		r.Id, r.SessionId, r.Engine, r.KeyId = id, id, commit, ptr(keyID)
		r.IssuedAt, r.ExpiresAt = time.Now(), time.Now().Add(10*time.Second)
		return r, round, msg
	}
	// B's order, routed to B: signed by B.
	forB, roundB, msgB := request("sr_b", "key_b", f.other)
	eng.hand("key_b", forB)
	// A's order, routed to A: signed by A.
	forA, roundA, msgA := request("sr_a", "key_a", f.key)
	eng.hand("key_a", forA)
	// B's order handed to A by an engine that routes wrong: A refuses it.
	misrouted, _, _ := request("sr_mis", "key_b", f.other)
	eng.hand("key_a", misrouted)

	got := map[string]routed{}
	timeout := time.After(15 * time.Second)
	for len(got) < 3 {
		select {
		case a := <-eng.answers:
			got[a.id] = a
		case <-timeout:
			t.Fatalf("answers in time: %v", got)
		}
	}
	if got["sr_b"].by != "key_b" || got["sr_a"].by != "key_a" || got["sr_mis"].by != "key_a" {
		t.Fatalf("answered by %s, %s, %s", got["sr_b"].by, got["sr_a"].by, got["sr_mis"].by)
	}
	f.finish(t, "B's order", got["sr_b"].answer, roundB, []byte(msgB))
	f.finish(t, "A's order", got["sr_a"].answer, roundA, []byte(msgA))
	a := got["sr_mis"].answer
	if a.Kind != api.SignerAnswerKindRefusal || a.Refusal == nil || !strings.HasPrefix(a.Refusal.Reason, RefuseOtherKey+": ") || !strings.Contains(a.Refusal.Reason, "key_b") {
		t.Fatalf("the misrouted request was answered %+v, want a refusal %s", a, RefuseOtherKey)
	}
}

// With a DrainTimeout, an answer in flight when Run's context ends still
// reaches the engine (the background signer's clean stop); without one it
// ends with the context, as before.
func TestDrainTimeoutFinishesTheAnswerInFlight(t *testing.T) {
	for _, drain := range []time.Duration{0, 5 * time.Second} {
		f := newFixture(t)
		eng := &routingEngine{pubs: map[string]string{"key_a": f.key.PublicKeyB64()}, queue: map[string][]api.SignRequest{}, answers: make(chan routed, 8)}
		srv := httptest.NewServer(eng)
		s, err := New(&Config{Engine: srv.URL + "/v1", KeyID: "key_a", Secret: agentkey.Secret("msk_abcdef", testSeed(1)), VenueKeys: map[string]VenueKey{"vk_1": f.policy.VenueKeys["vk_1"]},
			Hook: "sleep 0.4"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.PollWait, s.DrainTimeout = time.Second, drain
		p := f.proposal(t, f.key, func(p *api.ProposalSubmit) {
			p.ProposedAt, p.ExpiresAt = time.Now().UTC(), time.Now().Add(time.Minute).UTC()
		})
		_, commit := f.engineRound(t)
		r := orderRequest(p, binanceOrder(p, "msh-ag1-abc123", nil))
		r.Engine, r.IssuedAt, r.ExpiresAt = commit, time.Now(), time.Now().Add(10*time.Second)
		eng.hand("key_a", r)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()
		time.Sleep(150 * time.Millisecond) // handed out, the hook running
		cancel()
		<-done
		srv.Close()
		select {
		case a := <-eng.answers:
			if drain == 0 {
				t.Fatalf("without a drain the answer still went: %+v", a)
			}
			if a.answer.Kind != api.SignerAnswerKindShare {
				t.Fatalf("drained answer %+v", a.answer)
			}
		default:
			if drain > 0 {
				t.Fatal("with a drain the answer in flight was lost")
			}
		}
	}
}

// An order a person approved that waited for the signer arrives with its
// original proposal, proposed long ago and not yet expired, and a venue
// message built now: it is signed. When the engine took that answer, a
// second request for the same client order id is refused while the first
// message can still reach the venue (Binance: its timestamp plus
// recvWindow), and signed once that has passed. When the first answer did
// not reach the engine (the attempt went unanswered, 404), the retry under
// a new request id is signed.
func TestAWaitedOrderIsSignedOnceAtATime(t *testing.T) {
	f := newFixture(t)
	eng := &routingEngine{pubs: map[string]string{"key_a": f.key.PublicKeyB64()}, queue: map[string][]api.SignRequest{}, answers: make(chan routed, 8), gone: map[string]bool{}}
	srv := httptest.NewServer(eng)
	defer srv.Close()
	s, err := New(&Config{Engine: srv.URL + "/v1", KeyID: "key_a", Secret: agentkey.Secret("msk_abcdef", testSeed(1)), VenueKeys: map[string]VenueKey{"vk_1": f.policy.VenueKeys["vk_1"]}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.PollWait = time.Second
	clock := time.Now().Truncate(time.Millisecond) // a Binance timestamp's precision
	var clockMu sync.Mutex
	s.Now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()

	// Proposed twenty minutes ago, approved now, alive for ten more.
	waited := f.proposal(t, f.key, func(p *api.ProposalSubmit) {
		p.ProposedAt, p.ExpiresAt = clock.Add(-20*time.Minute).UTC(), clock.Add(10*time.Minute).UTC()
	})
	attempt := func(id, cid string, at time.Time) (api.SignRequest, engineRound, string) {
		msg := binanceOrder(waited, cid, map[string]string{"timestamp": strconv.FormatInt(at.UnixMilli(), 10), "recvWindow": "10000"})
		round, commit := f.engineRound(t)
		r := orderRequest(waited, msg)
		r.Id, r.SessionId, r.Engine, r.ClientOrderId, r.KeyId = id, id, commit, ptr(cid), ptr("key_a")
		r.IssuedAt, r.ExpiresAt = clock, clock.Add(10*time.Second)
		return r, round, msg
	}
	answer := func() routed {
		select {
		case a := <-eng.answers:
			return a
		case <-time.After(10 * time.Second):
			t.Fatal("no answer")
		}
		return routed{}
	}
	// The first send: signed, and the engine takes it.
	r1, round1, msg1 := attempt("sr_1", "msh-a-1", clock)
	eng.hand("key_a", r1)
	f.finish(t, "the waited order", answer().answer, round1, []byte(msg1))
	// A second request for the same order while the first message is live: refused.
	r2, _, _ := attempt("sr_2", "msh-a-1", clock.Add(time.Second))
	eng.hand("key_a", r2)
	if a := answer().answer; a.Kind != api.SignerAnswerKindRefusal || !strings.HasPrefix(a.Refusal.Reason, RefuseOrderSigned+": ") {
		t.Fatalf("a second send while the first can reach the venue: %+v", a)
	}
	// Once the first message's window has closed at the venue (its
	// recvWindow, plus the clock difference allowed), a new one is signed.
	clockMu.Lock()
	clock = clock.Add(10*time.Second + BinanceClockTolerance)
	clockMu.Unlock()
	r2b, _, _ := attempt("sr_2b", "msh-a-1", clock)
	eng.hand("key_a", r2b)
	if a := answer().answer; a.Kind != api.SignerAnswerKindRefusal || !strings.HasPrefix(a.Refusal.Reason, RefuseOrderSigned+": ") {
		t.Fatalf("at the very end of the first message's life: %+v", a)
	}
	clockMu.Lock()
	clock = clock.Add(time.Millisecond)
	clockMu.Unlock()
	r3, round3, msg3 := attempt("sr_3", "msh-a-1", clock)
	eng.hand("key_a", r3)
	f.finish(t, "after the window", answer().answer, round3, []byte(msg3))

	// An attempt the engine no longer takes (unanswered for it): the retry,
	// a new request id for the same order, is signed.
	r4, _, _ := attempt("sr_4", "msh-a-2", clock)
	eng.mu.Lock()
	eng.gone["sr_4"] = true
	eng.mu.Unlock()
	eng.hand("key_a", r4)
	if a := answer().answer; a.Kind != api.SignerAnswerKindShare {
		t.Fatalf("the unanswered attempt: %+v", a)
	}
	r5, round5, msg5 := attempt("sr_5", "msh-a-2", clock.Add(time.Second))
	eng.hand("key_a", r5)
	f.finish(t, "the retry", answer().answer, round5, []byte(msg5))
	// The same request id again stays a replay.
	if a := s.Handle(context.Background(), r5); a.Kind != api.SignerAnswerKindRefusal || !strings.HasPrefix(a.Refusal.Reason, RefuseReplay+": ") {
		t.Fatalf("a request answered twice: %+v", a)
	}
	// Past the proposal's deadline nothing is signed for it.
	clockMu.Lock()
	clock = clock.Add(11 * time.Minute)
	clockMu.Unlock()
	r6, _, _ := attempt("sr_6", "msh-a-3", clock)
	if a := s.Handle(context.Background(), r6); a.Kind != api.SignerAnswerKindRefusal || !strings.HasPrefix(a.Refusal.Reason, RefuseProposalExpired+": ") {
		t.Fatalf("after the deadline: %+v", a)
	}
}
