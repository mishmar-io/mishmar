package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/signer"
)

// readOptions are the flags every "mishmar read" command shares.
type readOptions struct {
	config, engine string
	json           bool
}

func newReadCmd() *cobra.Command {
	var o readOptions
	cmd := &cobra.Command{
		Use:     "read",
		GroupID: groupAgent,
		Short:   "Read the connected accounts the agent was given: balances, positions, transactions, value over time",
		Long: `Read connected-account data as the agent: the accounts it was given, and
each one's balances, the venue's positions, its transactions and its value
over time. An agent reads each account it was given whole, other agents'
and the owner's activity included; every read is signed with the agent's
key and written to the account's access log, which its owner sees.

These are the reads a read agent is for, and on hosted Mishmar they are
answered while the account's engine sleeps, so they never wake it.

The key comes from MISHMAR_API_KEY (the agent's Mishmar API key from the
console, which names its engine; MISHMAR_ENGINE or --engine for a
self-hosted one), or from the signer's config (--config, or the default
config when MISHMAR_API_KEY is not set). Times are RFC 3339
(2026-11-12T09:30:00Z) or a date (2026-11-12). --json prints the engine's
answer as it came.`,
	}
	f := cmd.PersistentFlags()
	f.StringVar(&o.config, "config", "", "the signer's config file to take the key from (default $"+signer.EnvConfig+" or ~/.mishmar/agent.json, when "+signer.EnvAPIKey+" is not set)")
	f.StringVar(&o.engine, "engine", "", "engine API base URL with /v1 (default: $"+signer.EnvEngine+" or the one the key names; with a config, the config's)")
	f.BoolVar(&o.json, "json", false, "print the engine's answer as JSON")
	cmd.AddCommand(
		newReadAgentCmd(&o),
		newReadAccountsCmd(&o),
		newReadAccountCmd(&o),
		newReadBalancesCmd(&o),
		newReadPositionsCmd(&o),
		newReadTransactionsCmd(&o),
		newReadEquityCmd(&o),
	)
	return cmd
}

func newReadAgentCmd(o *readOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "agent",
		Short: "Who the key belongs to: the agent, its account and portfolio",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := readClient(*o)
			if err != nil {
				return err
			}
			me, err := c.Me(cmd.Context())
			if err != nil {
				return err
			}
			return o.print(cmd.OutOrStdout(), me, func(w io.Writer) {
				fmt.Fprintf(w, "agent %s (%s)\n  account %s, portfolio %s, key %s\n", me.AgentId, me.AgentName, me.AccountId, me.PortfolioId, me.KeyId)
			})
		},
	}
}

func newReadAccountsCmd(o *readOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "accounts",
		Short: "The connected accounts the agent was given",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := readClient(*o)
			if err != nil {
				return err
			}
			accounts, err := c.ConnectedAccounts(cmd.Context())
			if err != nil {
				return err
			}
			return o.print(cmd.OutOrStdout(), accounts, func(w io.Writer) {
				if len(accounts) == 0 {
					fmt.Fprintln(w, "no connected accounts: the agent was given none yet (its owner gives them on the agent's page in the console)")
					return
				}
				for _, a := range accounts {
					printAccount(w, a)
				}
			})
		},
	}
}

func newReadAccountCmd(o *readOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "account <connected account>",
		Short: "One connected account the agent was given, its setup without any secret",
		Args:  connectedAccountArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := readClient(*o)
			if err != nil {
				return err
			}
			a, err := c.ConnectedAccount(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return o.print(cmd.OutOrStdout(), a, func(w io.Writer) { printAccount(w, a) })
		},
	}
}

func newReadBalancesCmd(o *readOptions) *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   "balances <connected account>",
		Short: "The account's balances at the venue, each with its value in USD, now or at a past moment",
		Args:  connectedAccountArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			when, err := readTime("--at", at)
			if err != nil {
				return err
			}
			c, err := readClient(*o)
			if err != nil {
				return err
			}
			b, err := c.AccountBalances(cmd.Context(), args[0], when)
			if err != nil {
				return err
			}
			return o.print(cmd.OutOrStdout(), b, func(w io.Writer) {
				fmt.Fprintf(w, "%s at %s (%s)", b.ConnectedAccountId, stampOf(b.AsOf), b.Source)
				if b.EquityUsd != nil {
					fmt.Fprintf(w, ", equity %s USD", *b.EquityUsd)
				}
				fmt.Fprintln(w)
				if len(b.Balances) == 0 {
					fmt.Fprintln(w, "  no balances")
					return
				}
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				for _, x := range b.Balances {
					fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", x.Wallet, x.Asset, x.Amount, usd(x.UsdValue))
				}
				tw.Flush()
			})
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "the moment to read, RFC 3339 or a date (default now)")
	return cmd
}

