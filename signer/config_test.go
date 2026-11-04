package signer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/ecdsa2p"
	"github.com/mishmar-io/mishmar/frost"
)

// testSeed is a fixed agent key seed for the tests.
func testSeed(fill byte) []byte {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = fill + byte(i)
	}
	return seed
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testConfig(t *testing.T) (*Config, *frost.Share, *frost.Share) {
	t.Helper()
	agent, engine, err := frost.Deal()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Engine: "http://localhost:8080/v1", KeyID: "key_a", Secret: agentkey.Secret("msk_abcdef", testSeed(1))}
	if err := cfg.AddVenueKey("vk_1", VenueKey{TradingAccountID: "rt_1", Venue: "binance", Scheme: SchemeFrostEd25519, Share: mustJSON(t, agent)}); err != nil {
		t.Fatal(err)
	}
	return cfg, agent, engine
}

func TestConfigRoundTrip(t *testing.T) {
	cfg, _, _ := testConfig(t)
	cfg.Hook = "/usr/local/bin/check-order"
	path := filepath.Join(t.TempDir(), "mishmar", "agent.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config written with mode %o, want 0600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("config directory made with mode %o, want 0700", perm)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Engine != cfg.Engine || got.KeyID != cfg.KeyID || got.Secret != cfg.Secret || got.Hook != cfg.Hook {
		t.Fatalf("loaded %+v, want %+v", got, cfg)
	}
	if len(got.VenueKeys) != 1 || got.VenueKeys["vk_1"].TradingAccountID != "rt_1" || got.VenueKeys["vk_1"].Venue != "binance" || got.VenueKeys["vk_1"].Scheme != SchemeFrostEd25519 {
		t.Fatalf("venue keys loaded as %+v", got.VenueKeys)
	}
	if _, err := frost.ParseShare(got.VenueKeys["vk_1"].Share); err != nil {
		t.Fatalf("the share did not survive the round trip: %v", err)
	}
	// Saving again replaces the file and keeps the mode.
	if err := got.Save(path); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after a second save: %o", info.Mode().Perm())
	}
	if err := got.AddVenueKey("vk_1", got.VenueKeys["vk_1"]); err == nil || !strings.Contains(err.Error(), "already in the config") {
		t.Fatalf("adding vk_1 twice: %v", err)
	}
}

// A replacement sits beside the key it replaces; PruneVenueKeys drops a
// share once the engine reports its key revoked or expired, and only then.
func TestReplacementAndPrune(t *testing.T) {
	cfg, _, _ := testConfig(t)
	next, _, err := frost.Deal()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddVenueKey("vk_2", VenueKey{TradingAccountID: "rt_1", Venue: "binance", Scheme: SchemeFrostEd25519, Share: mustJSON(t, next)}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.VenueKeys) != 2 {
		t.Fatalf("both keys on rt_1 stay while the replacement is pending: %+v", cfg.VenueKeys)
	}
	if dropped := cfg.PruneVenueKeys([]api.VenueKey{{Id: "vk_1", Status: api.VenueKeyStatusActive}, {Id: "vk_2", Status: api.VenueKeyStatusPending}}); len(dropped) != 0 || len(cfg.VenueKeys) != 2 {
		t.Fatalf("nothing is dead yet: dropped %v", dropped)
	}
	engine := []api.VenueKey{
		{Id: "vk_1", Status: api.VenueKeyStatusRevoked}, {Id: "vk_2", Status: api.VenueKeyStatusActive},
		{Id: "vk_9", Status: api.VenueKeyStatusExpired}, // not in the config
	}
	if dropped := cfg.PruneVenueKeys(engine); len(dropped) != 1 || dropped[0] != "vk_1" {
		t.Fatalf("dropped %v", dropped)
	}
	if _, ok := cfg.VenueKeys["vk_2"]; !ok || len(cfg.VenueKeys) != 1 {
		t.Fatalf("after the prune: %+v", cfg.VenueKeys)
	}
}

