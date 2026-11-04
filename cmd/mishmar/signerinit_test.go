package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
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
	// The engine's check has its own test; this one is about the file.
	common := []string{"--config", path, "--engine", "http://localhost:8080/v1", "--key-id", "key_main_abc123xyz", "--agent", "agt_1", "--no-check"}

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
	if _, err := runInit(t, "", "--config", other, "--engine", "http://localhost:8080/v1", "--key-id", "key_main_abc123xyz", "--agent", "agt_1", "--no-check"); err != nil {
		t.Fatalf("init from the environment: %v", err)
	}
	t.Setenv("MISHMAR_AGENT_SECRET", "msk_nope")
	if _, err := runInit(t, "", "--config", filepath.Join(t.TempDir(), "agent.json"), "--engine", "http://localhost:8080/v1", "--key-id", "key_x", "--agent", "agt_1", "--no-check"); err == nil {
		t.Fatal("a malformed secret was accepted")
	}
}

// agentEngine answers GET /v1/agent/venue-keys the way the engine does: only
// for the one key it knows, signed with that key's secret, and with the
// venue keys of the agent the key belongs to.
func agentEngine(t *testing.T, keyID, publicKey string, keys []api.VenueKey) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/agent/venue-keys" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(api.Error{Code: "not_found", Message: "no such path"})
			return
		}
		req := agentkey.Request{
			Method: r.Method, Path: "/agent/venue-keys", Query: r.URL.RawQuery,
			KeyID: r.Header.Get(agentkey.HeaderKeyID), Nonce: r.Header.Get(agentkey.HeaderNonce), SignedAt: r.Header.Get(agentkey.HeaderSignedAt),
		}
		if req.KeyID != keyID || !agentkey.VerifyRequest(publicKey, req, r.Header.Get(agentkey.HeaderSignature)) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(api.Error{Code: "unauthorized", Message: "unknown key or bad signature"})
			return
		}
		_ = json.NewEncoder(w).Encode(keys)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSignerInitChecksTheKeyWithTheEngine(t *testing.T) {
	newSecret := func() (string, *agentkey.Key) {
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			t.Fatal(err)
		}
		s := agentkey.Secret("msk_abc123", seed)
		k, err := agentkey.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return s, k
	}
	secret, key := newSecret()
	other, _ := newSecret()
	const keyID = "key_main_abc123xyz"
	fresh := agentEngine(t, keyID, key.PublicKeyB64(), []api.VenueKey{})
	withKeys := agentEngine(t, keyID, key.PublicKeyB64(), []api.VenueKey{{Id: "vk_1", AgentId: "agt_1"}})
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	for _, tc := range []struct {
		name, engine, secret, agent string
		written                     bool
		want                        string
	}{
		{"the key the engine knows", fresh.URL + "/v1", secret, "agt_1", true, "the engine accepts key " + keyID},
		{"another secret", fresh.URL + "/v1", other, "agt_1", false, "does not accept key " + keyID},
		{"another agent's key", withKeys.URL + "/v1", secret, "agt_2", false, "pass --agent agt_1"},
		{"its own agent's", withKeys.URL + "/v1", secret, "agt_1", true, "the engine accepts key " + keyID},
		{"the engine without /v1, or one too old to check", fresh.URL, secret, "agt_1", true, "with /v1; writing the config anyway"},
		{"an engine that is down", gone.URL + "/v1", secret, "agt_1", true, "writing the config anyway"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.json")
			out, err := runInit(t, tc.secret+"\n", "--config", path, "--engine", tc.engine, "--key-id", keyID, "--agent", tc.agent, "--secret-stdin")
			if tc.written != (err == nil) {
				t.Fatalf("written %v, error %v\n%s", tc.written, err, out)
			}
			if got := out + fmt.Sprint(err); !strings.Contains(got, tc.want) {
				t.Fatalf("want %q in:\n%s", tc.want, got)
			}
			if _, statErr := os.Stat(path); tc.written != (statErr == nil) {
				t.Fatalf("written %v, file: %v", tc.written, statErr)
			}
			if strings.Contains(out+fmt.Sprint(err), tc.secret) {
				t.Fatalf("the secret was printed:\n%s %v", out, err)
			}
		})
	}
}
