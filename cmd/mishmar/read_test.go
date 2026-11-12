package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
)

// readEngine answers the seven reads of connected-account data, the ones a
// hosted account's collector answers while its engine sleeps, for the one
// key it knows, and records every request; any other path is 404.
type readEngine struct {
	mu     sync.Mutex
	seen   []string
	others []string
}

// collectorReads are the seven reads, for the one connected account the tests here name.
var collectorReads = map[string]bool{
	"GET /agent/me": true, "GET /agent/connected-accounts": true, "GET /agent/connected-accounts/ta_1": true,
	"GET /agent/connected-accounts/ta_1/balances": true, "GET /agent/connected-accounts/ta_1/positions": true,
	"GET /agent/connected-accounts/ta_1/transactions": true, "GET /agent/connected-accounts/ta_1/equity": true,
}

func newReadEngine(t *testing.T, keyID, publicKey string) (*readEngine, *httptest.Server) {
	t.Helper()
	e := &readEngine{}
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	str := func(s string) *string { return &s }
	yes, no := true, false
	buy, sell := api.Side("buy"), api.Side("sell")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		path := strings.TrimPrefix(r.URL.Path, "/v1")
		e.mu.Lock()
		e.seen = append(e.seen, r.Method+" "+path+"?"+r.URL.RawQuery)
		if !collectorReads[r.Method+" "+path] {
			// Counted before anything is checked: a proposal signs its body, not the headers.
			e.others = append(e.others, r.Method+" "+path)
		}
		e.mu.Unlock()
		req := agentkey.Request{
			Method: r.Method, Path: path, Query: r.URL.RawQuery, Body: body,
			KeyID: r.Header.Get(agentkey.HeaderKeyID), Nonce: r.Header.Get(agentkey.HeaderNonce), SignedAt: r.Header.Get(agentkey.HeaderSignedAt),
		}
		if req.KeyID != keyID || !agentkey.VerifyRequest(publicKey, req, r.Header.Get(agentkey.HeaderSignature)) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(api.Error{Code: "unauthorized", Message: "unknown key or bad signature"})
			return
		}
		account := api.ConnectedAccount{Id: "ta_1", Name: "Main", Venue: api.VenueHyperliquid, Kind: api.ConnectedAccountKindHyperliquid, Status: api.ConnectedAccountStatusActive,
			Sync: &[]api.SyncState{{Kind: "balances", Status: "current", AsOf: &at}, {Kind: "transactions", Status: "backfilling", Progress: str("0.4")}}}
		var out any
		switch {
		case r.Method != http.MethodGet:
		case path == "/agent/me":
			out = api.AgentIdentity{AgentId: "agt_reader", AgentName: "Reader", AccountId: "acc_main", KeyId: keyID, PortfolioId: "pf_main", Permission: api.AgentPermissionRead}
		case path == "/agent/connected-accounts":
			out = []api.ConnectedAccount{account}
		case path == "/agent/connected-accounts/ta_1":
			out = account
		case path == "/agent/connected-accounts/ta_1/balances":
			out = api.BalanceSnapshot{ConnectedAccountId: "ta_1", AsOf: at, Source: api.SnapshotSourceObserved, EquityUsd: str("2300.5"),
				Balances: []api.Balance{{Asset: "USDC", Wallet: "margin", Amount: "1500.5", UsdValue: str("1500.5")}, {Asset: "HYPE", Wallet: "spot", Amount: "20"}}}
		case path == "/agent/connected-accounts/ta_1/positions":
			out = api.AccountPositionSnapshot{ConnectedAccountId: "ta_1", AsOf: at, Source: api.SnapshotSourceObserved,
				Positions: []api.AccountPosition{{Symbol: "BTC", Side: "long", Size: "0.01", EntryPrice: "79500", MarkPrice: str("80000"), UnrealizedUsd: str("5")}}}
		case path == "/agent/connected-accounts/ta_1/transactions":
			out = api.AccountTransactionPage{NextCursor: str("c2"), Items: []api.AccountTransaction{
				{Id: "tx_3", ConnectedAccountId: "ta_1", Kind: "trade", At: at, Side: &buy, Size: str("0.01"), Symbol: str("BTC"), Price: str("79500"), FeeUsd: str("0.28"),
					Origin: &api.TransactionOrigin{Kind: api.TransactionOriginKindAgent, AgentName: str("Reader"), Own: &yes}},
				{Id: "tx_2", ConnectedAccountId: "ta_1", Kind: "trade", At: at.Add(-time.Hour), Side: &sell, Size: str("2"), Symbol: str("ETH"), Price: str("3100"),
					Origin: &api.TransactionOrigin{Kind: api.TransactionOriginKindAgent, AgentName: str("Mean reversion"), Own: &no}},
				{Id: "tx_1", ConnectedAccountId: "ta_1", Kind: "transfer_in", At: at.Add(-24 * time.Hour), Amount: str("1000"), Asset: str("USDC"), AmountUsd: str("1000")},
			}}
		case path == "/agent/connected-accounts/ta_1/equity":
			out = api.EquitySeries{ConnectedAccountId: "ta_1", Resolution: "hour", Points: []api.EquityPoint{
				{At: at.Add(-time.Hour), EquityUsd: "2290", Source: api.SnapshotSourceObserved}, {At: at, EquityUsd: "2300.5", Source: api.SnapshotSourceObserved}}}
		}
		if out == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(api.Error{Code: "not_found", Message: "This process only collects connected-account data; the engine serves the API"})
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return e, srv
}

