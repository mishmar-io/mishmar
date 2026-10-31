// Command mishmar is the agent-side command line of Mishmar: it deals the
// two-party venue keys, sets an agent up on an engine, runs the agent's
// signer, and proposes an order the way an SDK would. `mishmar --help` lists
// the commands by what they are for; `mishmar <command> --help` says what
// each one needs.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/signer"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "mishmar:", err)
		os.Exit(1)
	}
}

// Command groups, by what the command is for.
const (
	groupSetup  = "setup"
	groupSigner = "signer"
	groupAgent  = "agent"
	groupTools  = "tools"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mishmar",
		Short: "The agent side of Mishmar: venue keys, the signer, proposals",
		Long: `Mishmar puts a policy engine between a trading agent and the venue. The
agent never holds a venue key whole: every trading key is dealt in two
shares on the owner's machine, one for the engine and one for the agent's
signer, and an order reaches the venue only when both have signed it. The
engine checks the order against the account's rules; the signer checks that
it is the order the agent proposed.

This binary is the agent's side:

  keygen       deal a two-party venue key (the engine's share and the agent's)
  add-agent    create an agent on an engine, issue its key, deal a venue key
               per trading account, and write the signer's config
  signer       add a share to the signer's config, or run the signer
  propose      sign one proposal with the agent's key and submit it
  mcp          serve the agent's tools to a language model over MCP, the
               signer built in

The signer's config (the agent's secret and its shares) lives in
~/.mishmar/agent.json, or the file MISHMAR_AGENT_CONFIG names, mode 0600.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddGroup(
		&cobra.Group{ID: groupSetup, Title: "Setup (on the owner's machine, with an operator token):"},
		&cobra.Group{ID: groupSigner, Title: "The signer (next to the agent, with its config):"},
		&cobra.Group{ID: groupAgent, Title: "Agent commands (with the agent's secret, as an SDK would):"},
		&cobra.Group{ID: groupTools, Title: "Tools:"},
	)
	root.SetHelpCommandGroupID(groupTools)
	root.SetCompletionCommandGroupID(groupTools)
	root.AddCommand(
		newKeygenCmd(),
		newAddAgentCmd(),
		newSignerCmd(),
		newProposeCmd(),
		newMCPCmd(),
		newVersionCmd(),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		GroupID: groupTools,
		Short:   "Print the version",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			v := version
			if v == "dev" {
				if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
					v = bi.Main.Version
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "mishmar %s (%s %s/%s)\n", v, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}

// configFlag adds --config to a command: the signer's config file, by
// default what signer.DefaultPath says.
func configFlag(cmd *cobra.Command, path *string) {
	cmd.Flags().StringVar(path, "config", "", "the signer's config file (default $"+signer.EnvConfig+" or ~/.mishmar/agent.json)")
}

// configPath resolves --config.
func configPath(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	return signer.DefaultPath()
}

// writeSecretFile writes key material to a new file with mode 0600; an
// existing file is never overwritten.
func writeSecretFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s exists already; it is not overwritten. Choose another file", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func newLogger(level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("--log-level %q: use debug, info, warn or error", level)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})), nil
}
