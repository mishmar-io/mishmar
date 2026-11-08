package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/signer/configlock"
)

// newConfig writes a signer config for an engine nobody answers on.
func newConfig(t *testing.T) string {
	t.Helper()
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	out, err := runInit(t, agentkey.Secret("msk_abc123", seed)+"\n", "--config", path, "--engine", "http://127.0.0.1:1/v1",
		"--key-id", "key_main_abc123xyz", "--agent", "agt_1", "--secret-stdin")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return path
}

// execute runs the CLI with stdin and returns what Execute returned, or fails
// the test when it has not returned by the deadline.
func execute(ctx context.Context, t *testing.T, stdin io.Reader, within time.Duration, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(stdin)
	root.SetArgs(args)
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(within):
		t.Fatalf("mishmar %s still running after %s", strings.Join(args, " "), within)
		return "", nil
	}
}

func TestSignerRunStopsWhenStdinCloses(t *testing.T) {
	path := newConfig(t)
	r, w := io.Pipe()
	go func() {
		time.Sleep(300 * time.Millisecond)
		w.Close() // the parent is gone
	}()
	if _, err := execute(context.Background(), t, r, 10*time.Second, "signer", "run", "--config", path, "--exit-on-stdin-close", "--log-level", "error"); err != nil {
		t.Fatalf("signer run: %v", err)
	}
	// The lock went with it.
	if _, held, err := configlock.Holder(path); err != nil || held {
		t.Fatalf("lock still held after the signer stopped: %v %v", held, err)
	}
}

func TestSignerRunIgnoresStdinWithoutTheFlag(t *testing.T) {
	path := newConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel() // SIGTERM, as main's NotifyContext delivers it
	}()
	start := time.Now()
	if _, err := execute(ctx, t, strings.NewReader(""), 10*time.Second, "signer", "run", "--config", path, "--log-level", "error"); err != nil {
		t.Fatalf("signer run: %v", err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("the signer stopped on an empty stdin without --exit-on-stdin-close")
	}
}

func TestASecondSignerOnAConfigIsRefused(t *testing.T) {
	path := newConfig(t)
	held, err := configlock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	for _, args := range [][]string{
		{"signer", "run", "--config", path},
		{"mcp", "--config", path},
	} {
		_, err := execute(context.Background(), t, strings.NewReader(""), 5*time.Second, args...)
		var locked *configlock.LockedError
		if !errors.As(err, &locked) || locked.PID != os.Getpid() {
			t.Fatalf("mishmar %s: %v, want refused naming pid %d", strings.Join(args, " "), err, os.Getpid())
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("(pid %d)", os.Getpid())) {
			t.Fatalf("message %q", err)
		}
	}
}

func TestAddKeyWarnsWhenASignerRuns(t *testing.T) {
	path := newConfig(t)
	agent, _, err := frost.Deal()
	if err != nil {
		t.Fatal(err)
	}
	share, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	shareFile := filepath.Join(t.TempDir(), "agent.share")
	if err := os.WriteFile(shareFile, share, 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := configlock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	out, err := execute(context.Background(), t, strings.NewReader(""), 5*time.Second,
		"signer", "add-key", "--config", path, "--venue-key", "vk_1", "--trading-account", "ta_1", "--venue", "binance", "--share", shareFile)
	if err != nil {
		t.Fatalf("add-key: %v\n%s", err, out)
	}
	if want := fmt.Sprintf("a signer is running with %s (pid %d): it picks the new key up", path, os.Getpid()); !strings.Contains(out, want) {
		t.Fatalf("output %q lacks %q", out, want)
	}
}
