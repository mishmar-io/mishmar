package signer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
)

func localFixture(t *testing.T) (*Signer, *fakeEngine) {
	t.Helper()
	seed := bytes.Repeat([]byte{3}, 32)
	key := agentkey.FromSeed(seed)
	fake := newFakeEngine(key.PublicKeyB64(), "key_1")
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	cfg := &Config{Engine: srv.URL + "/v1", KeyID: "key_1", Secret: agentkey.Secret("msk_abc", seed), AgentID: "agt_1", VenueKeys: map[string]VenueKey{}}
	s, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

func localCall(t *testing.T, base, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestLocalAPIProposesAndReadsForTheSDK(t *testing.T) {
	s, fake := localFixture(t)
	local := httptest.NewServer(s.LocalHandler("tok"))
	defer local.Close()

	if status, _ := localCall(t, local.URL, "", http.MethodGet, "/health", nil); status != http.StatusUnauthorized {
		t.Fatalf("without the token: %d", status)
	}
	if status, _ := localCall(t, local.URL, "wrong", http.MethodGet, "/health", nil); status != http.StatusUnauthorized {
		t.Fatalf("with the wrong token: %d", status)
	}
	status, health := localCall(t, local.URL, "tok", http.MethodGet, "/health", nil)
	if status != http.StatusOK || health["agentId"] != "agt_1" || health["keyId"] != "key_1" {
		t.Fatalf("health: %d %v", status, health)
	}

	// An intent becomes a signed proposal the engine accepts; with no venue
	// key for the connected account it goes through the channel.
	status, d := localCall(t, local.URL, "tok", http.MethodPost, "/propose", map[string]any{
		"connectedAccountId": "ta_1", "symbol": "BTCUSDT", "side": "buy", "size": "0.001", "price": "80000",
	})
	if status != http.StatusOK || d["id"] != "dec_1" || d["verdict"] != "allow" || fake.proposals.Load() != 1 {
		t.Fatalf("propose: %d %v", status, d)
	}
	// A bad intent is refused here, before anything is signed.
	for _, bad := range []map[string]any{
		{"connectedAccountId": "ta_1", "symbol": "BTCUSDT", "side": "buy", "price": "1"},
		{"connectedAccountId": "ta_1", "symbol": "BTCUSDT", "side": "hold", "size": "1", "price": "1"},
		{"connectedAccountId": "ta_1", "symbol": "BTCUSDT", "side": "buy", "size": "1"},
		{"connectedAccountId": "ta_1", "symbol": "BTCUSDT", "side": "buy", "size": "1", "orderType": "market", "price": "1"},
		{"connectedAccountId": "ta_1", "symbol": "BTCUSDT", "kind": "cancel"},
		{"connectedAccountId": "ta_1", "symbol": "BTCUSDT", "side": "buy", "size": "1", "price": "1", "validForSeconds": 100000},
	} {
		status, e := localCall(t, local.URL, "tok", http.MethodPost, "/propose", bad)
		if status != http.StatusUnprocessableEntity || e["code"] != "validation" {
			t.Fatalf("bad intent %v: %d %v", bad, status, e)
		}
	}
	if fake.proposals.Load() != 1 {
		t.Fatal("a bad intent reached the engine")
	}
	// Reads pass through with the agent's signature.
	req, _ := http.NewRequest(http.MethodGet, local.URL+"/positions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var positions []api.Position
	if err := json.NewDecoder(res.Body).Decode(&positions); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("positions: %d %v", res.StatusCode, err)
	}
	res.Body.Close()
	// The engine's refusals come through as they are.
	status, e := localCall(t, local.URL, "tok", http.MethodGet, "/decisions?limit=x", nil)
	if status != http.StatusUnprocessableEntity || e["code"] != "validation" {
		t.Fatalf("bad limit: %d %v", status, e)
	}
}

func TestProposalOfAnIntent(t *testing.T) {
	s, _ := localFixture(t)
	price := "100"
	p, err := s.proposalOf(ProposeRequest{ConnectedAccountID: " ta_1 ", Symbol: "BTC", Side: "Sell", Size: "0.5", Price: &price, ReduceOnly: ptr(true), ValidForSeconds: 120})
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyId != "key_1" || p.ConnectedAccountId != "ta_1" || p.Kind != api.ProposalKindOrder || p.Side != api.Side("sell") || p.OrderType != api.ProposalSubmitOrderTypeLimit || p.Price == nil || *p.Price != "100" || p.ReduceOnly == nil || !*p.ReduceOnly || p.Nonce == "" || !p.ExpiresAt.Equal(p.ProposedAt.Add(2*60*1e9)) {
		t.Fatalf("order: %+v", p)
	}
	target := "msh-1-abc"
	c, err := s.proposalOf(ProposeRequest{ConnectedAccountID: "ta_1", Symbol: "BTC", Kind: "cancel", TargetClientOrderID: &target})
	if err != nil || c.Kind != api.ProposalKindCancel || c.TargetClientOrderId == nil || *c.TargetClientOrderId != target || c.ExpiresAt.Sub(c.ProposedAt) != 60*1e9 {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	var le *LocalError
	if _, err := s.proposalOf(ProposeRequest{ConnectedAccountID: "ta_1", Symbol: "BTC", Side: "buy", Size: "1", OrderType: "stop"}); !errors.As(err, &le) || !strings.Contains(le.Message, "orderType") {
		t.Fatalf("stop order: %v", err)
	}
}

func TestServeLocalRefusesUnsafeAddresses(t *testing.T) {
	s, _ := localFixture(t)
	ctx := context.Background()
	if err := s.ServeLocal(ctx, "0.0.0.0:0", "tok"); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("all interfaces: %v", err)
	}
	if err := s.ServeLocal(ctx, "127.0.0.1:0", ""); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("no token: %v", err)
	}
	long := "unix:" + filepath.Join(t.TempDir(), strings.Repeat("d", 120), "signer.sock")
	if err := s.ServeLocal(ctx, long, ""); err == nil || !strings.Contains(err.Error(), "choose a shorter one") {
		t.Fatalf("a socket path too long: %v", err)
	}
}
