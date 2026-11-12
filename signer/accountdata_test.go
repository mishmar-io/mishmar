package signer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
)

// dataReads is what the engine's collector answers (engine.AgentDataRead in
// mishmar-engine): GET /agent/me, /agent/connected-accounts, one connected
// account and its balances, positions, transactions and equity. A read
// agent's calls must all be among these, or it wakes a sleeping engine.
func dataRead(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	if path == "/agent/me" || path == "/agent/connected-accounts" {
		return true
	}
	rest, ok := strings.CutPrefix(path, "/agent/connected-accounts/")
	if !ok || rest == "" {
		return false
	}
	id, sub, nested := strings.Cut(rest, "/")
	if id == "" {
		return false
	}
	switch {
	case !nested:
		return true
	case sub == "balances", sub == "positions", sub == "transactions", sub == "equity":
		return true
	}
	return false
}

// dataEngine answers the seven reads for one key, signed with it, and
// records each request's path and query; anything else is 404 and recorded too.
type dataEngine struct {
	pub, keyID string

	mu   sync.Mutex
	seen []string // "GET /agent/…?query"
}

func (d *dataEngine) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		path := strings.TrimPrefix(r.URL.Path, "/v1")
		d.mu.Lock()
		d.seen = append(d.seen, r.Method+" "+path+"?"+r.URL.RawQuery)
		d.mu.Unlock()
		req := agentkey.Request{
			Method: r.Method, Path: path, Query: r.URL.RawQuery, Body: body,
			KeyID: r.Header.Get(agentkey.HeaderKeyID), Nonce: r.Header.Get(agentkey.HeaderNonce), SignedAt: r.Header.Get(agentkey.HeaderSignedAt),
		}
		if req.KeyID != d.keyID || !agentkey.VerifyRequest(d.pub, req, r.Header.Get(agentkey.HeaderSignature)) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(api.Error{Code: "unauthorized", Message: "Invalid signature"})
			return
		}
		if !dataRead(r.Method, path) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(api.Error{Code: "not_found", Message: "the collector serves only reads of connected-account data"})
			return
		}
		at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
		own, other := true, false
		agentName := "Trend follower"
		var out any
		switch {
		case path == "/agent/me":
			out = api.AgentIdentity{AgentId: "agt_1", AgentName: agentName, AccountId: "acc_1", KeyId: d.keyID, PortfolioId: "pf_1"}
		case path == "/agent/connected-accounts":
			out = []api.ConnectedAccount{{Id: "ta_1", Name: "Main", Venue: api.VenueHyperliquid, Kind: api.ConnectedAccountKindHyperliquid, Status: api.ConnectedAccountStatusActive}}
		case strings.HasSuffix(path, "/balances"):
			equity := "1500.5"
			out = api.BalanceSnapshot{ConnectedAccountId: "ta_1", AsOf: at, Source: api.SnapshotSourceObserved, EquityUsd: &equity,
				Balances: []api.Balance{{Asset: "USDC", Wallet: "margin", Amount: "1500.5", UsdValue: &equity}}}
		case strings.HasSuffix(path, "/positions"):
			out = api.AccountPositionSnapshot{ConnectedAccountId: "ta_1", AsOf: at, Source: api.SnapshotSourceObserved,
				Positions: []api.AccountPosition{{Symbol: "BTC", Side: "long", Size: "0.01", EntryPrice: "79500"}}}
		case strings.HasSuffix(path, "/transactions"):
			next := "c2"
			agentID := "agt_1"
			out = api.AccountTransactionPage{NextCursor: &next, Items: []api.AccountTransaction{
				{Id: "tx_2", ConnectedAccountId: "ta_1", Kind: api.AccountTransactionKindTrade, At: at, Origin: &api.TransactionOrigin{Kind: api.TransactionOriginKindAgent, AgentId: &agentID, AgentName: &agentName, Own: &own}},
				{Id: "tx_1", ConnectedAccountId: "ta_1", Kind: api.AccountTransactionKindTrade, At: at.Add(-time.Hour), Origin: &api.TransactionOrigin{Kind: api.TransactionOriginKindOutside, Own: &other}},
			}}
		case strings.HasSuffix(path, "/equity"):
			out = api.EquitySeries{ConnectedAccountId: "ta_1", Resolution: api.EquityResolutionHour, Points: []api.EquityPoint{{At: at, EquityUsd: "1500.5", Source: api.SnapshotSourceObserved}}}
		default:
			out = api.ConnectedAccount{Id: strings.TrimPrefix(path, "/agent/connected-accounts/"), Name: "Main", Venue: api.VenueHyperliquid}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}

func (d *dataEngine) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.seen...)
}

