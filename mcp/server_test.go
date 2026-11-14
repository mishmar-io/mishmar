package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
)

// fakeBackend records what the tools ask for and answers from a script.
type fakeBackend struct {
	mu        sync.Mutex
	submitted []signer.ProposeRequest
	decision  api.Decision
	err       error
	reads     int
	calls     []string // the account reads, as record writes them
	perm      api.AgentPermission
	permErr   error
}

func (f *fakeBackend) Submit(_ context.Context, r signer.ProposeRequest) (api.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitted = append(f.submitted, r)
	if f.err != nil {
		return api.Decision{}, f.err
	}
	d := f.decision
	d.Proposal.Symbol, d.Proposal.Side, d.Proposal.Size = r.Symbol, api.Side(r.Side), r.Size
	if r.Kind == "cancel" {
		d.Proposal.Kind, d.Proposal.TargetClientOrderId = api.ProposalKindCancel, r.TargetClientOrderID
	}
	return d, nil
}

func (f *fakeBackend) Decision(context.Context, string) (api.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	d := f.decision
	// The third read finds the parked decision approved.
	if d.Verdict == api.VerdictRequireApproval && f.reads >= 3 {
		d.Approval = &api.ApprovalOutcome{Status: api.ApprovalStatusApproved}
	}
	return d, nil
}

func (f *fakeBackend) Decisions(context.Context, signer.DecisionQuery) (api.DecisionPage, error) {
	return api.DecisionPage{Items: []api.Decision{f.decision}}, nil
}
func (f *fakeBackend) Approvals(context.Context, string) ([]api.Approval, error) {
	return []api.Approval{}, nil
}
func (f *fakeBackend) Positions(context.Context) ([]api.Position, error) {
	return []api.Position{{Symbol: "BTC", Size: "0.001"}}, nil
}
func (f *fakeBackend) Pnl(context.Context, string, string) (api.PnlSeries, error) {
	return api.PnlSeries{}, nil
}
func (f *fakeBackend) ConnectedAccount(_ context.Context, id string) (api.ConnectedAccount, error) {
	return api.ConnectedAccount{Id: id, Venue: api.VenueHyperliquid}, nil
}
func (f *fakeBackend) VenueRules(context.Context, string, string) (api.VenueRules, error) {
	return api.VenueRules{}, nil
}
func (f *fakeBackend) Permission(context.Context) (api.AgentPermission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.permErr != nil {
		return "", f.permErr
	}
	if f.perm == "" {
		return api.AgentPermissionTrade, nil
	}
	return f.perm, nil
}
func (f *fakeBackend) Me(context.Context) (api.AgentIdentity, error) {
	return api.AgentIdentity{AgentId: "agt_1", AgentName: "Reader", AccountId: "acc_1", KeyId: "key_1", PortfolioId: "pf_1"}, nil
}
func (f *fakeBackend) ConnectedAccounts(context.Context) ([]api.ConnectedAccount, error) {
	f.record("accounts")
	main := api.VenueConnectionRef{Id: "hl_main", Name: "Main", Kind: api.VenueConnectionKindHyperliquid, AccountCount: 2, VenueAccount: api.VenueAccount{Role: api.VenueAccountRoleMain}}
	value, spot := "1500", "0"
	return []api.ConnectedAccount{
		{Id: "ta_1", VenueConnectionId: "hl_main", Connection: main, Name: "Perps", Type: api.ConnectedAccountTypeDerivatives, VenueRef: "perps", Venue: api.VenueHyperliquid, EquityUsd: &value},
		{Id: "ta_2", VenueConnectionId: "hl_main", Connection: main, Name: "Spot", Type: api.ConnectedAccountTypeSpot, VenueRef: "spot", Venue: api.VenueHyperliquid, EquityUsd: &spot, UnpricedAssets: &[]string{"HYPE"}},
	}, nil
}
func (f *fakeBackend) AccountBalances(_ context.Context, id string, at time.Time) (api.BalanceSnapshot, error) {
	f.record("balances " + id + " " + stamp(at))
	return api.BalanceSnapshot{ConnectedAccountId: id, Source: api.SnapshotSourceObserved, Balances: []api.Balance{{Asset: "USDC", Amount: "1500"}}}, nil
}
func (f *fakeBackend) AccountPositions(_ context.Context, id string, at time.Time) (api.AccountPositionSnapshot, error) {
	f.record("positions " + id + " " + stamp(at))
	return api.AccountPositionSnapshot{ConnectedAccountId: id, Positions: []api.AccountPosition{{Market: api.MarketKindPerp, Symbol: "ETH", Side: "short", Size: "2", EntryPrice: "3100"}}}, nil
}
func (f *fakeBackend) AccountTransactions(_ context.Context, id string, q signer.TransactionQuery) (api.AccountTransactionPage, error) {
	f.record(fmt.Sprintf("transactions %s %s %s %s %d %s", id, stamp(q.From), stamp(q.To), q.Kind, q.Limit, q.Cursor))
	own := true
	return api.AccountTransactionPage{Items: []api.AccountTransaction{{Id: "tx_1", ConnectedAccountId: id, Kind: api.AccountTransactionKindTrade, Origin: &api.TransactionOrigin{Kind: api.TransactionOriginKindAgent, Own: &own}}}}, nil
}
func (f *fakeBackend) AccountEquity(_ context.Context, id string, q signer.EquityQuery) (api.EquitySeries, error) {
	f.record(fmt.Sprintf("equity %s %s %s %s", id, stamp(q.From), stamp(q.To), q.Resolution))
	return api.EquitySeries{ConnectedAccountId: id, Resolution: api.EquityResolutionDay, Points: []api.EquityPoint{{EquityUsd: "1500", Source: api.SnapshotSourceObserved}}}, nil
}

