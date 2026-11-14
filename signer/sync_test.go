package signer

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
)

// keyEngine is the engine's side of what SyncKeys does: the agent's list of
// venue keys, the connected account they are on, and FROST's distributed key
// generation for a key made without a share. It checks agentAuth the way
// fakeEngine does.
type keyEngine struct {
	*fakeEngine
	mu      sync.Mutex
	keys    []api.VenueKey
	dkg     map[string]*frost.Keygen // open sessions by id
	keygens int
}

func newKeyEngine(pub, keyID string) *keyEngine {
	return &keyEngine{fakeEngine: newFakeEngine(pub, keyID), dkg: map[string]*frost.Keygen{}}
}

func (k *keyEngine) set(keys ...api.VenueKey) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys = keys
}

func (k *keyEngine) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/agent/me", k.auth(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, http.StatusOK, api.AgentIdentity{AgentId: "agt_1", AgentName: "Reversion", AccountId: "acc_main", KeyId: k.keyID})
	}))
	mux.HandleFunc("GET /v1/agent/venue-keys", k.auth(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		k.mu.Lock()
		defer k.mu.Unlock()
		writeJSON(w, http.StatusOK, k.keys)
	}))
	mux.HandleFunc("POST /v1/agent/signer/keygen", k.auth(k.keygen))
	mux.HandleFunc("POST /v1/agent/signer/poll", k.auth(k.poll))
	return mux
}

// keygen is the engine's two rounds of FROST's DKG: it opens with its first
// message, and closes with its last once the agent answered, recording the
// key's public key on the venue key.
func (k *keyEngine) keygen(w http.ResponseWriter, r *http.Request, body []byte) {
	var in api.KeygenRound
	if err := json.Unmarshal(body, &in); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: err.Error()})
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keygens++
	switch in.Round {
	case api.KeygenRoundRoundN1:
		kg, err := frost.NewKeygen(frost.EngineID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, api.Error{Code: "internal", Message: err.Error()})
			return
		}
		first, err := kg.First()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, api.Error{Code: "internal", Message: err.Error()})
			return
		}
		session := "kgs_" + in.VenueKeyId
		k.dkg[session] = kg
		writeJSON(w, http.StatusOK, api.KeygenRoundResult{SessionId: session, Message: base64.StdEncoding.EncodeToString(first)})
	case api.KeygenRoundRoundN2:
		kg := k.dkg[*in.SessionId]
		reply, _ := base64.StdEncoding.DecodeString(*in.Message)
		last, share, err := kg.Last(reply)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, api.Error{Code: "validation", Message: err.Error()})
			return
		}
		delete(k.dkg, *in.SessionId)
		pub := base64.StdEncoding.EncodeToString(share.PublicKey())
		for i := range k.keys {
			if k.keys[i].Id == in.VenueKeyId {
				done := api.VenueKeyKeygenDone
				k.keys[i].Keygen = &done
				pem := frost.PublicKeyPEM(share.PublicKey())
				k.keys[i].PublicKey = &pem
			}
		}
		writeJSON(w, http.StatusOK, api.KeygenRoundResult{SessionId: *in.SessionId, Message: base64.StdEncoding.EncodeToString(last), PublicKey: &pub})
	}
}

// pendingKey is a Binance key the console added without a share.
func pendingKey(id string) api.VenueKey {
	kg := api.VenueKeyKeygenPending
	return api.VenueKey{Id: id, AgentId: "agt_1", ConnectedAccountId: "ta_bn", Venue: api.VenueBinance, Scheme: api.VenueKeySchemeEd25519, Status: api.VenueKeyStatusPending, Keygen: &kg}
}

// syncSigner is a signer on a saved config with no trading keys, against the
// key engine.
func syncSigner(t *testing.T) (*Signer, *keyEngine, string) {
	t.Helper()
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	key := agentkey.FromSeed(seed)
	eng := newKeyEngine(key.PublicKeyB64(), "key_main_abcdefgh2345")
	srv := httptest.NewServer(eng.handler())
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "agent.json")
	cfg := &Config{Engine: srv.URL + "/v1", KeyID: "key_main_abcdefgh2345", Secret: agentkey.Secret("msk_abcdef", seed), AgentID: "agt_1", VenueKeys: map[string]VenueKey{}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, nil, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigPath = path
	return s, eng, path
}

