package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
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

// accountReads are the tools over the reads of connected-account data, the
// ones a read agent is for.
var accountReads = []string{"get_identity", "list_connected_accounts", "get_connected_account", "get_account_balances", "get_account_positions", "list_account_transactions", "get_account_equity"}

func TestAccountReadTools(t *testing.T) {
	b := &fakeBackend{}
	s := start(t, b)
	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		return text(t, s.call("tools/call", map[string]any{"name": name, "arguments": args}))
	}
	// The identity carries the agent's capital (contract 0.21.0), as the engine answers it.
	if txt, isErr := call("get_identity", nil); isErr || !strings.Contains(txt, `"agentName":"Reader"`) || !strings.Contains(txt, `"capitalUsd":"5000"`) {
		t.Fatalf("get_identity: %v %q", isErr, txt)
	}
	// Each account says which connection holds it, so one list tells them apart.
	if txt, isErr := call("list_connected_accounts", nil); isErr || !strings.Contains(txt, `"id":"ta_1"`) || strings.Count(txt, `"connection":{"accountCount":2,"id":"hl_main"`) != 2 || !strings.Contains(txt, `"unpricedAssets":["HYPE"]`) {
		t.Fatalf("list_connected_accounts: %v %q", isErr, txt)
	}
	if txt, isErr := call("get_account_balances", map[string]any{"connectedAccountId": "ta_1"}); isErr || !strings.Contains(txt, `"asset":"USDC"`) {
		t.Fatalf("get_account_balances: %v %q", isErr, txt)
	}
	// A moment with an offset is read in UTC; a date is its midnight.
	if _, isErr := call("get_account_balances", map[string]any{"connectedAccountId": "ta_1", "at": "2026-11-12T12:00:00+02:00"}); isErr {
		t.Fatal("balances at a moment")
	}
	if txt, isErr := call("get_account_positions", map[string]any{"connectedAccountId": "ta_1", "at": "2026-11-01"}); isErr || !strings.Contains(txt, `"market":"perp","side":"short","size":"2","symbol":"ETH"`) {
		t.Fatalf("get_account_positions: %v %q", isErr, txt)
	}
	txt, isErr := call("list_account_transactions", map[string]any{"connectedAccountId": "ta_1", "from": "2026-11-01", "kind": "trade"})
	if isErr || !strings.Contains(txt, `"own":true`) {
		t.Fatalf("list_account_transactions: %v %q", isErr, txt)
	}
	if _, isErr := call("list_account_transactions", map[string]any{"connectedAccountId": "ta_1", "limit": 9000, "cursor": "c2"}); isErr {
		t.Fatal("transactions, the page after")
	}
	if txt, isErr := call("get_account_equity", map[string]any{"connectedAccountId": "ta_1", "from": "2026-10-01", "to": "2026-11-01T00:00:00Z", "resolution": "day"}); isErr || !strings.Contains(txt, `"resolution":"day"`) {
		t.Fatalf("get_account_equity: %v %q", isErr, txt)
	}
	want := []string{
		"accounts",
		"balances ta_1 -",
		"balances ta_1 2026-11-12T10:00:00Z",
		"positions ta_1 2026-11-01T00:00:00Z",
		"transactions ta_1 2026-11-01T00:00:00Z - trade 50 ",
		"transactions ta_1 - -  500 c2",
		"equity ta_1 2026-10-01T00:00:00Z 2026-11-01T00:00:00Z day",
	}
	if strings.Join(b.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("backend calls:\n%s\nwant:\n%s", strings.Join(b.calls, "\n"), strings.Join(want, "\n"))
	}

	// The wallet filter of the first 0.21.0 draft is gone: an account is the pool.
	if txt, isErr := call("get_account_balances", map[string]any{"connectedAccountId": "ta_1", "walletId": "spot"}); !isErr || !strings.Contains(txt, "walletId") {
		t.Fatalf("walletId: %v %q", isErr, txt)
	}
	// A time that is neither is refused before anything is read, naming the field.
	if txt, isErr := call("get_account_equity", map[string]any{"connectedAccountId": "ta_1", "from": "yesterday"}); !isErr || !strings.Contains(txt, "from:") || !strings.Contains(txt, "RFC 3339") {
		t.Fatalf("bad time: %v %q", isErr, txt)
	}
	if txt, isErr := call("get_account_balances", map[string]any{"connectedAccountId": "ta_1", "when": "now"}); !isErr || !strings.Contains(txt, "when") {
		t.Fatalf("unknown argument: %v %q", isErr, txt)
	}
	if len(b.calls) != len(want) {
		t.Fatalf("a refused call reached the backend: %v", b.calls)
	}
}

