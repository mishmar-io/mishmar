package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
)

// apiKeyEngine answers the three agent calls a signer set up from its API
// key makes: GET /agent/me (the agent), GET /agent/venue-keys (none) and
// the poll (nothing), each only for the one key it knows, signed with it.
func apiKeyEngine(t *testing.T, keyID, publicKey string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			req := agentkey.Request{
				Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/v1"), Query: r.URL.RawQuery, Body: body,
				KeyID: r.Header.Get(agentkey.HeaderKeyID), Nonce: r.Header.Get(agentkey.HeaderNonce), SignedAt: r.Header.Get(agentkey.HeaderSignedAt),
			}
			if req.KeyID != keyID || !agentkey.VerifyRequest(publicKey, req, r.Header.Get(agentkey.HeaderSignature)) {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(api.Error{Code: "unauthorized", Message: "unknown key or bad signature"})
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /v1/agent/me", auth(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.AgentIdentity{AgentId: "agt_trend", AgentName: "Trend follower", AccountId: "acc_main", KeyId: keyID, PortfolioId: "pf_main"})
	}))
	mux.HandleFunc("GET /v1/agent/venue-keys", auth(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]api.VenueKey{})
	}))
	mux.HandleFunc("POST /v1/agent/signer/poll", auth(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_ = json.NewEncoder(w).Encode([]api.SignRequest{})
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newAPIKey is a key as an engine at 0.19.0 issues it, for the environment.
func newAPIKey(t *testing.T, environment, keyID string) (string, *agentkey.Key) {
	t.Helper()
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	secret, err := agentkey.NewSecret(environment, keyID, seed)
	if err != nil {
		t.Fatal(err)
	}
	return secret, agentkey.FromSeed(seed)
}

func TestSignerRunSetsItselfUpFromTheAPIKey(t *testing.T) {
	const keyID = "key_main_abcdefgh2345"
	secret, key := newAPIKey(t, "self", keyID)
	srv := apiKeyEngine(t, keyID, key.PublicKeyB64())
	path := filepath.Join(t.TempDir(), "agent.json")
	t.Setenv(signer.EnvAPIKey, secret)
	t.Setenv(signer.EnvEngine, srv.URL+"/v1/")

	// The signer writes its config and runs; its parent going away stops it.
	r, w := io.Pipe()
	go func() {
		time.Sleep(400 * time.Millisecond)
		w.Close()
	}()
	if out, err := execute(context.Background(), t, r, 10*time.Second, "signer", "run", "--config", path, "--exit-on-stdin-close", "--log-level", "error"); err != nil {
		t.Fatalf("signer run: %v\n%s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config file: %v %v", info, err)
	}
	cfg, err := signer.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine != srv.URL+"/v1" || cfg.KeyID != keyID || cfg.AgentID != "agt_trend" || cfg.Secret != secret || len(cfg.VenueKeys) != 0 {
		t.Fatalf("config %+v", cfg)
	}

	// The same key again: the config is kept and the signer runs.
	r, w = io.Pipe()
	go func() {
		time.Sleep(300 * time.Millisecond)
		w.Close()
	}()
	if out, err := execute(context.Background(), t, r, 10*time.Second, "signer", "run", "--config", path, "--exit-on-stdin-close", "--log-level", "error"); err != nil {
		t.Fatalf("second signer run: %v\n%s", err, out)
	}

	// Another key with this config: refused, nothing overwritten.
	other, _ := newAPIKey(t, "self", "key_main_zyxwvutsrq98")
	t.Setenv(signer.EnvAPIKey, other)
	if _, err := execute(context.Background(), t, strings.NewReader(""), 5*time.Second, "signer", "run", "--config", path, "--log-level", "error"); err == nil || !strings.Contains(err.Error(), "holds another key") {
		t.Fatalf("another key: %v", err)
	}
	if again, err := signer.Load(path); err != nil || again.Secret != secret {
		t.Fatalf("config changed: %+v %v", again, err)
	}

	// No key and no config: the error says what to do.
	t.Setenv(signer.EnvAPIKey, "")
	if _, err := execute(context.Background(), t, strings.NewReader(""), 5*time.Second, "signer", "run", "--config", filepath.Join(t.TempDir(), "none.json"), "--log-level", "error"); err == nil || !strings.Contains(err.Error(), signer.EnvAPIKey) {
		t.Fatalf("no key: %v", err)
	}
}

func TestSignerRunRefusesAKeyTheEngineDoesNotKnow(t *testing.T) {
	const keyID = "key_main_abcdefgh2345"
	_, key := newAPIKey(t, "self", keyID)
	srv := apiKeyEngine(t, keyID, key.PublicKeyB64())
	// The right key id, another seed: the engine refuses the signature.
	wrong, _ := newAPIKey(t, "self", keyID)
	path := filepath.Join(t.TempDir(), "agent.json")
	t.Setenv(signer.EnvAPIKey, wrong)
	t.Setenv(signer.EnvEngine, srv.URL+"/v1")
	_, err := execute(context.Background(), t, strings.NewReader(""), 5*time.Second, "signer", "run", "--config", path, "--log-level", "error")
	if err == nil || !strings.Contains(err.Error(), "does not accept this key") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a config was written for a key the engine refused")
	}
}

func TestSignerInitFromAnAPIKeyNeedsNoFlags(t *testing.T) {
	const keyID = "key_main_abcdefgh2345"
	secret, key := newAPIKey(t, "self", keyID)
	srv := apiKeyEngine(t, keyID, key.PublicKeyB64())
	path := filepath.Join(t.TempDir(), "agent.json")
	t.Setenv(signer.EnvAPIKey, secret)
	t.Setenv(signer.EnvEngine, srv.URL+"/v1")
	out, err := runInit(t, "", "--config", path)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	cfg, err := signer.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine != srv.URL+"/v1" || cfg.KeyID != keyID || cfg.AgentID != "agt_trend" {
		t.Fatalf("config %+v", cfg)
	}
	if !strings.Contains(out, "agt_trend (Trend follower)") || !strings.Contains(out, "mishmar signer run") || strings.Contains(out, secret) {
		t.Fatalf("output:\n%s", out)
	}
	// A hosted key needs no engine at all; a self-hosted one without MISHMAR_ENGINE says so.
	t.Setenv(signer.EnvEngine, "")
	if _, err := runInit(t, "", "--config", filepath.Join(t.TempDir(), "agent.json"), "--no-check", "--agent", "agt_x"); err == nil || !strings.Contains(err.Error(), signer.EnvEngine) {
		t.Fatalf("self-hosted key without an engine: %v", err)
	}
}

func TestEngineURLFromTheKey(t *testing.T) {
	secret, _ := newAPIKey(t, "testnet", "key_k7f2q9x1m3n4_abcdefgh2345")
	p, err := agentkey.ParseSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	if url, err := signer.EngineURL(p, ""); err != nil || url != "https://api.testnet.mishmar.io/a/acc_k7f2q9x1m3n4/v1" {
		t.Fatalf("testnet: %q %v", url, err)
	}
	if url, err := signer.EngineURL(p, "http://localhost:8080/v1/"); err != nil || url != "http://localhost:8080/v1" {
		t.Fatalf("override: %q %v", url, err)
	}
	self, _ := newAPIKey(t, "self", "key_main_abcdefgh2345")
	p, _ = agentkey.ParseSecret(self)
	if _, err := signer.EngineURL(p, ""); err == nil || !strings.Contains(err.Error(), `"self"`) {
		t.Fatalf("self: %v", err)
	}
	old, _ := agentkey.ParseSecret(agentkey.Secret("msk_abc123", make([]byte, 32)))
	if _, err := signer.EngineURL(old, ""); err == nil || !strings.Contains(err.Error(), "before 0.19.0") {
		t.Fatalf("old key: %v", err)
	}
}