func TestConfigValidate(t *testing.T) {
	cfg, agent, engine := testConfig(t)
	agentShare := mustJSON(t, agent)
	engineShare := mustJSON(t, engine)
	ecdsaShare := mustJSON(t, testECDSAAgentKey(t))
	good := func() Config {
		c := *cfg
		c.VenueKeys = map[string]VenueKey{"vk_1": cfg.VenueKeys["vk_1"]}
		return c
	}
	cases := []struct {
		name string
		mod  func(c *Config)
		want string
	}{
		{"valid", func(c *Config) {}, ""},
		{"no venue keys yet", func(c *Config) { c.VenueKeys = nil }, ""},
		{"ecdsa key on hyperliquid", func(c *Config) {
			c.VenueKeys["vk_2"] = VenueKey{TradingAccountID: "rt_2", Venue: "hyperliquid", Scheme: SchemeECDSA2p, Share: ecdsaShare}
		}, ""},
		{"no engine", func(c *Config) { c.Engine = "" }, "engine is required"},
		{"engine not a URL", func(c *Config) { c.Engine = "localhost:8080" }, "not an http(s) URL"},
		{"no key id", func(c *Config) { c.KeyID = "" }, "keyId is required"},
		{"no secret", func(c *Config) { c.Secret = "" }, "secret is required"},
		{"bad secret", func(c *Config) { c.Secret = "msk_abc" }, "malformed agent secret"},
		{"engine share refused", func(c *Config) {
			c.VenueKeys["vk_1"] = VenueKey{TradingAccountID: "rt_1", Venue: "binance", Scheme: SchemeFrostEd25519, Share: engineShare}
		}, "engine's share, not the agent's"},
		{"scheme does not fit the venue", func(c *Config) {
			c.VenueKeys["vk_1"] = VenueKey{TradingAccountID: "rt_1", Venue: "binance", Scheme: SchemeECDSA2p, Share: ecdsaShare}
		}, "does not fit venue binance"},
		{"share of another scheme", func(c *Config) {
			c.VenueKeys["vk_2"] = VenueKey{TradingAccountID: "rt_2", Venue: "hyperliquid", Scheme: SchemeECDSA2p, Share: agentShare}
		}, "share:"},
		{"unknown scheme", func(c *Config) {
			c.VenueKeys["vk_1"] = VenueKey{TradingAccountID: "rt_1", Venue: "bybit", Scheme: "rsa-2p", Share: agentShare}
		}, "not one this signer knows"},
		{"no trading account", func(c *Config) {
			c.VenueKeys["vk_1"] = VenueKey{Venue: "binance", Scheme: SchemeFrostEd25519, Share: agentShare}
		}, "tradingAccountId is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := good()
			tc.mod(&c)
			err := c.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv(EnvConfig, "/elsewhere/agent.json")
	if p, err := DefaultPath(); err != nil || p != "/elsewhere/agent.json" {
		t.Fatalf("with %s set: %q, %v", EnvConfig, p, err)
	}
	t.Setenv(EnvConfig, "")
	p, err := DefaultPath()
	if err != nil || !strings.HasSuffix(p, filepath.Join(".mishmar", "agent.json")) {
		t.Fatalf("default: %q, %v", p, err)
	}
}

func TestSchemeOfShare(t *testing.T) {
	_, agent, _ := testConfig(t)
	if s, err := SchemeOfShare(mustJSON(t, agent)); err != nil || s != SchemeFrostEd25519 {
		t.Fatalf("frost share: %q, %v", s, err)
	}
	if s, err := SchemeOfShare(mustJSON(t, testECDSAAgentKey(t))); err != nil || s != SchemeECDSA2p {
		t.Fatalf("ecdsa share: %q, %v", s, err)
	}
	if _, err := SchemeOfShare([]byte(`{"scheme":"rsa-2p"}`)); err == nil {
		t.Fatal("an unknown scheme was accepted")
	}
	if _, err := SchemeOfShare([]byte(`not json`)); err == nil {
		t.Fatal("garbage was accepted")
	}
	if s, ok := SchemeForVenue("binance"); !ok || s != SchemeFrostEd25519 {
		t.Fatalf("binance: %q %v", s, ok)
	}
	if _, ok := SchemeForVenue("okx"); ok {
		t.Fatal("okx has a scheme")
	}
}

// testECDSAAgentKey runs two-party key generation by hand and returns the
// agent's key.
func testECDSAAgentKey(t *testing.T) *ecdsa2p.Key {
	t.Helper()
	e, err := ecdsa2p.NewKeygen(ecdsa2p.RoleEngine)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ecdsa2p.NewKeygen(ecdsa2p.RoleAgent)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := e.First()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := a.Reply(m1)
	if err != nil {
		t.Fatal(err)
	}
	m3, _, err := e.Last(m2)
	if err != nil {
		t.Fatal(err)
	}
	key, err := a.Finish(m3)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