// A read agent's key proposes nothing: the engine refuses it (403
// forbidden), and the tool says so and what the agent can do instead.
func TestAReadAgentsProposalIsRefusedClearly(t *testing.T) {
	b := &fakeBackend{err: &signer.Error{Status: http.StatusForbidden, Code: "forbidden", Message: "Agent Reader is a read agent: it cannot propose orders"}}
	s := start(t, b)
	for _, c := range []map[string]any{
		{"name": "propose_order", "arguments": map[string]any{"connectedAccountId": "ta_1", "symbol": "BTC", "side": "buy", "size": "0.001", "orderType": "market"}},
		{"name": "cancel_order", "arguments": map[string]any{"connectedAccountId": "ta_1", "symbol": "BTC", "clientOrderId": "msh-1"}},
	} {
		txt, isErr := text(t, s.call("tools/call", c))
		if !isErr || !strings.Contains(txt, "403 forbidden") || !strings.Contains(txt, "is a read agent") || !strings.Contains(txt, "list_connected_accounts") {
			t.Fatalf("%s for a read agent: %v %q", c["name"], isErr, txt)
		}
	}
	// Another refusal is passed on as it came.
	b.err = &signer.Error{Status: http.StatusUnprocessableEntity, Code: "validation", Message: "size"}
	txt, isErr := text(t, s.call("tools/call", map[string]any{"name": "propose_order", "arguments": map[string]any{"connectedAccountId": "ta_1", "symbol": "BTC", "side": "buy", "size": "x", "orderType": "market"}}))
	if !isErr || strings.Contains(txt, "list_connected_accounts") {
		t.Fatalf("a validation refusal: %v %q", isErr, txt)
	}
}

// collector answers what a hosted account's collector answers while the
// engine sleeps (engine.AgentDataRead): the seven reads of connected-account
// data, signed with the one key it knows. Every other path is 404 and
// recorded, so a read tool that would wake the engine shows.
type collector struct {
	pub, keyID string
	perm       api.AgentPermission // what GET /agent/me says

	mu     sync.Mutex
	seen   []string
	others []string
}

