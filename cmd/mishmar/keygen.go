package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/frost"
)

type keygenOptions struct {
	scheme, engineOut, agentOut string
}

func newKeygenCmd() *cobra.Command {
	var o keygenOptions
	cmd := &cobra.Command{
		Use:     "keygen",
		GroupID: groupSetup,
		Short:   "Deal a two-party venue key: the engine's share and the agent's",
		Long: `Deal a fresh venue key and split it in two. The whole key exists only
inside this command, for the length of the call: the engine's share goes
to the engine (paste it when adding the venue key to the agent in the
console, or let "mishmar add-agent" do it), the agent's share goes to the
signer's config ("mishmar signer add-key"), and the public key goes to the
venue. Neither share alone can sign.

  --scheme ed25519   a FROST key over Ed25519: Binance

Two-party ECDSA keys (Hyperliquid API wallets, Coinbase) are not dealt:
they are made by key generation between the agent's signer and the engine's,
so no machine ever holds both shares. "mishmar add-agent" runs it for each
Hyperliquid connected account of a new agent, and "mishmar signer keygen" for a
venue key the console created without a share. RSA (Bybit) and HMAC (OKX)
keys are not supported yet.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return keygen(o, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.scheme, "scheme", "", "ed25519")
	f.StringVar(&o.engineOut, "engine-share-out", "", "write the engine's share here (mode 0600) instead of printing it")
	f.StringVar(&o.agentOut, "agent-share-out", "agent.share", "write the agent's share here (mode 0600)")
	_ = cmd.MarkFlagRequired("scheme")
	return cmd
}

// keygen deals the key, writes the shares and says what to do next. The
// engine share goes to stdout by default so it can be piped; everything
// else goes to stderr.
func keygen(o keygenOptions, out, errOut io.Writer) error {
	var agent, engine any
	var public, publicLabel string
	switch o.scheme {
	case "ed25519":
		a, e, err := frost.Deal()
		if err != nil {
			return err
		}
		der, err := x509.MarshalPKIXPublicKey(e.PublicKey())
		if err != nil {
			return err
		}
		agent, engine = a, e
		public = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
		publicLabel = "public key (SPKI PEM): register it at the exchange as the agent's Ed25519 API key"
	case "ecdsa":
		return errors.New("two-party ECDSA keys are not dealt: run \"mishmar add-agent\", or \"mishmar signer keygen\" for a venue key the console created without a share")
	case "rsa", "hmac":
		return fmt.Errorf("scheme %s is not supported yet", o.scheme)
	case "":
		return errors.New("--scheme is required: ed25519")
	default:
		return fmt.Errorf("scheme %q: use ed25519", o.scheme)
	}
	agentJSON, err := json.Marshal(agent)
	if err != nil {
		return err
	}
	engineJSON, err := json.Marshal(engine)
	if err != nil {
		return err
	}
	if o.agentOut == "" {
		return errors.New("--agent-share-out is required: the agent's share is written to a file, never printed")
	}
	if err := writeSecretFile(o.agentOut, append(agentJSON, '\n')); err != nil {
		return fmt.Errorf("agent share: %w", err)
	}
	engineWhere := "printed above"
	if o.engineOut != "" {
		if err := writeSecretFile(o.engineOut, append(engineJSON, '\n')); err != nil {
			return fmt.Errorf("engine share: %w", err)
		}
		engineWhere = o.engineOut
	} else {
		fmt.Fprintln(out, string(engineJSON))
	}
	fmt.Fprintf(errOut, `Dealt a two-party %s key. The whole key existed only inside this command.
  agent share:  %s (mode 0600). The agent's half: it stays with the signer and never goes to the engine.
  engine share: %s. The engine's half: it goes to the engine only.
  %s:
%s
Next:
  1. Give the engine its share: in the console, add a venue key to the agent on the connected account and paste the
     engine share (POST /agents/{agentId}/venue-keys, engineShare). The id in the answer is the venue key id (vk_…).
     "mishmar add-agent" does this for every connected account of a new agent in one go.
  2. Give the signer its share:
       mishmar signer add-key --venue-key vk_… --connected-account ta_… --venue <venue> --share %s
  3. Register the public key at the venue (an exchange: a trading-only key, no withdrawals; Hyperliquid: approve
     the API wallet), enter what the venue issued in the console, and verify the key with "mishmar signer run" running.
`, o.scheme, o.agentOut, engineWhere, publicLabel, indent(public), o.agentOut)
	return nil
}

// indent puts four spaces before every line of s.
func indent(s string) string {
	out := ""
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out += "    " + line + "\n"
	}
	return out
}
