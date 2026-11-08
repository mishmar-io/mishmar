package main

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/mcp"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/signer/configlock"
)

func newMCPCmd() *cobra.Command {
	var config, level string
	cmd := &cobra.Command{
		Use:     "mcp",
		GroupID: groupAgent,
		Short:   "Serve the agent's tools to a language model over MCP (stdio), the signer built in",
		Long: `An MCP server for one agent: a model gets tools to propose orders and
cancels, follow a decision parked for a person, and read positions, PnL,
approvals, the trading account and its venue rules. The agent's signer runs
inside this process with the config's key and shares, so the model never
sees a key, every order it proposes is checked by the engine's rules, and
nothing reaches a venue without both signatures.

Point an MCP client at it with the command "mishmar mcp" (and --config when
the config is not at the default path). The protocol runs on stdin and
stdout; this process logs to stderr only. Like "signer run", it holds the
config's lock while it runs, so it does not start next to another signer on
the same config, and it keeps the agent's trading keys in line with the
engine's (generating the ones the console adds). With no config at the path
yet, the agent's API key in MISHMAR_API_KEY (the client's env block) sets
the agent up; without that too, it still starts and lists its tools, and
every call says where the config was looked for.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := configPath(config)
			if err != nil {
				return err
			}
			log, err := newLogger(level)
			if err != nil {
				return err
			}
			if os.Getenv(signer.EnvAPIKey) != "" {
				// The agent's API key in the client's environment: the config
				// is written from it when there is none yet.
				if err := ensureConfig(cmd.Context(), path, log); err != nil {
					return err
				}
			} else if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
				// Before the agent exists, a client (or a directory's check) can
				// still start the server and see its tools; every call says why
				// it cannot run.
				log.Warn("mcp: no agent config, so every tool call fails until it exists", "config", path, "apiKey", signer.EnvAPIKey, "setup", mcp.SetupURL)
				return mcp.New(mcp.Unconfigured(path), version, log).Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
			}
			lock, err := configlock.Acquire(path)
			if err != nil {
				return err
			}
			defer lock.Close()
			cfg, err := signer.Load(path)
			if err != nil {
				return err
			}
			s, err := signer.New(cfg, nil, log)
			if err != nil {
				return err
			}
			s.ConfigPath = path
			s.CommitmentBatch = 8
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			errc := make(chan error, 2)
			go func() { errc <- s.Run(ctx) }()
			go func() {
				errc <- mcp.New(mcp.SignerBackend(s), version, log).Serve(ctx, cmd.InOrStdin(), cmd.OutOrStdout())
			}()
			err = <-errc
			cancel()
			<-errc
			return err
		},
	}
	configFlag(cmd, &config)
	cmd.Flags().StringVar(&level, "log-level", "info", "debug | info | warn | error (stderr)")
	return cmd
}