func newReadPositionsCmd(o *readOptions) *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   "positions <connected account>",
		Short: "The venue's net positions on the account, whoever placed them, now or at a past moment",
		Args:  connectedAccountArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			when, err := readTime("--at", at)
			if err != nil {
				return err
			}
			c, err := readClient(*o)
			if err != nil {
				return err
			}
			p, err := c.AccountPositions(cmd.Context(), args[0], when)
			if err != nil {
				return err
			}
			return o.print(cmd.OutOrStdout(), p, func(w io.Writer) {
				fmt.Fprintf(w, "%s at %s (%s)\n", p.ConnectedAccountId, stampOf(p.AsOf), p.Source)
				if len(p.Positions) == 0 {
					fmt.Fprintln(w, "  no open positions")
					return
				}
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				for _, x := range p.Positions {
					fmt.Fprintf(tw, "  %s\t%s\t%s\tentry %s", x.Side, x.Symbol, x.Size, x.EntryPrice)
					if x.MarkPrice != nil {
						fmt.Fprintf(tw, "\tmark %s", *x.MarkPrice)
					}
					if x.NotionalUsd != nil {
						fmt.Fprintf(tw, "\tnotional %s USD", *x.NotionalUsd)
					}
					if x.UnrealizedUsd != nil {
						fmt.Fprintf(tw, "\tunrealised %s USD", *x.UnrealizedUsd)
					}
					if x.Leverage != nil {
						fmt.Fprintf(tw, "\tleverage %s", *x.Leverage)
					}
					fmt.Fprintln(tw)
				}
				tw.Flush()
			})
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "the moment to read, RFC 3339 or a date (default now)")
	return cmd
}

func newReadTransactionsCmd(o *readOptions) *cobra.Command {
	var from, to, kind, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "transactions <connected account>",
		Short: "Trades, transfers, funding and fees on the account, newest first, with who placed each trade",
		Long: `The account's transactions as the venue reports them, newest first. A
trade's last column says who placed it: "this agent", another agent by name
(through Mishmar), or "outside Mishmar" (the owner by hand, another program
or the venue). A longer history comes a page at a time: the last line names
the --cursor of the next page.`,
		Args: connectedAccountArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := signer.TransactionQuery{Kind: kind, Cursor: cursor, Limit: limit}
			var err error
			if q.From, err = readTime("--from", from); err != nil {
				return err
			}
			if q.To, err = readTime("--to", to); err != nil {
				return err
			}
			if limit < 0 || limit > 500 {
				return errors.New("--limit must be 1 to 500")
			}
			c, err := readClient(*o)
			if err != nil {
				return err
			}
			page, err := c.AccountTransactions(cmd.Context(), args[0], q)
			if err != nil {
				return err
			}
			return o.print(cmd.OutOrStdout(), page, func(w io.Writer) {
				if len(page.Items) == 0 {
					fmt.Fprintln(w, "no transactions")
				}
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				for _, x := range page.Items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", stampOf(x.At), x.Kind, transactionWhat(x), originOf(x.Origin))
				}
				tw.Flush()
				if page.NextCursor != nil && *page.NextCursor != "" {
					fmt.Fprintf(w, "more: --cursor %s\n", *page.NextCursor)
				}
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&from, "from", "", "the oldest moment, RFC 3339 or a date")
	f.StringVar(&to, "to", "", "the newest moment, RFC 3339 or a date")
	f.StringVar(&kind, "kind", "", "only this kind: trade, transfer_in, transfer_out, funding, fee, liquidation, rebate, other")
	f.IntVar(&limit, "limit", 0, "how many, 1 to 500 (default the engine's, 100)")
	f.StringVar(&cursor, "cursor", "", "the page after the one that named this cursor")
	return cmd
}

func newReadEquityCmd(o *readOptions) *cobra.Command {
	var from, to, resolution string
	cmd := &cobra.Command{
		Use:   "equity <connected account>",
		Short: "The account's value in USD over time, oldest first",
		Args:  connectedAccountArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := signer.EquityQuery{Resolution: resolution}
			var err error
			if q.From, err = readTime("--from", from); err != nil {
				return err
			}
			if q.To, err = readTime("--to", to); err != nil {
				return err
			}
			switch resolution {
			case "", "minute", "hour", "day":
			default:
				return fmt.Errorf("--resolution %q: use minute, hour or day", resolution)
			}
			c, err := readClient(*o)
			if err != nil {
				return err
			}
			series, err := c.AccountEquity(cmd.Context(), args[0], q)
			if err != nil {
				return err
			}
			return o.print(cmd.OutOrStdout(), series, func(w io.Writer) {
				fmt.Fprintf(w, "%s per %s, %d points\n", series.ConnectedAccountId, series.Resolution, len(series.Points))
				for _, p := range series.Points {
					fmt.Fprintf(w, "  %s  %s USD", stampOf(p.At), p.EquityUsd)
					if p.Source != api.SnapshotSourceObserved {
						fmt.Fprintf(w, " (%s)", p.Source)
					}
					fmt.Fprintln(w)
				}
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&from, "from", "", "the start, RFC 3339 or a date")
	f.StringVar(&to, "to", "", "the end, RFC 3339 or a date")
	f.StringVar(&resolution, "resolution", "", "how far apart the points are: minute, hour or day (default: the engine picks a few hundred points)")
	return cmd
}

// readClient is the agent's client for the reads: the key from --config, or
// from MISHMAR_API_KEY, or from the default config when that is not set.
func readClient(o readOptions) (*signer.Client, error) {
	engine := strings.TrimSpace(o.engine)
	apiKey := strings.TrimSpace(os.Getenv(signer.EnvAPIKey))
	if o.config == "" && apiKey != "" {
		p, err := agentkey.ParseSecret(apiKey)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", signer.EnvAPIKey, err)
		}
		if p.KeyID == "" {
			return nil, fmt.Errorf("%s was issued before 0.19.0 and names no key id: pass --config with the signer's config instead", signer.EnvAPIKey)
		}
		if engine == "" {
			engine = os.Getenv(signer.EnvEngine)
		}
		url, err := signer.EngineURL(p, engine)
		if err != nil {
			return nil, err
		}
		return signer.NewClient(url, p.KeyID, p.Key), nil
	}
	path, err := configPath(o.config)
	if err != nil {
		return nil, err
	}
	cfg, err := signer.Load(path)
	if err != nil {
		if o.config == "" && errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no agent key: put the agent's Mishmar API key from the console in %s, or pass --config with the signer's config (none at %s)", signer.EnvAPIKey, path)
		}
		return nil, err
	}
	key, err := cfg.Key()
	if err != nil {
		return nil, err
	}
	if engine == "" {
		engine = cfg.Engine
	}
	return signer.NewClient(strings.TrimRight(engine, "/"), cfg.KeyID, key), nil
}

