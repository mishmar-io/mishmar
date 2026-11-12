package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/signer"
)

type signerInitOptions struct {
	config, engine, keyID, agent string
	secretStdin, noCheck         bool
}

// initCheckTimeout bounds init's one call to the engine: longer than the 45 s
// a hosted account that stopped takes to wake for a signed agent call.
const initCheckTimeout = 60 * time.Second

func newSignerInitCmd() *cobra.Command {
	var o signerInitOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write the signer's config for an agent key issued in the console",
		Long: `Write a signer config from an agent key the console issued (the agent's
page: issue a key; the secret, msk_…, is shown once). The file holds the
engine URL, the key id and its secret, and the agent id. "mishmar signer
run" does this by itself when it finds no config and MISHMAR_API_KEY is
set, and then generates the trading keys the console adds; init is the
explicit step for anyone who wants to see the config written first.

A key issued at contract 0.19.0 or later names its engine and key id, so
init needs no flags: the agent is read from the engine. A key from before
takes --engine, --key-id and --agent. MISHMAR_ENGINE, or --engine, names a
self-hosted engine's API base URL, with /v1, when the key does not name a
hosted one.

The secret is read from MISHMAR_API_KEY (or MISHMAR_AGENT_SECRET), or from
standard input with --secret-stdin, never from the command line, so it
stays out of the shell's history and the process list.

Before writing, init makes one signed read with the key, so a mistyped
secret or key id shows here rather than later: a key the engine refuses
writes nothing. --no-check skips the read; it then needs --agent.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := configPath(o.config)
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s exists already; run \"mishmar signer run\" with it, or pass --config for another file", path)
			}
			secret := strings.TrimSpace(os.Getenv(signer.EnvAPIKey))
			if secret == "" {
				secret = strings.TrimSpace(os.Getenv("MISHMAR_AGENT_SECRET"))
			}
			if o.secretStdin {
				line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && line == "" {
					return fmt.Errorf("--secret-stdin: reading the secret: %w", err)
				}
				secret = strings.TrimSpace(line)
			}
			if secret == "" {
				return errors.New("no secret: set " + signer.EnvAPIKey + ", or pipe it in with --secret-stdin")
			}
			parsed, err := agentkey.ParseSecret(secret)
			if err != nil {
				return err
			}
			if o.engine == "" {
				if o.engine, err = signer.EngineURL(parsed, os.Getenv(signer.EnvEngine)); err != nil {
					return fmt.Errorf("%w (or pass --engine)", err)
				}
			}
			if o.keyID == "" {
				if parsed.KeyID == "" {
					return errors.New("--key-id is required: the key was issued before 0.19.0 and does not name it")
				}
				o.keyID = parsed.KeyID
			}
			cfg := &signer.Config{Engine: o.engine, KeyID: o.keyID, Secret: secret, AgentID: o.agent, VenueKeys: map[string]signer.VenueKey{}}
			if err := cfg.Validate(); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case o.noCheck && o.agent == "":
				return errors.New("--agent is required with --no-check: the agent is otherwise read from the engine")
			case o.noCheck:
			case o.agent == "":
				// The agent from the engine, which proves the key at the same time.
				fmt.Fprintf(out, "checking key %s with %s…\n", cfg.KeyID, cfg.Engine)
				setup, me, err := signer.Setup(cmd.Context(), secret, cfg.Engine)
				if err != nil {
					return fmt.Errorf("%w; nothing written", err)
				}
				if setup.KeyID != cfg.KeyID {
					return fmt.Errorf("--key-id %s is not the key's id %s; nothing written", cfg.KeyID, setup.KeyID)
				}
				cfg.AgentID = me.AgentId
				fmt.Fprintf(out, "the engine accepts key %s: agent %s (%s)\n", cfg.KeyID, me.AgentId, me.AgentName)
			default:
				if err := checkAgentKey(cmd.Context(), cfg, out); err != nil {
					return err
				}
			}
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(out, "wrote %s (mode 0600) for agent %s, key %s, engine %s\n", path, cfg.AgentID, cfg.KeyID, cfg.Engine)
			fmt.Fprint(out, "next: mishmar signer run\n"+
				"  it generates the trading keys the console adds for this agent and logs what to approve or register at the venue;\n"+
				"  or make one by hand: mishmar signer keygen --venue-key vk_… --connected-account ta_… --venue binance|hyperliquid [--network mainnet|testnet]\n")
			return nil
		},
	}
	f := cmd.Flags()
	configFlag(cmd, &o.config)
	f.StringVar(&o.engine, "engine", "", "the engine's API base URL, with /v1 (default: what the key names, or "+signer.EnvEngine+")")
	f.StringVar(&o.keyID, "key-id", "", "the agent key's id (key_…); default: what the key names")
	f.StringVar(&o.agent, "agent", "", "the agent the key belongs to (agt_…); default: read from the engine")
	f.BoolVar(&o.secretStdin, "secret-stdin", false, "read the key (msk_…) from standard input instead of "+signer.EnvAPIKey)
	f.BoolVar(&o.noCheck, "no-check", false, "write the config without checking the key with the engine first (needs --agent)")
	return cmd
}

// checkAgentKey reads the agent's venue keys with the config's key: the
// engine answers only a key it knows, signed with the matching secret. A
// refusal is an error, and so is a key of another agent than --agent (seen
// once the agent holds a venue key). An engine that does not answer, is
// unavailable, or has no such path is a warning: the config is still written.
func checkAgentKey(ctx context.Context, cfg *signer.Config, out io.Writer) error {
	key, err := cfg.Key()
	if err != nil {
		return err
	}
	client := signer.NewClient(cfg.Engine, cfg.KeyID, key)
	client.Timeout = initCheckTimeout
	fmt.Fprintf(out, "checking key %s with %s…\n", cfg.KeyID, cfg.Engine)
	keys, err := client.VenueKeys(ctx)
	var refused *signer.Error
	switch {
	case err == nil:
	case errors.As(err, &refused) && refused.Status == http.StatusUnauthorized:
		return fmt.Errorf("the engine does not accept key %s (%s): check the key id and the secret against the console, or revoke the key there and issue another; nothing written", cfg.KeyID, refused.Message)
	case errors.As(err, &refused) && refused.Status == http.StatusNotFound:
		// A wrong URL, or an engine older than GET /agent/venue-keys: not proof the key is wrong.
		fmt.Fprintf(out, "could not check the key: %s has no GET /agent/venue-keys (%s); --engine is the API base URL, with /v1; writing the config anyway\n", cfg.Engine, refused.Message)
		return nil
	case errors.As(err, &refused) && refused.Status < 500 && refused.Status != http.StatusTooManyRequests:
		return fmt.Errorf("checking key %s: %w; nothing written", cfg.KeyID, err)
	default:
		fmt.Fprintf(out, "could not check the key with the engine (%v); writing the config anyway\n", err)
		return nil
	}
	for _, k := range keys {
		if k.AgentId != cfg.AgentID {
			return fmt.Errorf("key %s is agent %s's, not %s: pass --agent %s; nothing written", cfg.KeyID, k.AgentId, cfg.AgentID, k.AgentId)
		}
	}
	fmt.Fprintf(out, "the engine accepts key %s\n", cfg.KeyID)
	return nil
}