// collectorAnswers are the seven reads, for the one connected account the tools here name.
var collectorAnswers = map[string]bool{
	"GET /agent/me": true, "GET /agent/connected-accounts": true, "GET /agent/connected-accounts/ta_1": true,
	"GET /agent/connected-accounts/ta_1/balances": true, "GET /agent/connected-accounts/ta_1/positions": true,
	"GET /agent/connected-accounts/ta_1/transactions": true, "GET /agent/connected-accounts/ta_1/equity": true,
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	c.mu.Lock()
	c.seen = append(c.seen, r.Method+" "+path)
	if !collectorAnswers[r.Method+" "+path] {
		// Counted before anything is checked: a proposal signs its body, not the headers.
		c.others = append(c.others, r.Method+" "+path)
	}
	c.mu.Unlock()
	req := agentkey.Request{
		Method: r.Method, Path: path, Query: r.URL.RawQuery, Body: body,
		KeyID: r.Header.Get(agentkey.HeaderKeyID), Nonce: r.Header.Get(agentkey.HeaderNonce), SignedAt: r.Header.Get(agentkey.HeaderSignedAt),
	}
	if req.KeyID != c.keyID || !agentkey.VerifyRequest(c.pub, req, r.Header.Get(agentkey.HeaderSignature)) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(api.Error{Code: "unauthorized", Message: "Invalid signature"})
		return
	}
	var out any
	switch {
	case r.Method != http.MethodGet:
	case path == "/agent/me":
		out = api.AgentIdentity{AgentId: "agt_1", AgentName: "Reader", KeyId: c.keyID, Permission: c.perm}
	case path == "/agent/connected-accounts":
		out = []api.ConnectedAccount{}
	case path == "/agent/connected-accounts/ta_1":
		out = api.ConnectedAccount{Id: "ta_1"}
	case path == "/agent/connected-accounts/ta_1/balances":
		out = api.BalanceSnapshot{ConnectedAccountId: "ta_1", Balances: []api.Balance{}}
	case path == "/agent/connected-accounts/ta_1/positions":
		out = api.AccountPositionSnapshot{ConnectedAccountId: "ta_1", Positions: []api.AccountPosition{}}
	case path == "/agent/connected-accounts/ta_1/transactions":
		out = api.AccountTransactionPage{Items: []api.AccountTransaction{}}
	case path == "/agent/connected-accounts/ta_1/equity":
		out = api.EquitySeries{ConnectedAccountId: "ta_1", Points: []api.EquityPoint{}}
	}
	if out == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(api.Error{Code: "not_found", Message: "This process only collects connected-account data; the engine serves the API"})
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func TestAccountReadToolsUseOnlyTheCollectorsReads(t *testing.T) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	const keyID = "key_main_abcdefgh2345"
	secret, err := agentkey.NewSecret("self", keyID, seed)
	if err != nil {
		t.Fatal(err)
	}
	col := &collector{pub: agentkey.FromSeed(seed).PublicKeyB64(), keyID: keyID}
	srv := httptest.NewServer(col)
	t.Cleanup(srv.Close)
	// A read agent's config: its key and no trading key.
	sg, err := signer.New(&signer.Config{Engine: srv.URL + "/v1", KeyID: keyID, Secret: secret, AgentID: "agt_1", VenueKeys: map[string]signer.VenueKey{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := start(t, SignerBackend(sg))
	args := map[string]map[string]any{
		"get_identity":              {},
		"list_connected_accounts":   {},
		"get_connected_account":     {"connectedAccountId": "ta_1"},
		"get_account_balances":      {"connectedAccountId": "ta_1", "at": "2026-11-01"},
		"get_account_positions":     {"connectedAccountId": "ta_1"},
		"list_account_transactions": {"connectedAccountId": "ta_1", "from": "2026-11-01", "kind": "funding", "cursor": "c1"},
		"get_account_equity":        {"connectedAccountId": "ta_1", "resolution": "hour"},
	}
	for _, name := range accountReads {
		if txt, isErr := text(t, s.call("tools/call", map[string]any{"name": name, "arguments": args[name]})); isErr {
			t.Fatalf("%s: %s", name, txt)
		}
	}
	col.mu.Lock()
	defer col.mu.Unlock()
	if len(col.others) > 0 || len(col.seen) != len(accountReads) {
		t.Fatalf("the read tools called %v, of which %v are not the collector's", col.seen, col.others)
	}
}

// toolArgs are arguments every tool takes, so a session can call them all.
var toolArgs = map[string]map[string]any{
	"propose_order":             {"connectedAccountId": "ta_1", "symbol": "BTC", "side": "buy", "size": "0.001", "orderType": "market"},
	"cancel_order":              {"connectedAccountId": "ta_1", "symbol": "BTC", "clientOrderId": "msh-1"},
	"get_decision":              {"decisionId": "dec_1"},
	"wait_for_decision":         {"decisionId": "dec_1", "timeoutSeconds": 1},
	"list_decisions":            {},
	"list_positions":            {},
	"get_pnl":                   {},
	"list_approvals":            {},
	"get_venue_rules":           {"connectedAccountId": "ta_1"},
	"get_identity":              {},
	"list_connected_accounts":   {},
	"get_connected_account":     {"connectedAccountId": "ta_1"},
	"get_account_balances":      {"connectedAccountId": "ta_1", "at": "2026-11-01"},
	"get_account_positions":     {"connectedAccountId": "ta_1"},
	"list_account_transactions": {"connectedAccountId": "ta_1", "from": "2026-11-01", "kind": "funding", "cursor": "c1"},
	"get_account_equity":        {"connectedAccountId": "ta_1", "resolution": "hour"},
}

// A read agent's whole session, the signer running as in "mishmar mcp":
// it starts, lists the tools, calls every one of them and idles for many
// rounds of the signer's loop, and nothing it sends is outside the reads a
// hosted account's collector answers while the engine sleeps. Every tool
// but the account reads answers here, naming the reads to use.
func TestAReadAgentsSessionCallsOnlyTheCollectorsReads(t *testing.T) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	const keyID = "key_main_abcdefgh2345"
	secret, err := agentkey.NewSecret("self", keyID, seed)
	if err != nil {
		t.Fatal(err)
	}
	col := &collector{pub: agentkey.FromSeed(seed).PublicKeyB64(), keyID: keyID, perm: api.AgentPermissionRead}
	srv := httptest.NewServer(col)
	t.Cleanup(srv.Close)
	sg, err := signer.New(&signer.Config{Engine: srv.URL + "/v1", KeyID: keyID, Secret: secret, AgentID: "agt_1", VenueKeys: map[string]signer.VenueKey{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// As "mishmar mcp" runs it, with the key sync on and the permission asked again quickly.
	sg.ConfigPath = filepath.Join(t.TempDir(), "agent.json")
	sg.KeySyncInterval, sg.PermissionInterval, sg.PollWait = 20*time.Millisecond, 30*time.Millisecond, time.Second
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- sg.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-ran
	})
	s := start(t, SignerBackend(sg))
	s.call("initialize", map[string]any{"protocolVersion": ProtocolVersion})
	tools := s.call("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != len(toolArgs) {
		t.Fatalf("%d tools listed, %d with arguments here", len(tools), len(toolArgs))
	}
	reads := map[string]bool{}
	for _, name := range accountReads {
		reads[name] = true
	}
	for _, tl := range tools {
		name := tl.(map[string]any)["name"].(string)
		args, ok := toolArgs[name]
		if !ok {
			t.Fatalf("no arguments for %s: add them, so a read agent's session calls every tool", name)
		}
		txt, isErr := text(t, s.call("tools/call", map[string]any{"name": name, "arguments": args}))
		switch {
		case reads[name] && isErr:
			t.Fatalf("%s: %s", name, txt)
		case !reads[name] && (!isErr || !strings.Contains(txt, "not open to a read agent") || !strings.Contains(txt, "nothing was sent") || !strings.Contains(txt, "list_connected_accounts")):
			t.Fatalf("%s for a read agent: %v %q", name, isErr, txt)
		}
	}
	if p, known := sg.Permission(); !known || p != api.AgentPermissionRead || !sg.Client.ReadOnly() {
		t.Fatalf("permission %q known %v, client read-only %v", p, known, sg.Client.ReadOnly())
	}
	time.Sleep(300 * time.Millisecond) // ten rounds of the permission check, fifteen of the key sync
	col.mu.Lock()
	defer col.mu.Unlock()
	if len(col.others) > 0 {
		t.Fatalf("a read agent's session called %v, which the collector does not answer", col.others)
	}
	me := 0
	for _, r := range col.seen {
		if r == "GET /agent/me" {
			me++
		}
	}
	if me < 4 {
		t.Fatalf("GET /agent/me asked %d times; the signer asks it at the start and again on its timer: %v", me, col.seen)
	}
}

func TestEveryOtherToolAnswersAReadAgentHere(t *testing.T) {
	b := &fakeBackend{perm: api.AgentPermissionRead}
	s := start(t, b)
	for name, args := range toolArgs {
		txt, isErr := text(t, s.call("tools/call", map[string]any{"name": name, "arguments": args}))
		read := false
		for _, r := range accountReads {
			read = read || r == name
		}
		if read == isErr || (!read && !strings.Contains(txt, "not open to a read agent")) {
			t.Fatalf("%s for a read agent: %v %q", name, isErr, txt)
		}
	}
	if len(b.submitted) != 0 || b.reads != 0 {
		t.Fatalf("a refused tool reached the backend: %d proposals, %d decision reads", len(b.submitted), b.reads)
	}
	// Before the signer knows, the account reads still answer, and the rest say why they wait.
	b.permErr = errors.New("GET /agent/me: connection refused")
	if txt, isErr := text(t, s.call("tools/call", map[string]any{"name": "list_positions"})); !isErr || !strings.Contains(txt, "has not yet") || !strings.Contains(txt, "connection refused") {
		t.Fatalf("permission not known: %v %q", isErr, txt)
	}
	if _, isErr := text(t, s.call("tools/call", map[string]any{"name": "list_connected_accounts"})); isErr {
		t.Fatal("an account read waited for the permission")
	}
}

// The instructions and the account reads tell a model what an account is:
// one pool of money, held by a connection (the venue login), every balance,
// position and transaction one account's, and a move between two accounts
// of a connection on both with the other side named. No tool speaks of
// wallets or takes one.
func TestTheAccountReadsDescribeTheAccountModel(t *testing.T) {
	s := start(t, &fakeBackend{})
	res := s.call("initialize", map[string]any{"protocolVersion": ProtocolVersion})
	// Line breaks in the instructions are only wrapping.
	instructions := strings.Join(strings.Fields(res["result"].(map[string]any)["instructions"].(string)), " ")
	for _, want := range []string{"one pool of money", "venue login", "belongs to exactly one account", "accountCount", "the accounts you can see", "transfer_out on one and a transfer_in on the other", "counterpartConnectedAccountId", "never count it as a deposit or a withdrawal"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("the instructions do not say %q:\n%s", want, instructions)
		}
	}
	for _, gone := range []string{"wallet", "walletId", "wallet_transfer"} {
		if strings.Contains(instructions, gone) {
			t.Fatalf("the instructions still say %q", gone)
		}
	}
	descriptions := map[string]string{}
	for _, tl := range s.call("tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		m := tl.(map[string]any)
		name, desc := m["name"].(string), m["description"].(string)
		descriptions[name] = desc
		props := m["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if strings.Contains(strings.ToLower(desc), "wallet") || props["walletId"] != nil {
			t.Fatalf("%s still speaks of wallets: %s %v", name, desc, props)
		}
		if name == "list_account_transactions" {
			if kinds := props["kind"].(map[string]any)["enum"].([]any); fmt.Sprint(kinds) != fmt.Sprint(transactionKinds) {
				t.Fatalf("kinds %v", kinds)
			}
		}
	}
	for name, wants := range map[string][]string{
		"list_connected_accounts":   {"one pool of money", "connection", "venueAccount", "connection.sync", "same connection.id", "only when you see accountCount of them", "the accounts you can see", "unpricedAssets", "tradable", "Binance · Carry · USDⓈ-M Futures · UID …7868", "leaving the account name out when accountCount is 1"},
		"get_connected_account":     {"connection", "venue login", "tradable", "sync", "accountCount"},
		"get_account_balances":      {"priceUsd", "unpricedAssets", "not in equityUsd", "read on its own"},
		"get_account_positions":     {"per market (a symbol with its market kind)", "an asset held outright is a balance"},
		"list_account_transactions": {"counterpartConnectedAccountId", "opposite kind", "no money in or out", "a deposit, a withdrawal, or an account not connected here"},
		"get_account_equity":        {"no other account's", "only when you have all connection.accountCount of them"},
	} {
		for _, want := range wants {
			if !strings.Contains(descriptions[name], want) {
				t.Fatalf("%s does not say %q: %s", name, want, descriptions[name])
			}
		}
	}
}
