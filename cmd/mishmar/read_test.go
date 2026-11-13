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
		// Both accounts of a Hyperliquid login; the one account of a Bybit
		// login (its name left out of the line for accountCount 1, whatever
		// the venue calls it); one of a sub-account's two accounts, not read
		// yet; two of a Binance login's three.
		hlMain := api.VenueConnectionRef{Id: "hl_main", Name: "Main", Kind: api.VenueConnectionKindHyperliquid, AccountCount: 2,
			VenueAccount: api.VenueAccount{Id: str("0x12ab000000000000000000000000000000009f3c"), Label: str("0x12ab…9f3c"), Role: api.VenueAccountRoleMain},
			Sync:         &[]api.SyncState{{Kind: "balances", Status: "current", AsOf: &at}, {Kind: "transactions", Status: "backfilling", Progress: str("0.4")}}}
		perps := api.ConnectedAccount{Id: "ta_1", VenueConnectionId: "hl_main", Connection: hlMain, Name: "Perps", Type: api.ConnectedAccountTypeDerivatives, VenueRef: "perps",
			Venue: api.VenueHyperliquid, Status: api.ConnectedAccountStatusActive, EquityUsd: str("2300.5"), EquityAsOf: &at}
		spot := api.ConnectedAccount{Id: "ta_2", VenueConnectionId: "hl_main", Connection: hlMain, Name: "Spot", Type: api.ConnectedAccountTypeSpot, VenueRef: "spot",
			Venue: api.VenueHyperliquid, Status: api.ConnectedAccountStatusActive, EquityUsd: str("0"), EquityAsOf: &at, UnpricedAssets: &[]string{"HYPE"},
			Tradable: &no, UntradableReason: str("Mishmar does not trade spot on Hyperliquid yet")}
		bybit := api.ConnectedAccount{Id: "ta_3", VenueConnectionId: "bybit_test", Name: "Unified", Type: api.ConnectedAccountTypeUnified, VenueRef: "unified",
			Connection: api.VenueConnectionRef{Id: "bybit_test", Name: "Test", Kind: api.VenueConnectionKindExchange, AccountCount: 1, VenueAccount: api.VenueAccount{Id: str("104471"), Label: str("UID …4471"), Role: api.VenueAccountRoleMain}},
			Venue:      api.VenueBybit, Status: api.ConnectedAccountStatusActive, Traded: true, EquityUsd: str("1200"), EquityAsOf: &at}
		basis := api.ConnectedAccount{Id: "ta_4", VenueConnectionId: "hl_basis", Name: "Perps", Type: api.ConnectedAccountTypeDerivatives, VenueRef: "perps",
			Connection: api.VenueConnectionRef{Id: "hl_basis", Name: "Basis", Kind: api.VenueConnectionKindHyperliquid, ParentId: str("hl_main"), AccountCount: 2,
				VenueAccount: api.VenueAccount{Id: str("0x34cd0000000000000000000000000000000011aa"), Label: str("0x34cd…11aa"), Role: api.VenueAccountRoleSubaccount, ParentVenueAccountId: str("0x12ab000000000000000000000000000000009f3c")}},
			Venue: api.VenueHyperliquid, Status: api.ConnectedAccountStatusPending, Tradable: &no, UntradableReason: str("Mishmar does not trade a Hyperliquid sub-account yet")}
		bnCarry := api.VenueConnectionRef{Id: "bn_carry", Name: "Carry", Kind: api.VenueConnectionKindExchange, AccountCount: 3,
			VenueAccount: api.VenueAccount{Id: str("123457868"), Label: str("UID …7868"), Role: api.VenueAccountRoleMain}}
		// Binance Spot from an engine that gives no reason.
		bnSpot := api.ConnectedAccount{Id: "ta_5", VenueConnectionId: "bn_carry", Connection: bnCarry, Name: "Spot", Type: api.ConnectedAccountTypeSpot, VenueRef: "spot",
			Venue: api.VenueBinance, Status: api.ConnectedAccountStatusActive, Tradable: &no, EquityUsd: str("120"), EquityAsOf: &at}
		bnFutures := api.ConnectedAccount{Id: "ta_6", VenueConnectionId: "bn_carry", Connection: bnCarry, Name: "USDⓈ-M Futures", Type: api.ConnectedAccountTypeDerivatives, VenueRef: "usdm-futures",
			Venue: api.VenueBinance, Status: api.ConnectedAccountStatusActive, Traded: true, EquityUsd: str("2180.5"), EquityAsOf: &at}
		var out any
		switch {
		case r.Method != http.MethodGet:
		case path == "/agent/me":
			out = api.AgentIdentity{AgentId: "agt_reader", AgentName: "Reader", AccountId: "acc_main", KeyId: keyID, Permission: api.AgentPermissionRead}
		case path == "/agent/connected-accounts":
			out = []api.ConnectedAccount{perps, bybit, spot, basis, bnSpot, bnFutures}
		case path == "/agent/connected-accounts/ta_1":
			out = perps
		case path == "/agent/connected-accounts/ta_1/balances":
			out = api.BalanceSnapshot{ConnectedAccountId: "ta_1", AsOf: at, Source: api.SnapshotSourceObserved, EquityUsd: str("2300.5"), UnpricedAssets: &[]string{"PURR"},
				Balances: []api.Balance{{Asset: "USDC", Amount: "2300.5", UsdValue: str("2300.5"), PriceUsd: str("1")}, {Asset: "PURR", Amount: "20"}}}
		case path == "/agent/connected-accounts/ta_1/positions":
			out = api.AccountPositionSnapshot{ConnectedAccountId: "ta_1", AsOf: at, Source: api.SnapshotSourceObserved,
				Positions: []api.AccountPosition{{Market: api.MarketKindPerp, Symbol: "BTC", Side: "long", Size: "0.01", EntryPrice: "79500", MarkPrice: str("80000"), UnrealizedUsd: str("5")}}}
		case path == "/agent/connected-accounts/ta_1/transactions":
			perp := api.MarketKindPerp
			out = api.AccountTransactionPage{NextCursor: str("c2"), Items: []api.AccountTransaction{
				{Id: "tx_4", ConnectedAccountId: "ta_1", Kind: "trade", At: at, Market: &perp, Side: &buy, Size: str("0.01"), Symbol: str("BTC"), Price: str("79500"), FeeUsd: str("0.28"),
					Origin: &api.TransactionOrigin{Kind: api.TransactionOriginKindAgent, AgentName: str("Reader"), Own: &yes}},
				{Id: "tx_3", ConnectedAccountId: "ta_1", Kind: "trade", At: at.Add(-time.Hour), Market: &perp, Side: &sell, Size: str("2"), Symbol: str("ETH"), Price: str("3100"),
					Origin: &api.TransactionOrigin{Kind: api.TransactionOriginKindAgent, AgentName: str("Mean reversion"), Own: &no}},
				{Id: "tx_2", ConnectedAccountId: "ta_1", Kind: "transfer_in", At: at.Add(-2 * time.Hour), Amount: str("500"), Asset: str("USDC"), CounterpartConnectedAccountId: str("ta_2")},
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
		{[]string{"agent"}, []string{"agent agt_reader (Reader)", "account acc_main, key " + keyID + ", reads only"}},
		// By connection, in the order each first comes: a login's total; a
		// single account's line without its name; how many of a login's
		// accounts the agent sees when it was given fewer than all, and the
		// total of those alone.
		{[]string{"accounts"}, []string{"" +
			"ta_1  Hyperliquid · Main · Perps · 0x12ab…9f3c  2300.5 USD  active  read only\n" +
			"ta_2  Hyperliquid · Main · Spot · 0x12ab…9f3c   0 USD       active  read only (Mishmar does not trade spot on Hyperliquid yet)  not counting HYPE (no price)\n" +
			"      total                                     2300.5 USD as of 2026-11-12T10:00:00Z, not counting HYPE (no price)\n" +
			"      balances current as of 2026-11-12T10:00:00Z; transactions backfilling (0.4 done)\n" +
			"\n" +
			"ta_3  Bybit · Test · UID …4471  1200 USD  active  traded\n" +
			"\n" +
			"ta_4  Hyperliquid · Basis · Perps · 0x34cd…11aa  no value yet  pending  read only (Mishmar does not trade a Hyperliquid sub-account yet)\n" +
			"      1 of 2 accounts\n" +
			"\n" +
			"ta_5  Binance · Carry · Spot · UID …7868            120 USD     active  read only (not tradable yet)\n" +
			"ta_6  Binance · Carry · USDⓈ-M Futures · UID …7868  2180.5 USD  active  traded\n" +
			"      2 of 3 accounts, total of these               2300.5 USD as of 2026-11-12T10:00:00Z\n",
		}},
		{[]string{"account", "ta_1"}, []string{"" +
			"ta_1  Hyperliquid · Main · Perps · 0x12ab…9f3c  2300.5 USD  active  read only\n" +
			"  derivatives account perps, one of the 2 accounts of connection hl_main (hyperliquid), main login 0x12ab000000000000000000000000000000009f3c\n" +
			"  value as of 2026-11-12T10:00:00Z\n" +
			"  balances current as of 2026-11-12T10:00:00Z; transactions backfilling (0.4 done)\n",
		}},
		{[]string{"balances", "ta_1"}, []string{"" +
			"ta_1 at 2026-11-12T10:00:00Z (observed), value 2300.5 USD, not counting PURR (no price)\n" +
			"  USDC  2300.5  2300.5 USD  at 1\n" +
			"  PURR  20      -\n",
		}},
		{[]string{"positions", "ta_1", "--at", "2026-11-01"}, []string{"long  BTC perp  0.01  entry 79500  mark 80000  unrealised 5 USD"}},
		{[]string{"transactions", "ta_1", "--from", "2026-11-01T02:00:00+02:00", "--kind", "trade", "--limit", "3"}, []string{
			"2026-11-12T10:00:00Z  trade        buy 0.01 BTC perp at 79500, fee 0.28 USD  this agent",
			"2026-11-12T09:00:00Z  trade        sell 2 ETH perp at 3100                   agent Mean reversion",
			"2026-11-12T08:00:00Z  transfer_in  500 USDC from ta_2",
			"2026-11-11T10:00:00Z  transfer_in  1000 USDC (1000 USD)",
			"more: --cursor c2",
		}},
		{[]string{"equity", "ta_1", "--resolution", "hour"}, []string{"ta_1 per hour, 2 points", "2026-11-12T09:00:00Z  2290 USD\n", "2026-11-12T10:00:00Z  2300.5 USD\n"}},
	} {
		out := run(tc.args...)
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Fatalf("mishmar read %s: want %q in:\n%s", strings.Join(tc.args, " "), want, out)
			}
		}
	}
	// No read takes a wallet any more.
	if out, err := execute(context.Background(), t, strings.NewReader(""), 10*time.Second, "read", "balances", "ta_1", "--wallet", "spot"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("--wallet: %v\n%s", err, out)
	}
	// --json is the engine's answer as it came, origin and all.
	var page api.AccountTransactionPage
	if err := json.Unmarshal([]byte(run("transactions", "ta_1", "--json")), &page); err != nil || len(page.Items) != 4 || page.Items[0].Origin == nil || !*page.Items[0].Origin.Own || *page.Items[2].CounterpartConnectedAccountId != "ta_2" {
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
	if err != nil || !strings.Contains(out, "ta_1  Hyperliquid · Main · Perps") {
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

// "read agent" says what the agent may do and, when it has some, its
// capital (contract 0.21.0: capital is the agent's).
func TestIdentityText(t *testing.T) {
	capital := "5000"
	for _, c := range []struct {
		me   api.AgentIdentity
		want string
	}{
		{api.AgentIdentity{AgentId: "agt_r", AgentName: "Reader", AccountId: "acc_main", KeyId: "key_1", Permission: api.AgentPermissionRead},
			"agent agt_r (Reader)\n  account acc_main, key key_1, reads only\n"},
		{api.AgentIdentity{AgentId: "agt_t", AgentName: "Trend", AccountId: "acc_main", KeyId: "key_2", Permission: api.AgentPermissionTrade, CapitalUsd: &capital},
			"agent agt_t (Trend)\n  account acc_main, key key_2, reads and trades\n  capital 5000 USD\n"},
		// An engine from before contract 0.20.0 names no permission: its agents trade.
		{api.AgentIdentity{AgentId: "agt_o", AgentName: "Old", AccountId: "acc_main", KeyId: "key_3"},
			"agent agt_o (Old)\n  account acc_main, key key_3, reads and trades\n"},
	} {
		if got := identityText(c.me); got != c.want {
			t.Fatalf("got %q\nwant %q", got, c.want)
		}
	}
}
