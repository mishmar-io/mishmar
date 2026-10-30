package signer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/proposal"
	"github.com/mishmar-io/mishmar/venues/binance"
)

// oneShotEngine is the engine's side of one-shot signing as a signer sees
// it: venue rules, a pool of its own FROST commitments, and a proposal
// endpoint that spends the commitment named in the message, adds its half
// and keeps the aggregated signature, as the engine would before sending it
// to Binance.
type oneShotEngine struct {
	t        *testing.T
	pub      string
	share    *frost.Share
	mu       sync.Mutex
	pool     map[string]*poolEntry
	next     int
	issued   int
	sigs     []oneShotSig
	plain    int
	conflict bool // answer the next one-shot proposal 409, as after a restart
	refused  int  // one-shot proposals answered 409
	instance string
}

// restart is the engine starting again: a new instance that knows none of
// the commitments it issued before.
func (e *oneShotEngine) restart(instance string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.pool)
	e.instance = instance
}

func (e *oneShotEngine) counts() (issued, plain, refused int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.issued, e.plain, e.refused
}

type poolEntry struct {
	party *frost.Party
	c     frost.Commitment
}

type oneShotSig struct {
	message   []byte
	signature []byte
	clientID  string
}

func (e *oneShotEngine) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agent/signer/poll", func(w http.ResponseWriter, r *http.Request) {
		if !e.authed(r) {
			writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Invalid signature"})
			return
		}
		time.Sleep(5 * time.Millisecond)
		e.mu.Lock()
		w.Header().Set(HeaderEngineInstance, e.instance)
		e.mu.Unlock()
		writeJSON(w, http.StatusOK, []api.SignRequest{})
	})
	mux.HandleFunc("GET /v1/agent/trading-accounts/{id}/venue-rules", func(w http.ResponseWriter, r *http.Request) {
		if !e.authed(r) {
			writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Invalid signature"})
			return
		}
		tick, step, min := "0.10", "0.001", "0.001"
		writeJSON(w, http.StatusOK, api.VenueRules{TradingAccountId: r.PathValue("id"), Venue: api.VenueBinance, AsOf: now,
			Symbols: []api.SymbolRules{{Symbol: "BTCUSDT", TickSize: &tick, StepSize: &step, MinSize: &min}}})
	})
	mux.HandleFunc("POST /v1/agent/signer/commitments", func(w http.ResponseWriter, r *http.Request) {
		if !e.authed(r) {
			writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Invalid signature"})
			return
		}
		var in api.CommitmentRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		n := 8
		if in.Count != nil {
			n = *in.Count
		}
		out := api.CommitmentBatch{VenueKeyId: in.VenueKeyId, Scheme: api.SigningSchemeFrostEd25519, BatchSize: 8}
		e.mu.Lock()
		out.EngineInstance = e.instance
		for range n {
			party, err := e.share.Party()
			if err != nil {
				e.t.Error(err)
			}
			c := party.Commit()
			e.next++
			e.issued++
			id := "cm_" + strconv.Itoa(e.next)
			e.pool[id] = &poolEntry{party: party, c: c}
			out.Items = append(out.Items, api.EngineCommitment{Id: id, Commitment: ptr(base64.StdEncoding.EncodeToString(c.Bytes())), ExpiresAt: now.Add(24 * time.Hour)})
		}
		out.Outstanding = len(e.pool)
		e.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /v1/proposals", func(w http.ResponseWriter, r *http.Request) {
		var in api.ProposalSubmit
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, api.Error{Code: "bad_request", Message: err.Error()})
			return
		}
		if !proposal.Verify(e.pub, submitOf(in)) {
			writeJSON(w, http.StatusUnauthorized, api.Error{Code: "unauthorized", Message: "Invalid signature"})
			return
		}
		m := in.VenueMessage
		if m == nil {
			e.mu.Lock()
			e.plain++
			e.mu.Unlock()
			writeJSON(w, http.StatusOK, api.Decision{Id: "dec_plain", Verdict: api.VerdictAllow})
			return
		}
		e.mu.Lock()
		entry, ok := e.pool[m.EngineCommitmentId]
		delete(e.pool, m.EngineCommitmentId)
		conflict := e.conflict
		e.conflict = false
		e.mu.Unlock()
		if !ok || conflict {
			e.mu.Lock()
			e.refused++
			e.mu.Unlock()
			writeJSON(w, http.StatusConflict, api.Error{Code: "conflict", Message: "Commitment unknown or spent"})
			return
		}
		message, _ := base64.StdEncoding.DecodeString(m.Message)
		acRaw, _ := base64.StdEncoding.DecodeString(*m.Commitment)
		asRaw, _ := base64.StdEncoding.DecodeString(m.Share)
		ac, err := frost.ParseCommitment(acRaw)
		if err != nil {
			e.t.Fatal(err)
		}
		as, err := frost.ParseSignatureShare(asRaw)
		if err != nil {
			e.t.Fatal(err)
		}
		if err := e.share.VerifyShare(message, as, entry.c, ac); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: err.Error()})
			return
		}
		es, err := entry.party.Sign(message, entry.c, ac)
		if err != nil {
			e.t.Fatal(err)
		}
		sig, err := e.share.Aggregate(message, []frost.Commitment{entry.c, ac}, []frost.SignatureShare{as, es})
		if err != nil {
			e.t.Fatal(err)
		}
		id := ""
		if o, err := binance.DecodeOrder(message); err == nil {
			id = o.ClientOrderID
		}
		e.mu.Lock()
		e.sigs = append(e.sigs, oneShotSig{message: message, signature: sig, clientID: id})
		e.mu.Unlock()
		writeJSON(w, http.StatusOK, api.Decision{Id: "dec_one", Verdict: api.VerdictAllow})
	})
	return mux
}

