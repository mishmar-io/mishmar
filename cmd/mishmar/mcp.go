package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/mcp"
	"github.com/mishmar-io/mishmar/signer"
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
stdout; this process logs to stderr only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := configPath(config)
			if err != nil {
				return err
			}
			cfg, err := signer.Load(path)
			if err != nil {
				return err
			}
			log, err := newLogger(level)
			if err != nil {
				return err
			}
			s, err := signer.New(cfg, nil, log)
			if err != nil {
				return err
			}
			s.CommitmentBatch = 8
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			errc := make(chan error, 2)
			go func() { errc <- s.Run(ctx) }()
			go func() { errc <- mcp.New(mcp.SignerBackend(s), version, log).Serve(ctx, os.Stdin, os.Stdout) }()
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
