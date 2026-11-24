package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/venues/binance"
)

// oneStepEngine is the engine's side of an order the signer sends in one
// step, as "mishmar propose --config" meets it: the symbol's rules, FROST
// commitments from the engine's share, and a proposal endpoint that keeps
// what it received and allows it.
type oneStepEngine struct {
	t     *testing.T
	share *frost.Share
	mu    sync.Mutex
	next  int
	got   []api.ProposalSubmit
}

func (e *oneStepEngine) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/agent/connected-accounts/{id}/venue-rules", func(w http.ResponseWriter, r *http.Request) {
		tick, step, min := "0.10", "0.001", "0.001"
		writeOneStep(w, http.StatusOK, api.VenueRules{ConnectedAccountId: r.PathValue("id"), Venue: api.VenueBinance, AsOf: time.Now().UTC(),
			Symbols: []api.SymbolRules{{Symbol: "BTCUSDT", TickSize: &tick, StepSize: &step, MinSize: &min}}})
	})
	mux.HandleFunc("POST /v1/agent/signer/commitments", func(w http.ResponseWriter, r *http.Request) {
		var in api.CommitmentRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		n := 1
		if in.Count != nil {
			n = *in.Count
		}
		out := api.CommitmentBatch{VenueKeyId: in.VenueKeyId, Scheme: api.SigningSchemeFrostEd25519, BatchSize: n, EngineInstance: "ein_1"}
		e.mu.Lock()
		defer e.mu.Unlock()
		for range n {
			party, err := e.share.Party()
			if err != nil {
				e.t.Error(err)
				return
			}
			c := base64.StdEncoding.EncodeToString(party.Commit().Bytes())
			e.next++
			out.Items = append(out.Items, api.EngineCommitment{Id: "cm_" + strconv.Itoa(e.next), Commitment: &c, ExpiresAt: time.Now().Add(time.Hour)})
		}
		out.Outstanding = len(out.Items)
		writeOneStep(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /v1/proposals", func(w http.ResponseWriter, r *http.Request) {
		var in api.ProposalSubmit
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeOneStep(w, http.StatusBadRequest, api.Error{Code: "bad_request", Message: err.Error()})
			return
		}
		e.mu.Lock()
		e.got = append(e.got, in)
		e.mu.Unlock()
		writeOneStep(w, http.StatusOK, api.Decision{Id: "dec_one", Verdict: api.VerdictAllow,
			Proposal: api.Proposal{Kind: in.Kind, Venue: api.VenueBinance, Symbol: in.Symbol, Side: in.Side, Size: in.Size, NotionalUsd: "60"}})
	})
	return mux
}

func writeOneStep(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// "mishmar propose --config" sends a Binance limit order in one step (the
// venue message is built and half-signed here) and records it in the file
// beside the config, agent.signed.json for agent.json, the file "signer run"
// on that config keeps.
func TestProposeWithAConfigRecordsItsOneStepOrderBesideTheConfig(t *testing.T) {
	const keyID = "key_main_abcdefgh2345"
	secret, _ := newAPIKey(t, "self", keyID)
	agentShare, engineShare, err := frost.Deal()
	if err != nil {
		t.Fatal(err)
	}
	share, err := json.Marshal(agentShare)
	if err != nil {
		t.Fatal(err)
	}
	eng := &oneStepEngine{t: t, share: engineShare}
	srv := httptest.NewServer(eng.handler())
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	cfg := &signer.Config{Engine: srv.URL + "/v1", KeyID: keyID, Secret: secret, AgentID: "agt_ag1",
		VenueKeys: map[string]signer.VenueKey{"vk_1": {ConnectedAccountID: "ta_1", Venue: "binance", Scheme: signer.SchemeFrostEd25519, Share: share}}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	out, err := execute(context.Background(), t, strings.NewReader(""), 10*time.Second,
		"propose", "--config", path, "--connected-account", "ta_1", "--symbol", "BTCUSDT", "--size", "0.001", "--price", "60000")
	if err != nil {
		t.Fatalf("propose: %v\n%s", err, out)
	}

	eng.mu.Lock()
	got := eng.got
	eng.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("the engine received %d proposals, want 1\n%s", len(got), out)
	}
	p := got[0]
	if p.VenueMessage == nil {
		t.Fatalf("the proposal carried no venue message: it went through the channel, not in one step\n%s", out)
	}
	message, err := base64.StdEncoding.DecodeString(p.VenueMessage.Message)
	if err != nil {
		t.Fatal(err)
	}
	order, err := binance.DecodeOrder(message)
	if err != nil {
		t.Fatalf("the venue message is not a Binance order: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "agent.signed.json"))
	if err != nil {
		t.Fatalf("no record beside the config: %v", err)
	}
	var record struct {
		Version int `json:"version"`
		Orders  []struct {
			KeyID         string     `json:"keyId"`
			Nonce         string     `json:"nonce"`
			RequestID     string     `json:"requestId"`
			ClientOrderID string     `json:"clientOrderId"`
			SignedAt      time.Time  `json:"signedAt"`
			OneShotUntil  *time.Time `json:"oneShotUntil"`
		} `json:"orders"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("the record does not decode: %v\n%s", err, raw)
	}
	if len(record.Orders) != 1 {
		t.Fatalf("the record holds %d orders, want 1:\n%s", len(record.Orders), raw)
	}
	o := record.Orders[0]
	if o.KeyID != keyID {
		t.Errorf("keyId %q, want the config's %q", o.KeyID, keyID)
	}
	if o.Nonce != p.Nonce {
		t.Errorf("nonce %q, want the proposal's %q", o.Nonce, p.Nonce)
	}
	if o.ClientOrderID == "" || o.ClientOrderID != order.ClientOrderID {
		t.Errorf("clientOrderId %q, want the venue message's %q", o.ClientOrderID, order.ClientOrderID)
	}
	if o.RequestID != "" {
		t.Errorf("requestId %q, want none: no channel request was signed", o.RequestID)
	}
	if o.OneShotUntil == nil || !o.OneShotUntil.After(o.SignedAt) {
		t.Errorf("oneShotUntil %v, want a moment after signedAt %s", o.OneShotUntil, o.SignedAt)
	}
}
