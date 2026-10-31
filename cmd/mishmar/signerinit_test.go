package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/signer"
)

func runInit(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"signer", "init"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestSignerInitWritesTheConfigFromAnIssuedKey(t *testing.T) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	secret := agentkey.Secret("msk_abc123", seed)
	path := filepath.Join(t.TempDir(), "agent.json")
	common := []string{"--config", path, "--engine", "http://localhost:8080/v1", "--key-id", "key_main_abc123xyz", "--agent", "agt_1"}

	// No secret anywhere: nothing is written.
	t.Setenv("MISHMAR_AGENT_SECRET", "")
	if _, err := runInit(t, "", common...); err == nil || !strings.Contains(err.Error(), "no secret") {
		t.Fatalf("without a secret: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a config was written without a secret")
	}
	// Piped in: written with mode 0600, loadable, and the key parses.
	out, err := runInit(t, secret+"\n", append(common, "--secret-stdin")...)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config file: %v %v", info, err)
	}
	cfg, err := signer.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine != "http://localhost:8080/v1" || cfg.KeyID != "key_main_abc123xyz" || cfg.AgentID != "agt_1" || cfg.Secret != secret || len(cfg.VenueKeys) != 0 {
		t.Fatalf("config: %+v", cfg)
	}
	if _, err := cfg.Key(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mishmar signer keygen") || strings.Contains(out, secret) {
		t.Fatalf("output names the next step and never the secret:\n%s", out)
	}
	// A second run does not overwrite.
	if _, err := runInit(t, secret+"\n", append(common, "--secret-stdin")...); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("second init: %v", err)
	}
	// From the environment, into another file; a malformed secret is refused.
	other := filepath.Join(t.TempDir(), "agent.json")
	t.Setenv("MISHMAR_AGENT_SECRET", secret)
	if _, err := runInit(t, "", "--config", other, "--engine", "http://localhost:8080/v1", "--key-id", "key_main_abc123xyz", "--agent", "agt_1"); err != nil {
		t.Fatalf("init from the environment: %v", err)
	}
	t.Setenv("MISHMAR_AGENT_SECRET", "msk_nope")
	if _, err := runInit(t, "", "--config", filepath.Join(t.TempDir(), "agent.json"), "--engine", "http://localhost:8080/v1", "--key-id", "key_x", "--agent", "agt_1"); err == nil {
		t.Fatal("a malformed secret was accepted")
	}
}
