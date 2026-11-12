package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/signer"
)

// A read agent's "mishmar mcp", started the way an MCP client starts it with
// nothing but the agent's API key: it sets itself up, lists its tools, has
// every one of them called, and idles for longer than the signer channel
// took to poll and sync several times. Not one request leaves the seven reads
// a hosted account's collector answers while the engine sleeps, and every
// tool but the account reads answers that it is not open to a read agent.
func TestMCPForAReadAgentCallsOnlyItsReads(t *testing.T) {
	const keyID = "key_main_abcdefgh2345"
	secret, key := newAPIKey(t, "self", keyID)
	eng, srv := newReadEngine(t, keyID, key.PublicKeyB64())
	t.Setenv(signer.EnvAPIKey, secret)
	t.Setenv(signer.EnvEngine, srv.URL+"/v1")
	path := filepath.Join(t.TempDir(), "agent.json")

	args := map[string]map[string]any{
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
		"get_account_balances":      {"connectedAccountId": "ta_1"},
		"get_account_positions":     {"connectedAccountId": "ta_1", "at": "2026-11-01"},
		"list_account_transactions": {"connectedAccountId": "ta_1", "kind": "trade"},
		"get_account_equity":        {"connectedAccountId": "ta_1", "resolution": "hour"},
	}
	reads := map[string]bool{"get_identity": true, "list_connected_accounts": true, "get_connected_account": true, "get_account_balances": true,
		"get_account_positions": true, "list_account_transactions": true, "get_account_equity": true}
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)
	calls := map[float64]string{}
	for i, name := range names {
		calls[float64(10+i)] = name
	}

	r, w := io.Pipe()
	go func() {
		send := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = w.Write(append(b, '\n'))
		}
		send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}})
		send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
		send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
		for i, name := range names {
			send(map[string]any{"jsonrpc": "2.0", "id": 10 + i, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args[name]}})
		}
		// Idle: the signer channel polled at once and then after 1, 2 and 4 s
		// of backoff, and synced its keys at once and every 5 s.
		time.Sleep(5 * time.Second)
		w.Close()
	}()
	out, err := execute(context.Background(), t, r, 40*time.Second, "mcp", "--config", path, "--log-level", "error")
	if err != nil {
		t.Fatalf("mishmar mcp: %v\n%s", err, out)
	}

	answered := map[string]bool{}
	listed := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var res struct {
			ID     float64 `json:"id"`
			Result struct {
				Tools   []struct{ Name string } `json:"tools"`
				Content []struct{ Text string } `json:"content"`
				IsError bool                    `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &res); err != nil {
			t.Fatalf("not JSON-RPC: %q", line)
		}
		if res.ID == 2 {
			listed = len(res.Result.Tools)
			for _, tl := range res.Result.Tools {
				if _, ok := args[tl.Name]; !ok {
					t.Fatalf("no arguments for %s: add them, so a read agent's session calls every tool", tl.Name)
				}
			}
		}
		name, ok := calls[res.ID]
		if !ok {
			continue
		}
		answered[name] = true
		text := ""
		if len(res.Result.Content) > 0 {
			text = res.Result.Content[0].Text
		}
		switch {
		case reads[name] && res.Result.IsError:
			t.Fatalf("%s: %s", name, text)
		case !reads[name] && (!res.Result.IsError || !strings.Contains(text, "not open to a read agent") || !strings.Contains(text, "list_connected_accounts")):
			t.Fatalf("%s for a read agent: %v %q", name, res.Result.IsError, text)
		}
	}
	if listed != len(args) || len(answered) != len(args) {
		t.Fatalf("%d tools listed, %d answered, of %d", listed, len(answered), len(args))
	}

	eng.mu.Lock()
	defer eng.mu.Unlock()
	if len(eng.others) > 0 {
		t.Fatalf("a read agent's mishmar mcp called %v, which the collector does not answer", eng.others)
	}
	me := 0
	for _, s := range eng.seen {
		if strings.HasPrefix(s, "GET /agent/me?") {
			me++
		}
	}
	if me < 2 {
		t.Fatalf("GET /agent/me asked %d times; the setup and the signer each ask it: %v", me, eng.seen)
	}
}

// "mishmar propose" with a read agent's key says what it is and sends no proposal.
func TestProposeAsAReadAgentSendsNothing(t *testing.T) {
	const keyID = "key_main_abcdefgh2345"
	secret, key := newAPIKey(t, "self", keyID)
	eng, srv := newReadEngine(t, keyID, key.PublicKeyB64())
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := (&signer.Config{Engine: srv.URL + "/v1", KeyID: keyID, Secret: secret, AgentID: "agt_reader", VenueKeys: map[string]signer.VenueKey{}}).Save(path); err != nil {
		t.Fatal(err)
	}
	_, err := execute(context.Background(), t, strings.NewReader(""), 10*time.Second, "propose", "--config", path, "--connected-account", "ta_1", "--symbol", "BTC", "--size", "1", "--market")
	if err == nil || !strings.Contains(err.Error(), "is a read agent") || !strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("propose as a read agent: %v", err)
	}
	if len(eng.others) > 0 {
		t.Fatalf("sent %v", eng.others)
	}
}