func TestReadFromTheAPIKey(t *testing.T) {
	const keyID = "key_main_abcdefgh2345"
	secret, key := newAPIKey(t, "self", keyID)
	eng, srv := newReadEngine(t, keyID, key.PublicKeyB64())
	t.Setenv(signer.EnvAPIKey, secret)
	t.Setenv(signer.EnvEngine, srv.URL+"/v1/")
	t.Setenv(signer.EnvConfig, filepath.Join(t.TempDir(), "none.json"))

	run := func(args ...string) string {
		t.Helper()
		out, err := execute(context.Background(), t, strings.NewReader(""), 10*time.Second, append([]string{"read"}, args...)...)
		if err != nil {
			t.Fatalf("mishmar read %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"agent"}, []string{"agent agt_reader (Reader)", "account acc_main, portfolio pf_main, key " + keyID}},
		{[]string{"accounts"}, []string{"ta_1  Main  hyperliquid  active  read only", "balances current as of 2026-11-12T10:00:00Z; transactions backfilling (0.4 done)"}},
		{[]string{"account", "ta_1"}, []string{"ta_1  Main  hyperliquid  active  read only"}},
		{[]string{"balances", "ta_1"}, []string{"ta_1 at 2026-11-12T10:00:00Z (observed), equity 2300.5 USD", "margin  USDC  1500.5  1500.5 USD", "spot    HYPE  20      -"}},
		{[]string{"positions", "ta_1", "--at", "2026-11-01"}, []string{"long  BTC  0.01  entry 79500  mark 80000  unrealised 5 USD"}},
		{[]string{"transactions", "ta_1", "--from", "2026-11-01T02:00:00+02:00", "--kind", "trade", "--limit", "3"}, []string{
			"2026-11-12T10:00:00Z  trade        buy 0.01 BTC at 79500, fee 0.28 USD  this agent",
			"2026-11-12T09:00:00Z  trade        sell 2 ETH at 3100                   agent Mean reversion",
			"2026-11-11T10:00:00Z  transfer_in  1000 USDC (1000 USD)",
			"more: --cursor c2",
		}},
		{[]string{"equity", "ta_1", "--resolution", "hour"}, []string{"ta_1 per hour, 2 points", "2026-11-12T09:00:00Z  2290 USD", "2026-11-12T10:00:00Z  2300.5 USD"}},
	} {
		out := run(tc.args...)
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Fatalf("mishmar read %s: want %q in:\n%s", strings.Join(tc.args, " "), want, out)
			}
		}
	}
	// --json is the engine's answer as it came, origin and all.
	var page api.AccountTransactionPage
	if err := json.Unmarshal([]byte(run("transactions", "ta_1", "--json")), &page); err != nil || len(page.Items) != 3 || page.Items[0].Origin == nil || !*page.Items[0].Origin.Own {
		t.Fatalf("--json: %+v %v", page, err)
	}

	eng.mu.Lock()
	defer eng.mu.Unlock()
	if len(eng.others) > 0 {
		t.Fatalf("mishmar read called what the collector does not answer: %v", eng.others)
	}
	for _, want := range []string{
		"GET /agent/connected-accounts/ta_1/positions?at=2026-11-01T00%3A00%3A00Z",
		"GET /agent/connected-accounts/ta_1/transactions?from=2026-11-01T00%3A00%3A00Z&kind=trade&limit=3",
		"GET /agent/connected-accounts/ta_1/equity?resolution=hour",
	} {
		found := false
		for _, s := range eng.seen {
			found = found || s == want
		}
		if !found {
			t.Fatalf("no %s in %v", want, eng.seen)
		}
	}
}

