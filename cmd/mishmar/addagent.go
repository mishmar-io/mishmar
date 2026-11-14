package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/proposal"
	"github.com/mishmar-io/mishmar/signer"
)

type addAgentOptions struct {
	engine, token, name, capital, config string
	connectedAccounts                    []string
}

func newAddAgentCmd() *cobra.Command {
	var o addAgentOptions
	cmd := &cobra.Command{
		Use:     "add-agent",
		GroupID: groupSetup,
		Short:   "Create an agent on the engine, issue its key, deal a venue key per connected account, write the signer's config",
		Long: `The one-command setup of a new agent, over the operator API with a bearer
token (--token or MISHMAR_OPERATOR_TOKEN):

  1. the connected accounts are read, so a connected account whose venue has no supported key type
     stops the command before anything is created;
  2. the agent is created, with the capital allocated to it when --capital
     gives some (what its own leverage and drawdown rules are measured
     against), and a key is issued; its secret, shown once, goes straight into
     the signer's config, which is written (mode 0600) before anything else
     happens;
  3. for every connected account a two-party venue key is made: on Hyperliquid by key
     generation between this signer and the engine's (POST /agents/{id}/venue-keys
     without a share, then two rounds of POST /agent/signer/keygen), so no machine
     ever holds both shares; on an exchange dealt here, the engine's share sent to
     the engine and the agent's share stored in the config under the venue key id
     the engine returns.

The command prints what the venue needs next for each key. It never prints
a share or the secret. The config file must not exist yet: an agent's
identity is not overwritten.

Environment:
  MISHMAR_OPERATOR_TOKEN   the operator token, when --token is not given`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.token == "" {
				o.token = os.Getenv("MISHMAR_OPERATOR_TOKEN")
			}
			if o.token == "" {
				return errors.New("an operator token is required: --token or MISHMAR_OPERATOR_TOKEN")
			}
			return addAgent(cmd.Context(), o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.engine, "engine", "http://localhost:8080/v1", "engine API base URL")
	f.StringVar(&o.token, "token", "", "operator token (or MISHMAR_OPERATOR_TOKEN)")
	f.StringVar(&o.name, "name", "", "the agent's name")
	f.StringVar(&o.capital, "capital", "", "the capital allocated to the agent in USD, a decimal above 0 (5000): what its own leverage and drawdown rules are measured against; none when left out")
	f.StringArrayVar(&o.connectedAccounts, "connected-account", nil, "a connected account the agent trades through (ta_…); repeat for several")
	configFlag(cmd, &o.config)
	for _, name := range []string{"name", "connected-account"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// plannedKey is one venue key to deal: the connected account and its scheme.
type plannedKey struct {
	connectedAccount api.ConnectedAccount
	scheme           string
}

func addAgent(ctx context.Context, o addAgentOptions, out io.Writer) error {
	if o.capital != "" && !positiveDecimal(o.capital) {
		return fmt.Errorf("--capital %q: give the capital in USD as a decimal above 0, such as 5000 or 2500.50", o.capital)
	}
	path, err := configPath(o.config)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists already and holds an agent's identity; pass --config for another file, or remove it first", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	op := newOperatorClient(o.engine, o.token)

	// 1. The connected accounts, and whether this signer can serve their keys.
	plan := make([]plannedKey, 0, len(o.connectedAccounts))
	for _, id := range o.connectedAccounts {
		connectedAccount, err := op.getConnectedAccount(ctx, id)
		if err != nil {
			return err
		}
		if connectedAccount.Connection.Kind == api.VenueConnectionKindCosignerCallback {
			return fmt.Errorf("connected account %s (%s) is a custodian's callback: it takes no venue keys, any agent may use it; leave it out", id, accountLine(connectedAccount))
		}
		if connectedAccount.Tradable != nil && !*connectedAccount.Tradable {
			why := "Mishmar reads it but gives no trading key there"
			if r := deref(connectedAccount.UntradableReason); r != "" {
				why = r
			}
			return fmt.Errorf("connected account %s (%s) cannot be traded: %s; leave it out (an agent can still be given it to read)", id, accountLine(connectedAccount), why)
		}
		scheme, ok := signer.SchemeForVenue(string(connectedAccount.Venue))
		if !ok {
			return fmt.Errorf("connected account %s (%s) is on %s, whose keys (%s) are not supported by this signer yet", id, accountLine(connectedAccount), connectedAccount.Venue, venueKeyType(connectedAccount.Venue))
		}
		plan = append(plan, plannedKey{connectedAccount: connectedAccount, scheme: scheme})
	}

	// 2. The agent and its key; the config is written at once.
	// This command sets up an agent that trades: it makes its trading keys below.
	create := api.AgentCreate{Name: o.name, Permission: api.AgentPermissionTrade}
	if o.capital != "" {
		create.CapitalUsd = &o.capital
	}
	agent, err := op.createAgent(ctx, create)
	if err != nil {
		return err
	}
	if agent.CapitalUsd != nil {
		fmt.Fprintf(out, "agent %s (%s) created, with capital %s USD\n", agent.Id, agent.Name, *agent.CapitalUsd)
	} else {
		fmt.Fprintf(out, "agent %s (%s) created\n", agent.Id, agent.Name)
	}
	issued, err := op.issueKey(ctx, agent.Id)
	if err != nil {
		return fmt.Errorf("agent %s was created but no key was issued: %w", agent.Id, err)
	}
	cfg := &signer.Config{Engine: o.engine, KeyID: issued.Id, Secret: issued.Secret, AgentID: agent.Id, VenueKeys: map[string]signer.VenueKey{}}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("agent %s has key %s, whose secret could not be saved (it is shown once; revoke the key and start over): %w", agent.Id, issued.Id, err)
	}
	fmt.Fprintf(out, "key %s issued; the secret is in %s (mode 0600)\n", issued.Id, path)

	// 3. A venue key per connected account.
	agentKey, err := cfg.Key()
	if err != nil {
		return err
	}
	for _, p := range plan {
		// Every two-party key is made by key generation with the engine's
		// signer: the venue key is created without a share, then the two
		// signers run the rounds, and no machine ever holds the whole key.
		vk, err := op.createVenueKey(ctx, agent.Id, api.VenueKeyCreate{ConnectedAccountId: p.connectedAccount.Id})
		if err != nil {
			return fmt.Errorf("connected account %s: %w (the agent and its key exist; add this connected account's key later in the console and with mishmar signer keygen)", p.connectedAccount.Id, err)
		}
		network, err := networkOf(p.connectedAccount)
		if err != nil {
			return err
		}
		key, err := signer.RunKeygen(ctx, signer.NewClient(o.engine, cfg.KeyID, agentKey), vk.Id, p.scheme)
		if err != nil {
			return fmt.Errorf("connected account %s: %w (venue key %s exists without a key; run \"mishmar signer keygen --venue-key %s --venue %s\" to try again)", p.connectedAccount.Id, err, vk.Id, vk.Id, p.connectedAccount.Venue)
		}
		public := key.Address
		if key.Scheme == signer.SchemeFrostEd25519 {
			public = frost.PublicKeyPEM(key.PublicKey)
		}
		if err := cfg.AddVenueKey(vk.Id, signer.VenueKey{ConnectedAccountID: p.connectedAccount.Id, Venue: string(p.connectedAccount.Venue), Scheme: key.Scheme, Share: key.Share, Network: network}); err != nil {
			return err
		}
		if err := cfg.Save(path); err != nil {
			return err
		}
		if vk.PublicKey != nil {
			public = *vk.PublicKey
		}
		fmt.Fprintf(out, "venue key %s on connected account %s (%s, %s): %s\n", vk.Id, p.connectedAccount.Id, accountLine(p.connectedAccount), p.scheme, vk.Status)
		fmt.Fprint(out, nextStep(p.connectedAccount, vk, public))
	}
	fmt.Fprintf(out, "\nStart the signer with: mishmar signer run --config %s\n", path)
	return nil
}

// positiveDecimal reports whether s is a decimal string above zero.
func positiveDecimal(s string) bool {
	r, ok := new(big.Rat).SetString(s)
	return ok && proposal.IsDecimal(s) && r.Sign() > 0
}

// networkOf is the Hyperliquid network a connected account trades on.
func networkOf(connectedAccount api.ConnectedAccount) (string, error) {
	if connectedAccount.Connection.Kind != api.VenueConnectionKindHyperliquid {
		return "", nil
	}
	s, err := connectedAccount.Connection.Setup.AsHyperliquidSetup()
	if err != nil {
		return "", fmt.Errorf("connected account %s: its connection's setup: %w", connectedAccount.Id, err)
	}
	return string(s.Network), nil
}

// nextStep says what the venue needs for the key.
func nextStep(connectedAccount api.ConnectedAccount, vk api.VenueKey, public string) string {
	switch connectedAccount.Connection.Kind {
	case api.VenueConnectionKindHyperliquid:
		return fmt.Sprintf("  approve the API wallet %s from the master wallet in the console (venue key → approval), then verify the key.\n", public)
	default:
		s := fmt.Sprintf("  register this public key at %s as the agent's trading key (trading only, no withdrawals, IP-restricted to the address the console shows for the account's connection, if it shows one):\n", connectedAccount.Venue)
		s += indent(public)
		s += "  then enter the key id the venue issued in the console (the agent's Keys tab, Register key id) and verify the key with the signer running.\n"
		return s
	}
}

// venueKeyType names a venue's key type for a message.
func venueKeyType(v api.Venue) string {
	switch v {
	case api.VenueBybit:
		return "RSA"
	case api.VenueOkx:
		return "HMAC"
	case api.VenueFireblocks:
		return "none, a custodian's callback"
	}
	return "unknown"
}