func TestClientReadsConnectedAccountData(t *testing.T) {
	key := agentkey.FromSeed(testSeed(1))
	eng := &dataEngine{pub: key.PublicKeyB64(), keyID: "key_a"}
	srv := httptest.NewServer(eng.handler())
	defer srv.Close()
	c := NewClient(srv.URL+"/v1", "key_a", key)
	ctx := context.Background()
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.FixedZone("IST", 2*3600))
	to := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

	me, err := c.Me(ctx)
	if err != nil || me.AgentId != "agt_1" {
		t.Fatalf("me: %+v %v", me, err)
	}
	accounts, err := c.ConnectedAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].Id != "ta_1" {
		t.Fatalf("connected accounts: %+v %v", accounts, err)
	}
	if one, err := c.ConnectedAccount(ctx, "ta_1"); err != nil || one.Id != "ta_1" {
		t.Fatalf("connected account: %+v %v", one, err)
	}
	bal, err := c.AccountBalances(ctx, "ta_1", time.Time{})
	if err != nil || bal.EquityUsd == nil || *bal.EquityUsd != "1500.5" || bal.Balances[0].Asset != "USDC" {
		t.Fatalf("balances: %+v %v", bal, err)
	}
	if _, err := c.AccountBalances(ctx, "ta_1", from); err != nil {
		t.Fatal(err)
	}
	pos, err := c.AccountPositions(ctx, "ta_1", to)
	if err != nil || pos.Positions[0].Symbol != "BTC" {
		t.Fatalf("positions: %+v %v", pos, err)
	}
	page, err := c.AccountTransactions(ctx, "ta_1", TransactionQuery{From: from, To: to, Kind: "trade", Limit: 2, Cursor: "c1"})
	if err != nil || len(page.Items) != 2 || page.NextCursor == nil || *page.NextCursor != "c2" {
		t.Fatalf("transactions: %+v %v", page, err)
	}
	if o := page.Items[0].Origin; o == nil || o.Own == nil || !*o.Own || o.Kind != api.TransactionOriginKindAgent {
		t.Fatalf("the agent's own trade: %+v", o)
	}
	if o := page.Items[1].Origin; o == nil || o.Kind != api.TransactionOriginKindOutside || *o.Own {
		t.Fatalf("a trade from outside: %+v", o)
	}
	series, err := c.AccountEquity(ctx, "ta_1", EquityQuery{From: from, Resolution: "hour"})
	if err != nil || series.Resolution != api.EquityResolutionHour || len(series.Points) != 1 {
		t.Fatalf("equity: %+v %v", series, err)
	}

	// Each read is one of the seven the collector answers, with the query the
	// contract names, times in UTC.
	want := []string{
		"GET /agent/me?",
		"GET /agent/connected-accounts?",
		"GET /agent/connected-accounts/ta_1?",
		"GET /agent/connected-accounts/ta_1/balances?",
		"GET /agent/connected-accounts/ta_1/balances?at=2026-10-31T22%3A00%3A00Z",
		"GET /agent/connected-accounts/ta_1/positions?at=2026-11-12T00%3A00%3A00Z",
		"GET /agent/connected-accounts/ta_1/transactions?cursor=c1&from=2026-10-31T22%3A00%3A00Z&kind=trade&limit=2&to=2026-11-12T00%3A00%3A00Z",
		"GET /agent/connected-accounts/ta_1/equity?from=2026-10-31T22%3A00%3A00Z&resolution=hour",
	}
	got := eng.requests()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, r := range got {
		method, rest, _ := strings.Cut(r, " ")
		path, _, _ := strings.Cut(rest, "?")
		if !dataRead(method, path) {
			t.Fatalf("%s is not a read the collector answers", r)
		}
	}
}

// The local API passes the reads through for the SDKs' Signer, signed with
// the agent's key, and refuses a bad time before anything is sent.
func TestLocalAPIPassesTheAccountReadsThrough(t *testing.T) {
	seed := testSeed(3)
	key := agentkey.FromSeed(seed)
	eng := &dataEngine{pub: key.PublicKeyB64(), keyID: "key_1"}
	srv := httptest.NewServer(eng.handler())
	defer srv.Close()
	s, err := New(&Config{Engine: srv.URL + "/v1", KeyID: "key_1", Secret: agentkey.Secret("msk_abc", seed), AgentID: "agt_1", VenueKeys: map[string]VenueKey{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	local := httptest.NewServer(s.LocalHandler("tok"))
	defer local.Close()
	get := func(path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, local.URL+path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	for path, want := range map[string]string{
		"/me":                 `"agentName":"Trend follower"`,
		"/connected-accounts": `"id":"ta_1"`,
		"/connected-accounts/ta_1/balances?at=2026-11-01":                                              `"equityUsd":"1500.5"`,
		"/connected-accounts/ta_1/positions":                                                           `"symbol":"BTC"`,
		"/connected-accounts/ta_1/transactions?from=2026-11-01T00:00:00Z&kind=trade&limit=5&cursor=c1": `"own":true`,
		"/connected-accounts/ta_1/equity?resolution=day&to=2026-11-12":                                 `"resolution":"hour"`,
	} {
		if status, body := get(path); status != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("%s: %d %s", path, status, body)
		}
	}
	for _, want := range []string{
		"GET /agent/connected-accounts/ta_1/balances?at=2026-11-01T00%3A00%3A00Z",
		"GET /agent/connected-accounts/ta_1/transactions?cursor=c1&from=2026-11-01T00%3A00%3A00Z&kind=trade&limit=5",
		"GET /agent/connected-accounts/ta_1/equity?resolution=day&to=2026-11-12T00%3A00%3A00Z",
	} {
		found := false
		for _, r := range eng.requests() {
			found = found || r == want
		}
		if !found {
			t.Fatalf("no %s in %v", want, eng.requests())
		}
	}
	n := len(eng.requests())
	for _, path := range []string{"/connected-accounts/ta_1/balances?at=yesterday", "/connected-accounts/ta_1/transactions?limit=0", "/connected-accounts/ta_1/equity?from=x"} {
		if status, body := get(path); status != http.StatusUnprocessableEntity || !strings.Contains(body, "validation") {
			t.Fatalf("%s: %d %s", path, status, body)
		}
	}
	if len(eng.requests()) != n {
		t.Fatal("a bad read reached the engine")
	}
}