func (f *fakeBackend) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

// stamp is a time as the fake records it: RFC 3339 in UTC, "-" for none.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

// session drives a server over pipes, one request at a time.
type session struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Reader
	n   int
}

func start(t *testing.T, b Backend) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := New(b, "test", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, inR, outW)
		outW.Close()
	}()
	t.Cleanup(func() {
		inW.Close()
		cancel()
		<-done
	})
	return &session{t: t, in: inW, out: bufio.NewReader(outR)}
}

func (s *session) call(method string, params any) map[string]any {
	s.t.Helper()
	s.n++
	req := map[string]any{"jsonrpc": "2.0", "id": s.n, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		s.t.Fatal(err)
	}
	line, err := s.out.ReadBytes('\n')
	if err != nil {
		s.t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal(line, &res); err != nil {
		s.t.Fatalf("not JSON: %s", line)
	}
	if id, _ := res["id"].(float64); int(id) != s.n {
		s.t.Fatalf("answer %v to request %d", res["id"], s.n)
	}
	return res
}

func (s *session) notify(method string) {
	s.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		s.t.Fatal(err)
	}
}

func text(t *testing.T, res map[string]any) (string, bool) {
	t.Helper()
	result, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", res)
	}
	content := result["content"].([]any)
	first := content[0].(map[string]any)
	isErr, _ := result["isError"].(bool)
	return first["text"].(string), isErr
}

func TestInitializeListsTheTools(t *testing.T) {
	s := start(t, &fakeBackend{})
	res := s.call("initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test"}})
	result := res["result"].(map[string]any)
	if result["protocolVersion"] != "2025-03-26" || result["serverInfo"].(map[string]any)["name"] != "mishmar" || !strings.Contains(result["instructions"].(string), "require_approval") {
		t.Fatalf("initialize: %v", result)
	}
	s.notify("notifications/initialized")
	if res := s.call("ping", nil); res["error"] != nil {
		t.Fatalf("ping: %v", res)
	}
	res = s.call("tools/list", nil)
	tools := res["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		names[m["name"].(string)] = true
		if _, ok := m["inputSchema"].(map[string]any)["type"]; !ok {
			t.Fatalf("tool %s without a schema", m["name"])
		}
		// Clients decide what to confirm with the person from these hints:
		// only the two tools that reach the exchange may claim to change anything.
		name := m["name"].(string)
		if title, _ := m["title"].(string); title == "" {
			t.Fatalf("tool %s without a title", name)
		}
		ann, ok := m["annotations"].(map[string]any)
		if !ok {
			t.Fatalf("tool %s without annotations", name)
		}
		changes := name == "propose_order" || name == "cancel_order"
		if ann["readOnlyHint"] != !changes || ann["openWorldHint"] != changes {
			t.Fatalf("tool %s hints: %v", name, ann)
		}
		if changes && ann["destructiveHint"] != true {
			t.Fatalf("tool %s must be marked destructive: %v", name, ann)
		}
		if ann["idempotentHint"] != nil && ann["idempotentHint"] != (name == "cancel_order") {
			t.Fatalf("tool %s idempotent hint: %v", name, ann)
		}
	}
	for _, want := range append([]string{"propose_order", "cancel_order", "get_decision", "wait_for_decision", "list_decisions", "list_positions", "get_pnl", "list_approvals", "get_venue_rules"}, accountReads...) {
		if !names[want] {
			t.Fatalf("tool %s is missing from %v", want, names)
		}
	}
	if res := s.call("nope", nil); res["error"].(map[string]any)["code"].(float64) != codeMethodNotFound {
		t.Fatalf("unknown method: %v", res)
	}
}

func TestWithoutAConfigTheToolsAreListedAndEveryCallSaysWhy(t *testing.T) {
	s := start(t, Unconfigured("/home/x/.mishmar/agent.json"))
	s.call("initialize", map[string]any{"protocolVersion": ProtocolVersion})
	tools := s.call("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 16 {
		t.Fatalf("%d tools listed without a config, want 16", len(tools))
	}
	for _, tl := range tools {
		name := tl.(map[string]any)["name"].(string)
		res := s.call("tools/call", map[string]any{"name": name, "arguments": map[string]any{}})["result"].(map[string]any)
		text := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if res["isError"] != true || !strings.Contains(text, "no agent config at /home/x/.mishmar/agent.json") || !strings.Contains(text, signer.EnvAPIKey) || !strings.Contains(text, SetupURL) {
			t.Fatalf("%s without a config: %v", name, res)
		}
	}
}

