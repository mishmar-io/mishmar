package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/mishmar-io/mishmar/signer"
)

// ensureConfig makes sure the signer has its config at path before it
// starts. With none there and the agent's API key in MISHMAR_API_KEY, the
// config is written from the key alone (signer.Setup: the engine and key id
// come from the key, the agent from the engine). With a config there, a key
// in the environment must be the config's own, so a stale environment
// cannot quietly make the signer act for another agent. With neither, the
// error says what to do.
func ensureConfig(ctx context.Context, path string, log *slog.Logger) error {
	apiKey := strings.TrimSpace(os.Getenv(signer.EnvAPIKey))
	_, statErr := os.Stat(path)
	switch {
	case statErr == nil && apiKey == "":
		return nil
	case statErr == nil:
		cfg, err := signer.Load(path)
		if err != nil {
			return err
		}
		if cfg.Secret != apiKey {
			return fmt.Errorf("%s holds another key than %s; remove the file to set the agent up again, or pass --config for another file", path, signer.EnvAPIKey)
		}
		return nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return statErr
	case apiKey == "":
		return fmt.Errorf("no config at %s: put the agent's API key from the console in %s and start again (the signer writes the config itself), or run \"mishmar signer init\"", path, signer.EnvAPIKey)
	}
	log.Info("signer: no config yet; setting the agent up from "+signer.EnvAPIKey, "path", path)
	cfg, me, err := signer.Setup(ctx, apiKey, os.Getenv(signer.EnvEngine))
	if err != nil {
		return err
	}
	if err := cfg.Save(path); err != nil {
		return err
	}
	log.Info("signer: config written", "path", path, "agent", me.AgentId, "name", me.AgentName, "keyId", me.KeyId, "engine", cfg.Engine)
	return nil
}
