package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/proposal"
	"github.com/mishmar-io/mishmar/signer"
)

// proposeOptions are the flags of "mishmar propose".
type proposeOptions struct {
	engineURL, keyID, config, tradingAccount, kind, symbol, side, size, price, leverage, cancel string
	market, reduceOnly, channel                                                                 bool
	validFor                                                                                    time.Duration
	engineSet                                                                                   bool
}

func newProposeCmd() *cobra.Command {
	var o proposeOptions
	cmd := &cobra.Command{
		Use:     "propose",
		GroupID: groupAgent,
		Short:   "Sign one proposal with the agent's secret and submit it to the engine",
		Long: `Do what an agent SDK does for one order: build the proposal, sign it with
the agent's Ed25519 key, submit it to POST /v1/proposals and print the
decision with every rule's evaluation, the signer's outcome and the venue's
acknowledgement.

The key comes from --config (the signer's config: its keyId, secret and
engine), or from --key with the secret in MISHMAR_AGENT_SECRET.

With --config, an order or a cancel on a Binance trading account goes
one-shot: this command builds the venue's own request from the engine's
rules for the symbol and signs the agent's half of it with the share in the
config, so the engine can forward an allowed order without asking the
signer. Anything else, or --channel, leaves the venue request to the engine,
which asks the agent's signer for its half: "mishmar signer run" must be
running then, and for any order that waits for a person. A market order is
priced at the engine's mark. --cancel pulls a resting order this agent
placed, by the clientOrderId its decision carries. --valid-for is the
deadline the agent sets: an order still waiting for a person past it
expires, and nothing is forwarded after it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.engineSet = cmd.Flags().Changed("engine")
			switch {
			case o.validFor <= 0 || o.validFor > 24*time.Hour:
				return errors.New("--valid-for must be between 1s and 24h")
			case o.cancel != "" && (o.market || o.price != "" || o.size != "" || o.reduceOnly || o.leverage != ""):
				return errors.New("--cancel takes --trading-account and --symbol only, no order fields")
			case o.cancel != "" && (o.tradingAccount == "" || o.symbol == ""):
				return errors.New("--cancel needs --trading-account and --symbol")
			case o.cancel != "":
			case o.market == (o.price != ""):
				return errors.New("give exactly one of --market or --price")
			case o.tradingAccount == "" || o.symbol == "" || o.size == "":
				return errors.New("--trading-account, --symbol and --size are required")
			}
			return propose(cmd.Context(), o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.engineURL, "engine", "http://localhost:8080/v1", "engine API base URL (the config's when --config is given)")
	f.StringVar(&o.keyID, "key", "", "the agent key id (key_…); the secret comes from MISHMAR_AGENT_SECRET")
	configFlag(cmd, &o.config)
	f.StringVar(&o.tradingAccount, "trading-account", "", "trading account id (ta_…)")
	f.StringVar(&o.kind, "kind", "order", "order | transaction | signed_message")
	f.StringVar(&o.symbol, "symbol", "", "venue symbol, e.g. BTC on Hyperliquid, BTCUSDT on Binance")
	f.StringVar(&o.side, "side", "buy", "buy | sell")
	f.StringVar(&o.size, "size", "", "quantity, decimal string")
	f.StringVar(&o.price, "price", "", "limit price, decimal string (omit with --market)")
	f.BoolVar(&o.market, "market", false, "market order, priced at the engine's mark")
	f.StringVar(&o.leverage, "leverage", "", "leverage, decimal string (optional)")
	f.BoolVar(&o.reduceOnly, "reduce-only", false, "the order may only reduce the position")
	f.DurationVar(&o.validFor, "valid-for", time.Minute, "the agent's deadline for the order; at most 24h")
	f.StringVar(&o.cancel, "cancel", "", "pull the resting order with this clientOrderId (msh-…) instead of placing one")
	f.BoolVar(&o.channel, "channel", false, "leave the venue request to the engine and the running signer, even where one-shot is possible")
	return cmd
}

// propose builds, signs and submits one proposal and prints the decision.
func propose(ctx context.Context, o proposeOptions, out io.Writer) error {
	engine, keyID, secret := o.engineURL, o.keyID, os.Getenv("MISHMAR_AGENT_SECRET")
	var cfg *signer.Config
	if o.config != "" {
		var err error
		if cfg, err = signer.Load(o.config); err != nil {
			return err
		}
		secret = cfg.Secret
		if keyID == "" {
			keyID = cfg.KeyID
		}
		if !o.engineSet {
			engine = cfg.Engine
		}
	}
	switch {
	case keyID == "":
		return errors.New("--key is required (or --config)")
	case secret == "":
		return errors.New("MISHMAR_AGENT_SECRET is required (the agent's one-time secret, msk_…), or --config")
	}
	key, err := agentkey.Parse(secret)
	if err != nil {
		return err
	}
	nonce, err := proposal.NewNonce()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	submit := api.ProposalSubmit{
		KeyId: keyID, TradingAccountId: o.tradingAccount, Kind: api.ProposalKind(o.kind), Symbol: o.symbol,
		Side: api.Side(o.side), Size: o.size, ReduceOnly: &o.reduceOnly,
		OrderType: api.ProposalSubmitOrderTypeLimit, Nonce: nonce, ProposedAt: now, ExpiresAt: now.Add(o.validFor),
	}
	switch {
	case o.cancel != "":
		submit = api.ProposalSubmit{
			KeyId: keyID, TradingAccountId: o.tradingAccount, Kind: api.ProposalKindCancel, Symbol: o.symbol,
			TargetClientOrderId: &o.cancel, Nonce: nonce, ProposedAt: now, ExpiresAt: now.Add(o.validFor),
		}
	case o.market:
		submit.OrderType = api.ProposalSubmitOrderTypeMarket
	default:
		submit.Price = &o.price
	}
	if o.leverage != "" {
		submit.Leverage = &o.leverage
	}
	client := signer.NewClient(engine, keyID, key)
	var d api.Decision
	if cfg != nil && !o.channel {
		cfg.Engine = engine
		s, err := signer.New(cfg, client, slog.New(slog.NewTextHandler(os.Stderr, nil)))
		if err != nil {
			return err
		}
		d, err = s.Propose(ctx, submit)
		if err != nil {
			return err
		}
	} else {
		d, err = client.Propose(ctx, submit)
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "decision %s: %s", d.Id, d.Verdict)
	if d.Reason != nil && *d.Reason != "" {
		fmt.Fprintf(out, " (%s)", *d.Reason)
	}
	what := fmt.Sprintf("%s %s %s on %s, notional %s", d.Proposal.Side, d.Proposal.Size, d.Proposal.Symbol, d.Proposal.Venue, d.Proposal.NotionalUsd)
	if d.Proposal.Kind == api.ProposalKindCancel && d.Proposal.TargetClientOrderId != nil {
		what = fmt.Sprintf("cancel of %s (%s %s %s on %s)", *d.Proposal.TargetClientOrderId, d.Proposal.Side, d.Proposal.Size, d.Proposal.Symbol, d.Proposal.Venue)
	}
	fmt.Fprintf(out, "\n  %s, latency %.2f ms, revision %d\n", what, d.LatencyMs, d.ConfigRevision)
	if d.Exit != nil && *d.Exit {
		fmt.Fprintln(out, "  exit: rules that do not apply to exits are waived")
	}
	for _, e := range d.Evaluations {
		mark := "passed"
		switch {
		case e.Waived != nil && *e.Waived:
			mark = "waived"
		case !e.Passed:
			mark = "BREACHED"
		}
		fmt.Fprintf(out, "  %-7s %-36s %s observed %s, limit %s\n", mark, e.RuleName, e.Metric, e.Observed, e.Threshold)
	}
	if d.Signing != nil {
		fmt.Fprintf(out, "  signing: %s", d.Signing.Status)
		if d.Signing.Detail != nil && *d.Signing.Detail != "" {
			fmt.Fprintf(out, " (%s)", *d.Signing.Detail)
		}
		fmt.Fprintln(out)
	}
	if d.VenueAck != nil {
		fmt.Fprintf(out, "  venue: %s order %s at %s\n", d.VenueAck.Status, d.VenueAck.OrderId, d.VenueAck.ReceivedAt.Format(time.RFC3339))
	}
	if d.ApprovalId != nil {
		fmt.Fprintf(out, "  parked for approval %s\n", *d.ApprovalId)
	}
	return nil
}
