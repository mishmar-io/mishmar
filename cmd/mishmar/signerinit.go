package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/signer"
)

type signerInitOptions struct {
	config, engine, keyID, agent string
	secretStdin                  bool
}

func newSignerInitCmd() *cobra.Command {
	var o signerInitOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write the signer's config for an agent key issued in the console",
		Long: `Start a signer config from an agent key the console issued (the agent's
page: issue a key; the secret, msk_…, is shown once). The file holds the
engine URL, the key id and its secret, and the agent id; venue keys are
added next with "mishmar signer keygen" (one per trading account). "mishmar
add-agent" does all of this in one go with an operator token; init is for
an agent and key someone created in the console.

The secret is read from MISHMAR_AGENT_SECRET, or from standard input with
--secret-stdin, never from the command line, so it stays out of the shell's
history and the process list.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := configPath(o.config)
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s exists already; add venue keys to it with \"mishmar signer keygen\", or pass --config for another file", path)
			}
			secret := strings.TrimSpace(os.Getenv("MISHMAR_AGENT_SECRET"))
			if o.secretStdin {
				line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && line == "" {
					return fmt.Errorf("--secret-stdin: reading the secret: %w", err)
				}
				secret = strings.TrimSpace(line)
			}
			if secret == "" {
				return errors.New("no secret: set MISHMAR_AGENT_SECRET, or pipe it in with --secret-stdin")
			}
			cfg := &signer.Config{Engine: o.engine, KeyID: o.keyID, Secret: secret, AgentID: o.agent, VenueKeys: map[string]signer.VenueKey{}}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (mode 0600) for agent %s, key %s, engine %s\n", path, o.agent, o.keyID, o.engine)
			fmt.Fprint(cmd.OutOrStdout(), "next, one venue key per trading account, made with the engine's signer:\n"+
				"  mishmar signer keygen --venue-key vk_… --trading-account ta_… --venue binance\n"+
				"  mishmar signer keygen --venue-key vk_… --trading-account ta_… --venue hyperliquid --network mainnet|testnet\n"+
				"then: mishmar signer run\n")
			return nil
		},
	}
	f := cmd.Flags()
	configFlag(cmd, &o.config)
	f.StringVar(&o.engine, "engine", "", "the engine's API base URL, with /v1: https://engine.example/v1")
	f.StringVar(&o.keyID, "key-id", "", "the agent key's id (key_…), from the console")
	f.StringVar(&o.agent, "agent", "", "the agent the key belongs to (agt_…)")
	f.BoolVar(&o.secretStdin, "secret-stdin", false, "read the key's secret (msk_…) from standard input instead of MISHMAR_AGENT_SECRET")
	for _, name := range []string{"engine", "key-id", "agent"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