// print writes v as JSON with --json, else as text.
func (o readOptions) print(w io.Writer, v any, text func(io.Writer)) error {
	if !o.json {
		text(w)
		return nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func connectedAccountArg(cmd *cobra.Command, args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return errors.New("give the connected account (ta_…); \"mishmar read accounts\" lists the ones the agent was given")
	}
	return nil
}

// readTime reads a time flag: RFC 3339, or a date (its midnight, UTC).
func readTime(flag, v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%s %q: use RFC 3339 (2026-11-12T09:30:00Z) or a date (2026-11-12)", flag, v)
}

// printAccount is a connected account in a line, then how current its data is.
func printAccount(w io.Writer, a api.ConnectedAccount) {
	use := "read only"
	if a.Traded {
		use = "traded"
	}
	fmt.Fprintf(w, "%s  %s  %s  %s  %s\n", a.Id, a.Name, a.Venue, a.Status, use)
	if a.Sync != nil && len(*a.Sync) > 0 {
		parts := make([]string, 0, len(*a.Sync))
		for _, s := range *a.Sync {
			part := fmt.Sprintf("%s %s", s.Kind, s.Status)
			if s.AsOf != nil {
				part += " as of " + stampOf(*s.AsOf)
			}
			if s.Progress != nil && s.Status == "backfilling" {
				part += " (" + *s.Progress + " done)"
			}
			if s.Error != nil && *s.Error != "" {
				part += " (" + *s.Error + ")"
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(w, "  %s\n", strings.Join(parts, "; "))
	}
	if a.Restrictions != nil {
		for _, r := range *a.Restrictions {
			on := "the account"
			if r.Asset != nil && *r.Asset != "" {
				on = *r.Asset
			}
			fmt.Fprintf(w, "  restricted: %s on %s since %s\n", r.Cause, on, stampOf(r.Since))
		}
	}
}

// transactionWhat is a transaction in a few words: a trade's side, size,
// symbol and price, or an amount in its asset.
func transactionWhat(x api.AccountTransaction) string {
	var b strings.Builder
	if x.Side != nil && x.Size != nil && x.Symbol != nil {
		fmt.Fprintf(&b, "%s %s %s", *x.Side, *x.Size, *x.Symbol)
		if x.Price != nil {
			fmt.Fprintf(&b, " at %s", *x.Price)
		}
		if x.FeeUsd != nil {
			fmt.Fprintf(&b, ", fee %s USD", *x.FeeUsd)
		}
		return b.String()
	}
	if x.Amount != nil {
		b.WriteString(*x.Amount)
		if x.Asset != nil {
			b.WriteString(" " + *x.Asset)
		}
		if x.AmountUsd != nil {
			fmt.Fprintf(&b, " (%s USD)", *x.AmountUsd)
		}
	}
	if x.Symbol != nil {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("on " + *x.Symbol)
	}
	return b.String()
}

// originOf says who placed a trade, as the agent reads it.
func originOf(o *api.TransactionOrigin) string {
	switch {
	case o == nil:
		return ""
	case o.Own != nil && *o.Own:
		return "this agent"
	case o.Kind == api.TransactionOriginKindAgent && o.AgentName != nil:
		return "agent " + *o.AgentName
	case o.Kind == api.TransactionOriginKindAgent:
		return "another agent"
	}
	return "outside Mishmar"
}

func usd(d *string) string {
	if d == nil {
		return "-"
	}
	return *d + " USD"
}

func stampOf(t time.Time) string { return t.UTC().Format(time.RFC3339) }