func TestReadFromTheConfig(t *testing.T) {
	const keyID = "key_main_abcdefgh2345"
	secret, key := newAPIKey(t, "self", keyID)
	eng, srv := newReadEngine(t, keyID, key.PublicKeyB64())
	path := filepath.Join(t.TempDir(), "agent.json")
	cfg := &signer.Config{Engine: srv.URL + "/v1", KeyID: keyID, Secret: secret, AgentID: "agt_reader", VenueKeys: map[string]signer.VenueKey{}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv(signer.EnvAPIKey, "")
	out, err := execute(context.Background(), t, strings.NewReader(""), 10*time.Second, "read", "accounts", "--config", path)
	if err != nil || !strings.Contains(out, "ta_1  Main") {
		t.Fatalf("--config: %v\n%s", err, out)
	}
	// Without the flag, the default config.
	t.Setenv(signer.EnvConfig, path)
	if out, err := execute(context.Background(), t, strings.NewReader(""), 10*time.Second, "read", "agent"); err != nil || !strings.Contains(out, "agt_reader") {
		t.Fatalf("default config: %v\n%s", err, out)
	}
	if len(eng.others) > 0 {
		t.Fatalf("calls the collector does not answer: %v", eng.others)
	}
}

func TestReadSaysWhatIsMissing(t *testing.T) {
	t.Setenv(signer.EnvAPIKey, "")
	t.Setenv(signer.EnvConfig, filepath.Join(t.TempDir(), "none.json"))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"read", "accounts"}, signer.EnvAPIKey},
		{[]string{"read", "balances"}, "mishmar read accounts"},
		{[]string{"read", "equity", "ta_1", "--from", "yesterday"}, "--from \"yesterday\""},
		{[]string{"read", "equity", "ta_1", "--resolution", "week"}, "minute, hour or day"},
		{[]string{"read", "transactions", "ta_1", "--limit", "900"}, "--limit"},
	} {
		if _, err := execute(context.Background(), t, strings.NewReader(""), 5*time.Second, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("mishmar %s: %v, want %q", strings.Join(tc.args, " "), err, tc.want)
		}
	}
	// A key that names no key id cannot read without a config.
	t.Setenv(signer.EnvAPIKey, agentkey.Secret("msk_abc123", make([]byte, 32)))
	if _, err := execute(context.Background(), t, strings.NewReader(""), 5*time.Second, "read", "accounts"); err == nil || !strings.Contains(err.Error(), "before 0.19.0") {
		t.Fatalf("an old key: %v", err)
	}
}
