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

	"github.com/shopspring/decimal"
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

A connected account is one pool of money at a venue, and its connection is
the venue login that holds it: a Binance login has a Spot and a USDⓈ-M
Futures account, a Hyperliquid address has Perps and Spot. Every balance,
position and transaction belongs to one account. "read accounts" lists the
accounts by connection, each with its value, and the connection's total;
when the agent was given only some of a login's accounts it says how many
("2 of 3 accounts") and totals those alone. A move between two accounts of
one connection is a transfer_out on one and a transfer_in on the other,
each naming the other account.

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
		Short: "The connected accounts the agent was given, by connection, each with its value",
		Long: `The connected accounts the agent was given, grouped by their connection
(the venue login that holds them). Each line is one account: its id, then
venue · connection · account · login (the account's name left out on a
login with a single account), its value in USD, its status, and whether an
agent trades on it. Under a login's accounts, "total" is their sum; when the
agent was given fewer than all of them, the line says how many it sees ("2
of 3 accounts, total of these") and the sum is of those alone, not the
login's value. An asset with no price is not in a value and is named on its
line.`,
		Args: cobra.NoArgs,
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
				printAccounts(w, accounts)
			})
		},
	}
}

func newReadAccountCmd(o *readOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "account <connected account>",
		Short: "One connected account the agent was given, with its connection, its setup without any secret",
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
		Short: "The account's balances, each with its value in USD, now or at a past moment",
		Long: `The account's balances: its value first, then every asset it holds with
its amount, its value in USD and the price that value was worked out at. An
asset with no price shows "-" and is named on the first line, since its
value is not in the account's.`,
		Args: connectedAccountArg,
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
					fmt.Fprintf(w, ", value %s USD%s", *b.EquityUsd, notCounting(b.UnpricedAssets))
				}
				fmt.Fprintln(w)
				if len(b.Balances) == 0 {
					fmt.Fprintln(w, "  no balances")
					return
				}
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				for _, x := range b.Balances {
					fmt.Fprintf(tw, "  %s\t%s\t%s", x.Asset, x.Amount, usd(x.UsdValue))
					if x.PriceUsd != nil {
						fmt.Fprintf(tw, "\tat %s", *x.PriceUsd)
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

func newReadPositionsCmd(o *readOptions) *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   "positions <connected account>",
		Short: "The venue's net positions on the account, whoever placed them, with their market, now or at a past moment",
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
					fmt.Fprintf(tw, "  %s\t%s %s\t%s\tentry %s", x.Side, x.Symbol, x.Market, x.Size, x.EntryPrice)
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
		Long: `The account's transactions as the venue reports them, newest first, with
the market (perp, spot, …) beside a symbol. transfer_in and transfer_out
cross the account's edge: a move to or from another account of the same
connection, or of a connected sub-account, names that account ("to
bn_spot", "from bn_futures") and shows there too with the opposite kind;
without one the other side is outside Mishmar (a deposit, a withdrawal, or
an account not connected here). A trade's last column says who placed it:
"this agent", another agent by name (through Mishmar), or "outside
Mishmar" (the owner by hand, another program or the venue). A longer
history comes a page at a time: the last line names the --cursor of the
next page.`,
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

// printAccounts lists accounts by connection in the engine's order
// (connections oldest first, each one's accounts together in the venue's
// order), which it keeps; an account listed apart from its connection's
// others is shown with them. A line per account, then the connection's
// line (the total of a login with more than one account, or how many of
// its accounts are shown when the agent was given fewer than all, with the
// total of those), then how current the connection's data is. A blank line
// separates connections.
func printAccounts(w io.Writer, accounts []api.ConnectedAccount) {
	var order []string
	byConnection := map[string][]api.ConnectedAccount{}
	for _, a := range accounts {
		id := connectionOf(a)
		if _, ok := byConnection[id]; !ok {
			order = append(order, id)
		}
		byConnection[id] = append(byConnection[id], a)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, id := range order {
		if i > 0 {
			fmt.Fprintln(tw)
		}
		group := byConnection[id]
		for _, a := range group {
			fmt.Fprintln(tw, accountRow(a))
		}
		if line := connectionLine(group); line != "" {
			fmt.Fprintln(tw, line)
		}
		if fresh := syncOf(group[0].Connection.Sync); fresh != "" {
			fmt.Fprintf(tw, "\t%s\n", fresh)
		}
	}
	tw.Flush()
}

// printAccount is one account's line, then what it is at its connection,
// what its value leaves out and what restricts it.
func printAccount(w io.Writer, a api.ConnectedAccount) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, accountRow(a))
	tw.Flush()
	c := a.Connection
	fmt.Fprintf(w, "  %s account %s, %s connection %s (%s)", a.Type, a.VenueRef, shareOf(c.AccountCount), connectionOf(a), c.Kind)
	v := c.VenueAccount
	login := "main login"
	if v.Role == api.VenueAccountRoleSubaccount {
		login = "sub-account"
	}
	if v.Id != nil && *v.Id != "" {
		login += " " + *v.Id
	}
	if v.ParentVenueAccountId != nil && *v.ParentVenueAccountId != "" {
		login += " of " + *v.ParentVenueAccountId
	}
	fmt.Fprintf(w, ", %s", login)
	if c.ParentId != nil && *c.ParentId != "" {
		fmt.Fprintf(w, " (its main login is connection %s)", *c.ParentId)
	}
	fmt.Fprintln(w)
	if a.EquityAsOf != nil {
		fmt.Fprintf(w, "  value as of %s%s\n", stampOf(*a.EquityAsOf), notCounting(a.UnpricedAssets))
	}
	if fresh := syncOf(c.Sync); fresh != "" {
		fmt.Fprintf(w, "  %s\n", fresh)
	}
	if a.Restrictions != nil {
		for _, r := range *a.Restrictions {
			fmt.Fprintf(w, "  restricted: %s\n", restrictionOf(r))
		}
	}
}

// accountRow is an account's cells: its id, its line, its value, its status,
// whether an agent trades on it, and what to know about its value.
func accountRow(a api.ConnectedAccount) string {
	use := "read only"
	switch {
	case a.Traded:
		use = "traded"
	case a.Tradable != nil && !*a.Tradable:
		use = "read only (not tradable yet)"
		if r := deref(a.UntradableReason); r != "" {
			use = "read only (" + r + ")"
		}
	}
	value := "no value yet"
	if a.EquityUsd != nil {
		value = *a.EquityUsd + " USD"
	}
	var notes []string
	if u := a.UnpricedAssets; u != nil && len(*u) > 0 {
		notes = append(notes, "not counting "+strings.Join(*u, ", ")+" (no price)")
	}
	if a.Restrictions != nil {
		for _, r := range *a.Restrictions {
			notes = append(notes, "restricted: "+restrictionOf(r))
		}
	}
	row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", a.Id, accountLine(a), value, a.Status, use)
	if len(notes) > 0 {
		row += "\t" + strings.Join(notes, "; ")
	}
	return row
}

// connectionLine is the line under a connection's accounts: "total" and
// their sum when they are the whole login and more than one, "2 of 3
// accounts, total of these" and their sum when the agent was given some of
// the login's accounts, "1 of 2 accounts" for one of them alone, and ""
// for a login's single account.
func connectionLine(group []api.ConnectedAccount) string {
	shown, count := len(group), group[0].Connection.AccountCount
	switch {
	case count > shown && shown == 1:
		return fmt.Sprintf("\t1 of %d accounts", count)
	case count > shown:
		return fmt.Sprintf("\t%d of %d accounts, total of these\t%s", shown, count, connectionTotal(group))
	case shown > 1:
		return "\ttotal\t" + connectionTotal(group)
	}
	return ""
}

// shareOf says which share of its connection an account is, from the
// connection's accountCount.
func shareOf(count int) string {
	switch {
	case count == 1:
		return "the only account of"
	case count > 1:
		return fmt.Sprintf("one of the %d accounts of", count)
	}
	return "an account of"
}

// connectionTotal is the sum of the accounts' values, as of the oldest
// reading among them, naming what it leaves out.
func connectionTotal(group []api.ConnectedAccount) string {
	sum := decimal.Zero
	var asOf *time.Time
	var unpriced, missing []string
	seen := map[string]bool{}
	for _, a := range group {
		d, err := decimal.NewFromString(deref(a.EquityUsd))
		if a.EquityUsd == nil || err != nil {
			missing = append(missing, a.Id)
			continue
		}
		sum = sum.Add(d)
		if a.EquityAsOf != nil && (asOf == nil || a.EquityAsOf.Before(*asOf)) {
			asOf = a.EquityAsOf
		}
		if a.UnpricedAssets != nil {
			for _, x := range *a.UnpricedAssets {
				if !seen[x] {
					seen[x] = true
					unpriced = append(unpriced, x)
				}
			}
		}
	}
	if len(missing) == len(group) {
		return "no value yet"
	}
	out := sum.String() + " USD"
	if asOf != nil {
		out += " as of " + stampOf(*asOf)
	}
	if len(unpriced) > 0 {
		out += ", not counting " + strings.Join(unpriced, ", ") + " (no price)"
	}
	if len(missing) > 0 {
		out += ", without " + strings.Join(missing, ", ") + " (no value yet)"
	}
	return out
}

// accountLine names an account for a person, in one line: the venue, the
// connection's name, the account's own name (left out when the login has a
// single account, connection.accountCount 1) and which login it is.
func accountLine(a api.ConnectedAccount) string {
	parts := []string{venueName(a.Venue)}
	if a.Connection.Name != "" {
		parts = append(parts, a.Connection.Name)
	}
	if a.Name != "" && a.Connection.AccountCount != 1 {
		parts = append(parts, a.Name)
	}
	v := a.Connection.VenueAccount
	switch {
	case v.Label != nil && *v.Label != "":
		parts = append(parts, *v.Label)
	case v.Id != nil && *v.Id != "":
		parts = append(parts, *v.Id)
	}
	return strings.Join(parts, " · ")
}

// connectionOf is the id of an account's connection.
func connectionOf(a api.ConnectedAccount) string {
	if a.Connection.Id != "" {
		return a.Connection.Id
	}
	return a.VenueConnectionId
}

// venueName is a venue as a person writes it.
func venueName(v api.Venue) string {
	switch v {
	case api.VenueHyperliquid:
		return "Hyperliquid"
	case api.VenueBinance:
		return "Binance"
	case api.VenueBybit:
		return "Bybit"
	case api.VenueOkx:
		return "OKX"
	case api.VenueCoinbase:
		return "Coinbase"
	case api.VenueFireblocks:
		return "Fireblocks"
	}
	return string(v)
}

// syncOf says how current each kind of a connection's data is, in a line;
// "" before the connection is verified.
func syncOf(sync *[]api.SyncState) string {
	if sync == nil || len(*sync) == 0 {
		return ""
	}
	parts := make([]string, 0, len(*sync))
	for _, s := range *sync {
		part := fmt.Sprintf("%s %s", s.Kind, s.Status)
		if s.AsOf != nil {
			part += " as of " + stampOf(*s.AsOf)
		}
		if s.Progress != nil && s.Status == api.SyncStateStatusBackfilling {
			part += " (" + *s.Progress + " done)"
		}
		if s.Error != nil && *s.Error != "" {
			part += " (" + *s.Error + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// restrictionOf is a restriction in a few words.
func restrictionOf(r api.ConnectedAccountRestriction) string {
	on := "the account"
	if r.Asset != nil && *r.Asset != "" {
		on = *r.Asset
	}
	return fmt.Sprintf("%s on %s since %s", r.Cause, on, stampOf(r.Since))
}

// notCounting names the assets with no price, which a value leaves out; ""
// when every asset was priced.
func notCounting(unpriced *[]string) string {
	if unpriced == nil || len(*unpriced) == 0 {
		return ""
	}
	return ", not counting " + strings.Join(*unpriced, ", ") + " (no price)"
}

// symbolOf is a symbol with its market kind, which it needs to name a market
// (BTCUSDT perp, BTCUSDT spot).
func symbolOf(x api.AccountTransaction) string {
	if x.Symbol == nil {
		return ""
	}
	if x.Market != nil && *x.Market != "" {
		return *x.Symbol + " " + string(*x.Market)
	}
	return *x.Symbol
}

// transactionWhat is a transaction in a few words: a trade's side, size,
// market and price, or an amount in its asset, and on a transfer the account
// on the other side when it is one here.
func transactionWhat(x api.AccountTransaction) string {
	var b strings.Builder
	if x.Side != nil && x.Size != nil && x.Symbol != nil {
		fmt.Fprintf(&b, "%s %s %s", *x.Side, *x.Size, symbolOf(x))
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
	if other := deref(x.CounterpartConnectedAccountId); other != "" {
		switch x.Kind {
		case api.AccountTransactionKindTransferOut:
			b.WriteString(" to " + other)
		case api.AccountTransactionKindTransferIn:
			b.WriteString(" from " + other)
		default:
			b.WriteString(" with " + other)
		}
	}
	if x.Symbol != nil {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("on " + symbolOf(x))
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

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func stampOf(t time.Time) string { return t.UTC().Format(time.RFC3339) }