func (e *oneShotEngine) authed(r *http.Request) bool {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	req := agentkey.Request{
		Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/v1"), Query: r.URL.RawQuery, Body: body,
		KeyID: r.Header.Get(agentkey.HeaderKeyID), Nonce: r.Header.Get(agentkey.HeaderNonce), SignedAt: r.Header.Get(agentkey.HeaderSignedAt),
	}
	return agentkey.VerifyRequest(e.pub, req, r.Header.Get(agentkey.HeaderSignature))
}

// newOneShot is a signer with a Binance venue key on rt_1 and the engine that
// holds the other share.
func newOneShot(t *testing.T, mod func(*Config)) (*Signer, *oneShotEngine) {
	t.Helper()
	agentShare, engineShare, err := frost.Deal()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(agentShare)
	key := agentkey.FromSeed(testSeed(1))
	eng := &oneShotEngine{t: t, pub: key.PublicKeyB64(), share: engineShare, pool: map[string]*poolEntry{}, instance: "ein_first"}
	srv := httptest.NewServer(eng.handler())
	t.Cleanup(srv.Close)
	cfg := &Config{
		Engine: srv.URL + "/v1", KeyID: "key_a", Secret: agentkey.Secret("msk_abcdef", testSeed(1)), AgentID: "agt_ag1",
		VenueKeys: map[string]VenueKey{"vk_1": {TradingAccountID: "rt_1", Venue: "binance", Scheme: SchemeFrostEd25519, Share: raw}},
	}
	if mod != nil {
		mod(cfg)
	}
	s, err := New(cfg, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return now }
	return s, eng
}

func limitProposal() api.ProposalSubmit {
	price := "60000.55"
	return api.ProposalSubmit{
		KeyId: "key_a", TradingAccountId: "rt_1", Kind: api.ProposalKindOrder, Symbol: "BTCUSDT", Side: api.SideBuy, Size: "0.5009",
		Price: &price, OrderType: api.ProposalSubmitOrderTypeLimit, Nonce: "n1", ProposedAt: now, ExpiresAt: now.Add(time.Minute),
	}
}

// TestOneShotOrder: the signer builds the Binance order from the engine's
// rules, signs its half against a pre-issued commitment, and the engine's
// half completes an Ed25519 signature that verifies under the venue key, over
// exactly the bytes the signer built.
func TestOneShotOrder(t *testing.T) {
	s, eng := newOneShot(t, nil)
	d, err := s.Propose(context.Background(), limitProposal())
	if err != nil {
		t.Fatal(err)
	}
	if d.Id != "dec_one" || len(eng.sigs) != 1 || eng.plain != 0 {
		t.Fatalf("decision %s, one-shot %d, plain %d", d.Id, len(eng.sigs), eng.plain)
	}
	got := eng.sigs[0]
	if !ed25519.Verify(eng.share.PublicKey(), got.message, got.signature) {
		t.Fatal("the aggregated signature does not verify")
	}
	o, err := binance.DecodeOrder(got.message)
	if err != nil {
		t.Fatal(err)
	}
	if o.Quantity != "0.500" || o.Price != "60000.5" || !binance.IsClientOrderIDOf(o.ClientOrderID, "agt_ag1") {
		t.Fatalf("built %+v", o)
	}
	if eng.issued != 1 {
		t.Fatalf("a signer without a batch took %d commitments, want one per proposal", eng.issued)
	}
}

