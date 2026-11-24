package mcp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mishmar-io/mishmar/signer"
)

// When the engine no longer takes the agent's key, a tool's error keeps the
// engine's words and adds the likely cause and what to do. Another refusal,
// another 401 included, is passed on as it came.
func TestAToolSaysWhyWhenTheEngineNoLongerTakesTheKey(t *testing.T) {
	call := map[string]any{"name": "propose_order", "arguments": map[string]any{"connectedAccountId": "ta_1", "symbol": "BTC", "side": "buy", "size": "0.001", "orderType": "market"}}
	for _, c := range []struct {
		status        int
		code, message string
		adds          string
	}{
		{http.StatusUnauthorized, "unauthorized", "Unknown key", "The agent or its key was most likely removed in the console. Add the agent again or issue a new key, then set this server up with it."},
		{http.StatusUnauthorized, "unauthorized", "Key revoked", "Issue a new key in the console, then set this server up with it."},
		{http.StatusUnauthorized, "unauthorized", "Key expired", "Issue a new key in the console, then set this server up with it."},
		{http.StatusUnauthorized, "unauthorized", "Invalid signature", ""},
		{http.StatusNotFound, "not_found", "Unknown key", ""},
	} {
		b := &fakeBackend{err: &signer.Error{Status: c.status, Code: c.code, Message: c.message}}
		txt, isErr := text(t, start(t, b).call("tools/call", call))
		want := "Error: " + b.err.Error()
		if c.adds != "" {
			want += ". " + c.adds
		}
		if !isErr || txt != want {
			t.Fatalf("%d %q: %v %q, want %q", c.status, c.message, isErr, txt, want)
		}
		if c.adds == "" && strings.Contains(txt, "console") {
			t.Fatalf("%d %q names the console: %q", c.status, c.message, txt)
		}
	}
}
