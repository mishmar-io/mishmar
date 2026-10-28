package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/signer"
)

func newSignerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "signer",
		GroupID: groupSigner,
		Short:   "The agent's signer: add a venue key share to its config, or run it",
		Long: `The signer holds the agent's identity key and the agent's share of each
venue key, long-polls the engine for venue requests to co-sign, checks
each one against the order the agent proposed, and answers with its share
of the signature or a refusal. Its config is ~/.mishmar/agent.json (or
MISHMAR_AGENT_CONFIG), written by "mishmar add-agent" or by hand.`,
	}
	cmd.AddCommand(newSignerAddKeyCmd(), newSignerRunCmd())
	return cmd
}

type addKeyOptions struct {
	config, venueKey, tradingAccount, venue, share, agent string
}

func newSignerAddKeyCmd() *cobra.Command {
	var o addKeyOptions
	cmd := &cobra.Command{
		Use:   "add-key",
		Short: "Store the agent's share of a venue key in the signer's config",
		Long: `Store the agent's share that "mishmar keygen" wrote under the venue key
id the engine gave when the engine's share was added to the agent. The
scheme is read from the share file and must fit the venue (Binance:
frost-ed25519; Hyperliquid, Coinbase: ecdsa-2p). An engine share is
refused: it never belongs in this file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := configPath(o.config)
			if err != nil {
				return err
			}
			cfg, err := signer.Load(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("no config at %s: run \"mishmar add-agent\", or write the file by hand (README, \"The signer\")", path)
				}
				return err
			}
			share, err := os.ReadFile(o.share)
			if err != nil {
				return fmt.Errorf("share: %w", err)
			}
			scheme, err := signer.SchemeOfShare(share)
			if err != nil {
				return err
			}
			if err := cfg.AddVenueKey(o.venueKey, signer.VenueKey{TradingAccountID: o.tradingAccount, Venue: o.venue, Scheme: scheme, Share: share}); err != nil {
				return err
			}
			switch {
			case o.agent == "":
			case cfg.AgentID == "":
				cfg.AgentID = o.agent
			case cfg.AgentID != o.agent:
				return fmt.Errorf("the config is agent %s's, not %s's", cfg.AgentID, o.agent)
			}
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stored the agent's share of %s (%s on trading account %s, %s) in %s\n", o.venueKey, o.venue, o.tradingAccount, scheme, path)
			return nil
		},
	}
	f := cmd.Flags()
	configFlag(cmd, &o.config)
	f.StringVar(&o.venueKey, "venue-key", "", "the venue key id the engine returned (vk_…)")
	f.StringVar(&o.tradingAccount, "trading-account", "", "the trading account the key is on (ta_…)")
	f.StringVar(&o.venue, "venue", "", "the trading account's venue: binance, hyperliquid, coinbase")
	f.StringVar(&o.share, "share", "", "the agent share file mishmar keygen wrote")
	f.StringVar(&o.agent, "agent", "", "the agent the key belongs to (agt_…), stored in the config when it names none; one-shot signing needs it")
	for _, name := range []string{"venue-key", "trading-account", "venue", "share"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func newSignerRunCmd() *cobra.Command {
	var config, level string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the signer until SIGINT or SIGTERM",
		Long: `Poll the engine for requests addressed to this agent and answer them: an
order is signed only when it is the one the agent proposed (the proposal in
the request is signed by this agent's key, for this trading account, and the venue
request decodes to that order and nothing more); a verify request is signed
when it decodes to a read and not an order. Everything else is refused with
a reason the engine records on the decision. One line is logged per request.`,
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
			log.Info("signer: config loaded", "path", path, "hook", cfg.Hook != "")
			return s.Run(cmd.Context())
		},
	}
	configFlag(cmd, &config)
	cmd.Flags().StringVar(&level, "log-level", "info", "debug | info | warn | error")
	return cmd
}