func TestProposeOrderAndCancel(t *testing.T) {
	ack := api.VenueAck{Status: "filled", OrderId: "77"}
	b := &fakeBackend{decision: api.Decision{Id: "dec_1", Verdict: api.VerdictAllow, Proposal: api.Proposal{Venue: api.VenueHyperliquid, NotionalUsd: "79.5"}, VenueAck: &ack}}
	s := start(t, b)
	res := s.call("tools/call", map[string]any{"name": "propose_order", "arguments": map[string]any{
		"connectedAccountId": "ta_1", "symbol": "BTC", "side": "buy", "size": "0.001", "price": "79500", "validForSeconds": 30,
	}})
	txt, isErr := text(t, res)
	if isErr || !strings.HasPrefix(txt, "Decision dec_1: allow. buy 0.001 BTC on hyperliquid, notional 79.5 USD. Venue: filled, order 77.") || !strings.Contains(txt, `"id":"dec_1"`) {
		t.Fatalf("propose_order: %v %q", isErr, txt)
	}
	got := b.submitted[0]
	if got.Kind != "order" || got.ConnectedAccountID != "ta_1" || got.Side != "buy" || got.Price == nil || *got.Price != "79500" || got.ValidForSeconds != 30 || got.Channel {
		t.Fatalf("submitted: %+v", got)
	}
	// Unknown arguments are refused before anything is proposed.
	res = s.call("tools/call", map[string]any{"name": "propose_order", "arguments": map[string]any{"connectedAccountId": "ta_1", "symbol": "BTC", "side": "buy", "size": "1", "bogus": 1}})
	if txt, isErr := text(t, res); !isErr || !strings.Contains(txt, "bogus") || len(b.submitted) != 1 {
		t.Fatalf("unknown argument: %v %q", isErr, txt)
	}
	res = s.call("tools/call", map[string]any{"name": "cancel_order", "arguments": map[string]any{"connectedAccountId": "ta_1", "symbol": "BTC", "clientOrderId": "msh-1-abc"}})
	txt, isErr = text(t, res)
	if isErr || !strings.Contains(txt, "Cancel of msh-1-abc on hyperliquid") {
		t.Fatalf("cancel_order: %v %q", isErr, txt)
	}
	if got := b.submitted[1]; got.Kind != "cancel" || got.TargetClientOrderID == nil || *got.TargetClientOrderID != "msh-1-abc" {
		t.Fatalf("cancel submitted: %+v", got)
	}
	// A backend error is a tool error, not a protocol error.
	b.err = errors.New("engine refused (422 validation): size")
	res = s.call("tools/call", map[string]any{"name": "propose_order", "arguments": map[string]any{"connectedAccountId": "ta_1", "symbol": "BTC", "side": "buy", "size": "x"}})
	if txt, isErr := text(t, res); !isErr || !strings.Contains(txt, "422") {
		t.Fatalf("backend error: %v %q", isErr, txt)
	}
	if res := s.call("tools/call", map[string]any{"name": "no_such_tool"}); res["error"] == nil {
		t.Fatal("an unknown tool was called")
	}
}

func TestWaitForDecisionFollowsAnApproval(t *testing.T) {
	b := &fakeBackend{decision: api.Decision{Id: "dec_2", Verdict: api.VerdictRequireApproval, Proposal: api.Proposal{Venue: api.VenueBinance}}}
	s := start(t, b)
	res := s.call("tools/call", map[string]any{"name": "get_decision", "arguments": map[string]any{"decisionId": "dec_2"}})
	if txt, _ := text(t, res); !strings.Contains(txt, "Parked for a person's approval") {
		t.Fatalf("parked: %q", txt)
	}
	started := time.Now()
	res = s.call("tools/call", map[string]any{"name": "wait_for_decision", "arguments": map[string]any{"decisionId": "dec_2", "timeoutSeconds": 10}})
	txt, isErr := text(t, res)
	if isErr || !strings.Contains(txt, "Approval: approved") || time.Since(started) > 5*time.Second {
		t.Fatalf("wait_for_decision: %v %q after %s", isErr, txt, time.Since(started))
	}
	res = s.call("tools/call", map[string]any{"name": "list_positions"})
	if txt, isErr := text(t, res); isErr || !strings.Contains(txt, `"symbol":"BTC"`) {
		t.Fatalf("list_positions: %v %q", isErr, txt)
	}
	res = s.call("tools/call", map[string]any{"name": "get_connected_account", "arguments": map[string]any{"connectedAccountId": "ta_9"}})
	if txt, isErr := text(t, res); isErr || !strings.Contains(txt, `"id":"ta_9"`) {
		t.Fatalf("get_connected_account: %v %q", isErr, txt)
	}
}
