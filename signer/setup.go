package signer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
)

// The environment of a signer that is given the agent's API key and nothing
// else: EnvAPIKey holds the key (msk_…), which names the engine and the key
// id; EnvEngine overrides the engine's API base URL, for a self-hosted
// engine or one the key's environment does not name.
const (
	EnvAPIKey = "MISHMAR_API_KEY"
	EnvEngine = "MISHMAR_ENGINE"
)

// SetupTimeout bounds the one call Setup makes to the engine: longer than
// the 45 s a hosted account that stopped takes to wake for a signed agent call.
const SetupTimeout = 60 * time.Second

// HostedAPI is the API host of each hosted environment a key can name. An
// account's engine is reached under /a/<account id>/v1 on it.
var HostedAPI = map[string]string{
	"testnet": "https://api.testnet.mishmar.io",
	"stage":   "https://api.stage.mishmar.io",
	"prod":    "https://api.mishmar.io",
}

// EngineURL is the API base URL a key's engine is reached at: override when
// given (trailing slashes dropped), else the hosted host the key's
// environment names, plus the account's path. A key that names no engine
// (issued before 0.19.0), or an environment that is not hosted (a
// self-hosted engine's), needs the override.
func EngineURL(p agentkey.Parsed, override string) (string, error) {
	if override = strings.TrimSpace(override); override != "" {
		return strings.TrimRight(override, "/"), nil
	}
	if p.KeyID == "" {
		return "", errors.New("signer: the key was issued before 0.19.0 and names no engine; set " + EnvEngine + " to the engine's API base URL, with /v1")
	}
	host, ok := HostedAPI[p.Environment]
	if !ok {
		return "", fmt.Errorf("signer: the key names environment %q, which is not a hosted one; set %s to the engine's API base URL, with /v1", p.Environment, EnvEngine)
	}
	return host + "/a/" + p.AccountID + "/v1", nil
}

// Setup makes a signer's config from the agent's API key alone: the engine
// from what the key names (or engine, when given), the key id from the key,
// and the agent from GET /agent/me, which also proves the key before
// anything is written. The config has no trading keys yet; the running
// signer generates them (SyncKeys).
func Setup(ctx context.Context, apiKey, engine string) (*Config, api.AgentIdentity, error) {
	p, err := agentkey.ParseSecret(apiKey)
	if err != nil {
		return nil, api.AgentIdentity{}, err
	}
	if p.KeyID == "" {
		return nil, api.AgentIdentity{}, errors.New("signer: the key was issued before 0.19.0 and names no key id; write the config with \"mishmar signer init --engine … --key-id … --agent …\"")
	}
	url, err := EngineURL(p, engine)
	if err != nil {
		return nil, api.AgentIdentity{}, err
	}
	c := NewClient(url, p.KeyID, p.Key)
	c.Timeout = SetupTimeout
	me, err := c.Me(ctx)
	if err != nil {
		var refused *Error
		switch {
		case errors.As(err, &refused) && refused.Status == http.StatusUnauthorized:
			return nil, api.AgentIdentity{}, fmt.Errorf("signer: %s does not accept this key (%s): it may have been revoked; issue a new one in the console", url, refused.Message)
		case errors.As(err, &refused) && refused.Status == http.StatusNotFound:
			return nil, api.AgentIdentity{}, fmt.Errorf("signer: %s has no GET /agent/me: the engine is older than contract 0.19.0, or the URL is wrong", url)
		default:
			return nil, api.AgentIdentity{}, fmt.Errorf("signer: checking the key with %s: %w", url, err)
		}
	}
	if me.KeyId != p.KeyID {
		return nil, api.AgentIdentity{}, fmt.Errorf("signer: %s answered for key %s, not %s", url, me.KeyId, p.KeyID)
	}
	cfg := &Config{Engine: url, KeyID: p.KeyID, Secret: strings.TrimSpace(apiKey), AgentID: me.AgentId, VenueKeys: map[string]VenueKey{}}
	if err := cfg.Validate(); err != nil {
		return nil, api.AgentIdentity{}, err
	}
	return cfg, me, nil
}
