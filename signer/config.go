// Package signer is the agent's signer: the process that holds the agent's
// identity key and the agent's shares of its venue keys, long-polls the
// engine for venue requests to co-sign, checks each one against the order
// the agent proposed, and answers with its share of the signature or a
// refusal. The engine holds the other share and never sees this one; an
// order the signer did not propose, or that differs from what it proposed,
// is refused before anything is signed.
//
// It is a library: Signer.Run is the loop and Signer.Handle one request, so
// an SDK can embed it or run `mishmar signer run` as a child process.
package signer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/ecdsa2p"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/venues/hyperliquid"
)

// EnvConfig names the environment variable that overrides the default
// config path.
const EnvConfig = "MISHMAR_AGENT_CONFIG"

// The signing schemes a venue key in the config may name: the contract's
// SigningScheme, decided by the venue's key type.
const (
	SchemeFrostEd25519 = string(api.SigningSchemeFrostEd25519)
	SchemeECDSA2p      = string(api.SigningSchemeEcdsa2p)
)

// Config is the signer's file: the engine, the agent's identity key, and the
// agent's share of each venue key, by venue key id. It holds the identity
// secret and the agent's shares only, never an engine share; Validate refuses
// one. Written with mode 0600.
type Config struct {
	// Engine is the API base URL, including /v1: http://localhost:8080/v1.
	Engine string `json:"engine"`
	// KeyID is the agent key's id (key_…); Secret the secret shown once when
	// it was issued (msk_…).
	KeyID  string `json:"keyId"`
	Secret string `json:"secret"`
	// AgentID is the agent the key belongs to (agt_…). The signer needs it to
	// name the orders it builds for one-shot signing; without it every
	// proposal goes through the channel.
	AgentID string `json:"agentId,omitempty"`
	// VenueKeys are the agent's trading keys, by the id the engine gave each
	// (vk_…).
	VenueKeys map[string]VenueKey `json:"venueKeys"`
	// Hook is an optional command run before each signature with the
	// SignRequest JSON on stdin; a non-zero exit refuses the request.
	Hook string `json:"hook,omitempty"`
}

// VenueKey is the agent's side of one trading key.
type VenueKey struct {
	TradingAccountID string `json:"tradingAccountId"`
	Venue            string `json:"venue"`
	// Scheme is the signing scheme the key runs (SchemeFrostEd25519, …).
	Scheme string `json:"scheme"`
	// Share is the agent's share as the scheme's package marshals it
	// (frost.Share.MarshalJSON, ecdsa2p.Key.MarshalJSON).
	Share json.RawMessage `json:"share"`
	// Network is the Hyperliquid network the key signs for, mainnet or
	// testnet, which the digest depends on. Hyperliquid keys only.
	Network string `json:"network,omitempty"`
}

// DefaultPath is where the signer looks for its config: $MISHMAR_AGENT_CONFIG,
// else ~/.mishmar/agent.json.
func DefaultPath() (string, error) {
	if p := os.Getenv(EnvConfig); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("signer: no home directory for the default config path; set %s: %w", EnvConfig, err)
	}
	return filepath.Join(home, ".mishmar", "agent.json"), nil
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("signer: config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("signer: config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("signer: config %s: %w", path, err)
	}
	return &c, nil
}