// TestOneShotCancel: a cancel goes one-shot too, as a DELETE by client order id.
func TestOneShotCancel(t *testing.T) {
	s, eng := newOneShot(t, nil)
	target := "msh-ag1-abcdef"
	p := api.ProposalSubmit{KeyId: "key_a", TradingAccountId: "rt_1", Kind: api.ProposalKindCancel, Symbol: "BTCUSDT",
		TargetClientOrderId: &target, Nonce: "n2", ProposedAt: now, ExpiresAt: now.Add(time.Minute)}
	if _, err := s.Propose(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if len(eng.sigs) != 1 {
		t.Fatalf("one-shot %d, plain %d", len(eng.sigs), eng.plain)
	}
	c, err := binance.DecodeCancel(eng.sigs[0].message)
	if err != nil || c.ClientOrderID != target || c.Symbol != "BTCUSDT" {
		t.Fatalf("%+v %v", c, err)
	}
}

// TestOneShotFallsBack: without an agent id, a venue key or a commitment the
// engine still knows, the proposal goes through the channel, signed afresh.
func TestOneShotFallsBack(t *testing.T) {
	s, eng := newOneShot(t, func(c *Config) { c.AgentID = "" })
	if _, err := s.Propose(context.Background(), limitProposal()); err != nil {
		t.Fatal(err)
	}
	if eng.plain != 1 || len(eng.sigs) != 0 {
		t.Fatalf("no agent id: one-shot %d, plain %d", len(eng.sigs), eng.plain)
	}

	s, eng = newOneShot(t, nil)
	p := limitProposal()
	p.TradingAccountId = "rt_other"
	if _, err := s.Propose(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if eng.plain != 1 {
		t.Fatalf("no venue key: plain %d", eng.plain)
	}

	s, eng = newOneShot(t, nil)
	s.CommitmentBatch = 4
	eng.conflict = true
	if _, err := s.Propose(context.Background(), limitProposal()); err != nil {
		t.Fatal(err)
	}
	if eng.plain != 1 || len(eng.sigs) != 0 {
		t.Fatalf("after a 409: one-shot %d, plain %d", len(eng.sigs), eng.plain)
	}
	if left := s.oneShot().commitments["vk_1"]; len(left) != 0 {
		t.Fatalf("%d commitments kept after the engine said it did not know them", len(left))
	}
}

// TestOneShotHookRefuses: the hook sees what one-shot would sign, and its
// refusal stops the proposal rather than sending it another way.
func TestOneShotHookRefuses(t *testing.T) {
	s, eng := newOneShot(t, func(c *Config) { c.Hook = "exit 3" })
	_, err := s.Propose(context.Background(), limitProposal())
	if r, ok := err.(*Refusal); !ok || r.Code != RefuseHook {
		t.Fatalf("%v", err)
	}
	if eng.plain != 0 || len(eng.sigs) != 0 {
		t.Fatalf("sent anyway: one-shot %d, plain %d", len(eng.sigs), eng.plain)
	}
}

// TestOneShotKeepsABatch: a long-running signer takes a batch once and
// signs several proposals from it.
func TestOneShotKeepsABatch(t *testing.T) {
	s, eng := newOneShot(t, nil)
	s.CommitmentBatch = 8
	for i := range 3 {
		p := limitProposal()
		p.Nonce = "n" + strconv.Itoa(i)
		if _, err := s.Propose(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if len(eng.sigs) != 3 || eng.issued != 8 {
		t.Fatalf("one-shot %d, commitments issued %d", len(eng.sigs), eng.issued)
	}
}

// TestOneShotEngineRestart: the poll names a new engine instance after a
// restart, and a signer that keeps a batch drops the commitments the engine
// forgot and takes fresh ones before its next proposal, which then goes
// one-shot without a refusal.
func TestOneShotEngineRestart(t *testing.T) {
	s, eng := newOneShot(t, nil)
	s.CommitmentBatch = 8
	s.PollWait = time.Second
	p := limitProposal()
	if _, err := s.Propose(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx) }()
	defer func() { cancel(); <-done }()

	eng.restart("ein_second")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if issued, _, _ := eng.counts(); issued == 16 {
			break
		}
		if time.Now().After(deadline) {
			issued, _, _ := eng.counts()
			t.Fatalf("issued %d commitments after the restart, want a fresh batch of 8", issued-8)
		}
		time.Sleep(5 * time.Millisecond)
	}
	p.Nonce = "n2"
	if _, err := s.Propose(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, plain, refused := eng.counts(); plain != 0 || refused != 0 || len(eng.sigs) != 2 {
		t.Fatalf("after the restart: %d through the channel, %d refused, %d one-shot; want 0, 0, 2", plain, refused, len(eng.sigs))
	}
}