func TestSyncKeysGeneratesWhatTheConsoleAdded(t *testing.T) {
	s, eng, path := syncSigner(t)
	eng.set(pendingKey("vk_1"))
	ctx := context.Background()

	made, err := s.SyncKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 1 || made[0].VenueKeyID != "vk_1" || made[0].Scheme != SchemeFrostEd25519 || !strings.HasPrefix(made[0].PublicKeyPEM, "-----BEGIN PUBLIC KEY-----") || made[0].Address != "" {
		t.Fatalf("made %+v", made)
	}
	// Held in memory and saved, with the share the agent's half: the key signs
	// now, without a restart.
	if _, ok := s.venueKeys()["vk_1"]; !ok {
		t.Fatal("the key is not held in memory")
	}
	if _, err := s.share("vk_1"); err != nil {
		t.Fatalf("share: %v", err)
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := saved.VenueKeys["vk_1"]; !ok || k.ConnectedAccountID != "ta_bn" || k.Venue != "binance" || k.Scheme != SchemeFrostEd25519 {
		t.Fatalf("saved %+v", saved.VenueKeys)
	}
	// The engine's public key is the one the share derives.
	if pem := frost.PublicKeyPEM(mustShare(t, saved.VenueKeys["vk_1"].Share).PublicKey()); pem != *eng.keys[0].PublicKey {
		t.Fatalf("public keys differ:\n%s\n%s", pem, *eng.keys[0].PublicKey)
	}
	// A second sync has nothing to do: key generation ran once.
	if made, err := s.SyncKeys(ctx); err != nil || len(made) != 0 || eng.keygens != 2 {
		t.Fatalf("second sync: %v %v, %d keygen rounds", made, err, eng.keygens)
	}
}

func TestSyncKeysDropsRevokedKeysAndAdoptsTheFile(t *testing.T) {
	s, eng, path := syncSigner(t)
	eng.set(pendingKey("vk_1"))
	ctx := context.Background()
	if _, err := s.SyncKeys(ctx); err != nil {
		t.Fatal(err)
	}
	// Another command adds a key to the file (as "signer add-key" does): the
	// running signer holds it after the next sync.
	agentShare, _, err := frost.Deal()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(agentShare)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := onDisk.AddVenueKey("vk_dealt", VenueKey{ConnectedAccountID: "ta_2", Venue: "binance", Scheme: SchemeFrostEd25519, Share: raw}); err != nil {
		t.Fatal(err)
	}
	if err := onDisk.Save(path); err != nil {
		t.Fatal(err)
	}
	// The dealt key the engine lists as active; the generated one it now reports revoked.
	revoked := eng.keys[0]
	revoked.Status = api.VenueKeyStatusRevoked
	eng.set(revoked, api.VenueKey{Id: "vk_dealt", AgentId: "agt_1", ConnectedAccountId: "ta_2", Venue: api.VenueBinance, Scheme: api.VenueKeySchemeEd25519, Status: api.VenueKeyStatusActive})
	if made, err := s.SyncKeys(ctx); err != nil || len(made) != 0 {
		t.Fatalf("sync: %v %v", made, err)
	}
	keys := s.venueKeys()
	if _, held := keys["vk_1"]; held {
		t.Fatal("the revoked key's share was kept")
	}
	if _, held := keys["vk_dealt"]; !held {
		t.Fatal("the key added to the file was not adopted")
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, held := saved.VenueKeys["vk_1"]; held {
		t.Fatal("the revoked key's share is still in the file")
	}
	if _, held := saved.VenueKeys["vk_dealt"]; !held {
		t.Fatal("the adopted key left the file")
	}
}

func TestSyncKeysLeavesKeysItCannotGenerate(t *testing.T) {
	s, eng, _ := syncSigner(t)
	// Generated by another signer: keygen done, no share here. Nothing to
	// do but say so.
	done := api.VenueKeyKeygenDone
	other := pendingKey("vk_other")
	other.Keygen = &done
	active := pendingKey("vk_active")
	active.Keygen = nil
	active.Status = api.VenueKeyStatusActive
	eng.set(other, active)
	made, err := s.SyncKeys(context.Background())
	if err != nil || len(made) != 0 || eng.keygens != 0 {
		t.Fatalf("sync: %v %v, %d keygen rounds", made, err, eng.keygens)
	}
	if len(s.venueKeys()) != 0 {
		t.Fatalf("keys %v", s.venueKeys())
	}
	if len(s.warned) != 2 {
		t.Fatalf("warned %v, want one warning per key", s.warned)
	}
}

func TestSyncKeysNeedsAConfigPath(t *testing.T) {
	s, _, _ := syncSigner(t)
	s.ConfigPath = ""
	if _, err := s.SyncKeys(context.Background()); err == nil || !strings.Contains(err.Error(), "ConfigPath") {
		t.Fatalf("err %v", err)
	}
}

func mustShare(t *testing.T, raw json.RawMessage) *frost.Share {
	t.Helper()
	sh, err := frost.ParseShare(raw)
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

func TestHyperliquidAgentName(t *testing.T) {
	if got := HyperliquidAgentName("agt_k7f2q9x1m3n4"); got != "mishmar-q9x1m3n4" {
		t.Fatalf("name %q", got)
	}
	if got := HyperliquidAgentName("agt_1"); got != "mishmar-agt_1" {
		t.Fatalf("name %q", got)
	}
}