// Save validates the config and writes it with mode 0600 (the directory 0700
// when it has to be made), replacing the file atomically.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("signer: config: %w", err)
	}
	out := *c
	if out.VenueKeys == nil {
		out.VenueKeys = map[string]VenueKey{}
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("signer: config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".agent-*.json")
	if err != nil {
		return fmt.Errorf("signer: config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("signer: config: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("signer: config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("signer: config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("signer: config: %w", err)
	}
	return nil
}

// Validate names the first thing wrong with the config.
func (c *Config) Validate() error {
	u, err := url.Parse(c.Engine)
	switch {
	case c.Engine == "":
		return errors.New("engine is required (the API base URL, e.g. http://localhost:8080/v1)")
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return fmt.Errorf("engine %q is not an http(s) URL", c.Engine)
	case c.KeyID == "":
		return errors.New("keyId is required (the agent key's id, key_…)")
	case c.Secret == "":
		return errors.New("secret is required (the agent key's secret, msk_…)")
	}
	if _, err := agentkey.Parse(c.Secret); err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	ids := make([]string, 0, len(c.VenueKeys))
	for id := range c.VenueKeys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := c.VenueKeys[id].validate(); err != nil {
			return fmt.Errorf("venueKeys[%s]: %w", id, err)
		}
	}
	return nil
}

// Key parses the identity key from the secret.
func (c *Config) Key() (*agentkey.Key, error) {
	return agentkey.Parse(c.Secret)
}

// AddVenueKey stores the agent's share of a venue key under its id. An id
// already in the config is refused: remove it first.
func (c *Config) AddVenueKey(id string, k VenueKey) error {
	if id == "" {
		return errors.New("signer: config: a venue key needs an id")
	}
	if _, dup := c.VenueKeys[id]; dup {
		return fmt.Errorf("signer: config: venue key %s is already in the config; remove it first", id)
	}
	if err := k.validate(); err != nil {
		return fmt.Errorf("signer: config: venue key %s: %w", id, err)
	}
	if c.VenueKeys == nil {
		c.VenueKeys = map[string]VenueKey{}
	}
	c.VenueKeys[id] = k
	return nil
}

// validate checks the entry's shape and that the share is the agent's half
// of a key of the named scheme.
func (k VenueKey) validate() error {
	switch {
	case k.TradingAccountID == "":
		return errors.New("tradingAccountId is required")
	case k.Venue == "":
		return errors.New("venue is required")
	case k.Scheme == "":
		return errors.New("scheme is required")
	case len(k.Share) == 0:
		return errors.New("share is required")
	}
	if want, ok := SchemeForVenue(k.Venue); ok && want != k.Scheme {
		return fmt.Errorf("scheme %s does not fit venue %s, which takes %s", k.Scheme, k.Venue, want)
	}
	if k.Network != "" {
		if api.Venue(k.Venue) != api.VenueHyperliquid {
			return fmt.Errorf("network is for Hyperliquid keys, not %s", k.Venue)
		}
		if _, err := hyperliquid.ParseNetwork(k.Network); err != nil {
			return fmt.Errorf("network %q is not mainnet or testnet", k.Network)
		}
	}
	switch k.Scheme {
	case SchemeFrostEd25519:
		s, err := frost.ParseShare(k.Share)
		if err != nil {
			return fmt.Errorf("share: %w", err)
		}
		if s.ID() != frost.AgentID {
			return errors.New("share is the engine's share, not the agent's; the engine share goes to the engine only")
		}
	case SchemeECDSA2p:
		s, err := ecdsa2p.ParseKey(k.Share)
		if err != nil {
			return fmt.Errorf("share: %w", err)
		}
		if s.Role() != ecdsa2p.RoleAgent {
			return errors.New("share is the engine's key, not the agent's; the engine's goes to the engine's signer only")
		}
	default:
		return fmt.Errorf("scheme %q is not one this signer knows (%s, %s)", k.Scheme, SchemeFrostEd25519, SchemeECDSA2p)
	}
	return nil
}

// SchemeForVenue is the signing scheme a venue's keys run, decided by the
// venue's key type alone: Binance Ed25519 keys sign with FROST, Hyperliquid
// and Coinbase ECDSA keys with two-party ECDSA. False for a venue whose keys
// have no two-party scheme yet (Bybit RSA, OKX HMAC) or no trading keys
// (Fireblocks).
func SchemeForVenue(venue string) (string, bool) {
	switch api.Venue(venue) {
	case api.VenueBinance:
		return SchemeFrostEd25519, true
	case api.VenueHyperliquid, api.VenueCoinbase:
		return SchemeECDSA2p, true
	}
	return "", false
}

// SchemeOfShare reads which scheme a share file (as frost or ecdsa2p
// marshal it) belongs to.
func SchemeOfShare(share []byte) (string, error) {
	var head struct {
		Scheme string `json:"scheme"`
	}
	if err := json.Unmarshal(share, &head); err != nil {
		return "", errors.New("signer: the share is not the JSON mishmar keygen wrote")
	}
	switch head.Scheme {
	case frost.Scheme:
		return SchemeFrostEd25519, nil
	case ecdsa2p.Scheme:
		return SchemeECDSA2p, nil
	}
	return "", fmt.Errorf("signer: share scheme %q is not one this signer knows (%s, %s)", head.Scheme, frost.Scheme, ecdsa2p.Scheme)
}
