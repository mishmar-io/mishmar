package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A directory's check (Glama's, for one) starts the server with no agent
// config and asks for its tools: it has to answer, and every call has to
// say what is missing, without making a lock file.
func TestMCPWithoutAConfigListsTheTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_positions","arguments":{}}}`,
	}, "\n") + "\n")
	out, err := execute(context.Background(), t, in, 10*time.Second, "mcp", "--config", path, "--log-level", "error")
	if err != nil {
		t.Fatalf("mishmar mcp without a config: %v\n%s", err, out)
	}
	for _, want := range []string{`"protocolVersion":"2025-06-18"`, `"name":"propose_order"`, `"name":"get_venue_rules"`, "no agent config at " + path} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a lock file was made without a config: %v", err)
	}
}
