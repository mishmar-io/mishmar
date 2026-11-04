package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/signer/configlock"
	"github.com/mishmar-io/mishmar/signer/service"
	"github.com/mishmar-io/mishmar/signerapi"
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
	cmd.AddCommand(newSignerInitCmd(), newSignerAddKeyCmd(), newSignerKeygenCmd(), newSignerRunCmd(), newSignerWrapKeyCmd(), newSignerServeCmd())
	return cmd
}

type serveOptions struct {
	listen, store, engineKey, tlsCert, tlsKey, clientCA, level string
	schemes                                                    []string
	sync                                                       time.Duration
}

func newSignerServeCmd() *cobra.Command {
	var o serveOptions
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the signer in the engine's role: the service that holds the engine's shares and finishes its signatures",
		Long: `The engine's signer (spec/signer-service.yaml). It holds the engine's share
of every venue key, the Paillier keys and the presigned nonces, and the
engine holds no signing secret: it relays key generation and presigning
between the agents' signers and this service, and asks it to finish one
signature per order. The service signs only orders and cancels it decoded
itself and that match a decision the engine signed; a verify request for an
exchange read, nothing else.

Reach it from the engine alone: over a Unix socket (--listen unix:/path) when
the two share a machine, else over mTLS (--tls-cert, --tls-key and
--client-ca, the CA the engine's client certificate is issued by). Without
either, it listens in plain HTTP on localhost for development.

The shares are sealed at rest (AES-256-GCM) under the master key in
MISHMAR_SIGNER_KEY, 64 hex characters (openssl rand -hex 32), which must not
live next to the store. Or, with MISHMAR_KMS_KEY naming a Cloud KMS key,
MISHMAR_SIGNER_KEY_WRAPPED holds it wrapped under that key (signer wrap-key):
it is unwrapped at startup with the workload's identity, each time in the KMS
audit log, and if KMS cannot unwrap, the service does not start. --store is where the sealed shares are kept: a file
(the default, mode 0600, replaced atomically) or object storage,
s3://bucket/prefix on AWS S3 or any S3-compatible store (MISHMAR_S3_ENDPOINT,
MISHMAR_S3_REGION, MISHMAR_S3_ACCESS_KEY and MISHMAR_S3_SECRET_KEY, or the
AWS SDK's own variables and roles) and gs://bucket/prefix on Google Cloud
Storage as the workload itself, with no keys (its identity is granted the
prefix). Every share is opened into memory at startup, so no
order waits on the store; --sync is how often a replica reconciles with a
shared backend.

--engine-key is the engine's Ed25519 public key (base64, GET /ledger/head),
with which the decisions are verified; without it they are not, which is for
development only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serve(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.listen, "listen", "127.0.0.1:8091", "host:port, or unix:/path/to/socket")
	f.StringVar(&o.store, "store", "", "where the sealed shares are kept: a file path (default ~/.mishmar/signer-keys.json), file:<path>, s3://… or gs://…")
	f.DurationVar(&o.sync, "sync", time.Minute, "how often to reconcile with a shared backend; 0 never")
	f.StringVar(&o.engineKey, "engine-key", "", "the engine's public key, base64, to verify decisions with")
	f.StringVar(&o.tlsCert, "tls-cert", "", "the service's certificate (PEM); with --tls-key and --client-ca, mTLS")
	f.StringVar(&o.tlsKey, "tls-key", "", "the service's private key (PEM)")
	f.StringVar(&o.clientCA, "client-ca", "", "the CA that issues the engine's client certificate (PEM)")
	f.StringSliceVar(&o.schemes, "schemes", nil, "the schemes this instance holds: frost-ed25519, ecdsa-2p (default both)")
	f.StringVar(&o.level, "log-level", "info", "debug | info | warn | error")
	return cmd
}

func serve(ctx context.Context, o serveOptions) error {
	log, err := newLogger(o.level)
	if err != nil {
		return err
	}
	master, err := service.MasterKeyFromEnv(ctx)
	if err != nil {
		return err
	}
	spec := o.store
	if spec == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("no home directory for the default key file; pass --store: %w", err)
		}
		spec = filepath.Join(home, ".mishmar", "signer-keys.json")
	}
	backend, err := service.OpenBackend(spec)
	if err != nil {
		return err
	}
	store, err := service.OpenStore(backend, master)
	if err != nil {
		return err
	}
	opts := service.Options{Store: store, Logger: log}
	if o.engineKey != "" {
		pub, err := agentkey.ParsePublicKey(o.engineKey)
		if err != nil {
			return fmt.Errorf("--engine-key: %w", err)
		}
		opts.EnginePublicKey = pub
	}
	for _, s := range o.schemes {
		switch scheme := signerapi.Scheme(s); scheme {
		case signerapi.SchemeFrostEd25519, signerapi.SchemeEcdsa2p:
			opts.Schemes = append(opts.Schemes, scheme)
		default:
			return fmt.Errorf("--schemes: %q is not frost-ed25519 or ecdsa-2p", s)
		}
	}
	svc, err := service.New(opts)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second}
	var ln net.Listener
	switch {
	case strings.HasPrefix(o.listen, "unix:"):
		path := strings.TrimPrefix(o.listen, "unix:")
		_ = os.Remove(path)
		if ln, err = net.Listen("unix", path); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	case o.tlsCert != "" || o.tlsKey != "" || o.clientCA != "":
		if o.tlsCert == "" || o.tlsKey == "" || o.clientCA == "" {
			return errors.New("mTLS needs --tls-cert, --tls-key and --client-ca together")
		}
		cert, err := tls.LoadX509KeyPair(o.tlsCert, o.tlsKey)
		if err != nil {
			return fmt.Errorf("--tls-cert/--tls-key: %w", err)
		}
		caPEM, err := os.ReadFile(o.clientCA)
		if err != nil {
			return fmt.Errorf("--client-ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return errors.New("--client-ca: no certificate in the file")
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS13}
		if ln, err = tls.Listen("tcp", o.listen, srv.TLSConfig); err != nil {
			return err
		}
	default:
		host, _, err := net.SplitHostPort(o.listen)
		if err != nil {
			return fmt.Errorf("--listen %q: %w", o.listen, err)
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("plain HTTP is for localhost only; use mTLS or a Unix socket to reach the service from elsewhere")
		}
		if ln, err = net.Listen("tcp", o.listen); err != nil {
			return err
		}
	}
	log.Info("signer service: listening", "on", o.listen, "store", spec, "keys", len(store.IDs()), "instance", svc.Instance(), "decisionsVerified", opts.EnginePublicKey != nil)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	if o.sync > 0 {
		go func() {
			t := time.NewTicker(o.sync)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := svc.Sync(); err != nil {
						log.Error("signer service: sync failed", "err", err)
					}
				}
			}
		}()
	}
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type signerKeygenOptions struct {
	config, venueKey, tradingAccount, venue, network string
}

func newSignerWrapKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "wrap-key",
		Short: "Wrap the signer service's master key under a Cloud KMS key, and print the wrapped form",
		Long: `Print the value for MISHMAR_SIGNER_KEY_WRAPPED, base64: the master key in
MISHMAR_SIGNER_KEY wrapped under the KMS key in MISHMAR_KMS_KEY, so the shares
already sealed under it keep opening; with MISHMAR_SIGNER_KEY unset, a new
master key. It uses this workload's identity from the metadata server, and
checks that the result unwraps. The raw key is never printed. Once the service
runs on the wrapped form, remove the raw key from wherever it was kept.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wrapped, err := service.WrapMasterKey(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), wrapped)
			return nil
		},
	}
}

func newSignerKeygenCmd() *cobra.Command {
	var o signerKeygenOptions
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate the agent's half of a two-party key for a venue key the engine created without a share",
		Long: `Run two-party key generation with the engine for a venue key created
without a share (the console, or POST /agents/{id}/venue-keys with no
engineShare): two rounds over the agent API, the engine relaying to its own
signer, after which this signer holds the agent's half, the engine's signer
holds the engine's, no machine ever held both, and the venue key carries the
key's public side: a Hyperliquid API wallet's address for the master wallet
to approve (two-party ECDSA), or the Ed25519 public key for the owner to
register at the exchange (two-party FROST). "mishmar add-agent" does this
for every trading account of a new agent.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := configPath(o.config)
			if err != nil {
				return err
			}
			cfg, err := signer.Load(path)
			if err != nil {
				return err
			}
			if _, dup := cfg.VenueKeys[o.venueKey]; dup {
				return fmt.Errorf("the config holds a key for %s already", o.venueKey)
			}
			agentKey, err := cfg.Key()
			if err != nil {
				return err
			}
			scheme, ok := signer.SchemeForVenue(o.venue)
			if !ok {
				return fmt.Errorf("--venue %s: this signer has no two-party key scheme for it", o.venue)
			}
			if o.venue == string(api.VenueHyperliquid) && o.network == "" {
				return errors.New("--network is required for a Hyperliquid key: mainnet | testnet")
			}
			client := signer.NewClient(cfg.Engine, cfg.KeyID, agentKey)
			key, err := signer.RunKeygen(cmd.Context(), client, o.venueKey, scheme)
			if err != nil {
				return err
			}
			if err := cfg.AddVenueKey(o.venueKey, signer.VenueKey{TradingAccountID: o.tradingAccount, Venue: o.venue, Scheme: key.Scheme, Share: key.Share, Network: o.network}); err != nil {
				return err
			}
			// A new key often follows one that expired or was revoked: its share goes.
			// One that is still active stays until its replacement takes over.
			out := cmd.OutOrStdout()
			var dropped []string
			if keys, err := client.VenueKeys(cmd.Context()); err != nil {
				fmt.Fprintf(out, "could not read the agent's keys from the engine (%v); shares of expired or revoked keys stay in the config until the next keygen\n", err)
			} else {
				dropped = cfg.PruneVenueKeys(keys)
			}
			if err := cfg.Save(path); err != nil {
				return err
			}
			if len(dropped) > 0 {
				fmt.Fprintf(out, "dropped the shares of %s, which the engine reports expired or revoked\n", strings.Join(dropped, ", "))
			}
			warnRunningSigner(out, path)
			if key.Scheme == signer.SchemeECDSA2p {
				fmt.Fprintf(out, "generated the agent's key for %s (%s %s, trading account %s) in %s\n  approve the API wallet %s from the master wallet in the console, then verify the key.\n", o.venueKey, o.venue, o.network, o.tradingAccount, path, key.Address)
				return nil
			}
			fmt.Fprintf(out, "generated the agent's key for %s (%s, trading account %s) in %s\n  register this public key at %s as the agent's trading key (trading only, no withdrawals, IP-restricted to the engine):\n%s  then enter the key id the venue issued in the console (venue key → register) and verify the key with the signer running.\n", o.venueKey, o.venue, o.tradingAccount, path, o.venue, indent(frost.PublicKeyPEM(key.PublicKey)))
			return nil
		},
	}
	f := cmd.Flags()
	configFlag(cmd, &o.config)
	f.StringVar(&o.venueKey, "venue-key", "", "the venue key id the engine returned (vk_…)")
	f.StringVar(&o.tradingAccount, "trading-account", "", "the trading account the key is on (ta_…)")
	f.StringVar(&o.venue, "venue", string(api.VenueHyperliquid), "the trading account's venue: hyperliquid | binance")
	f.StringVar(&o.network, "network", "", "a Hyperliquid trading account's network: mainnet | testnet")
	for _, name := range []string{"venue-key", "trading-account"} {
		_ = cmd.MarkFlagRequired(name)
	}
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
			warnRunningSigner(cmd.OutOrStdout(), path)
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
	var config, level, local string
	var exitOnStdinClose bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the signer until SIGINT or SIGTERM",
		Long: `Poll the engine for requests addressed to this agent and answer them: an
order is signed only when it is the one the agent proposed (the proposal in
the request is signed by this agent's key, for this trading account, and the venue
request decodes to that order and nothing more); a verify request is signed
when it decodes to a read and not an order. Everything else is refused with
a reason the engine records on the decision. One line is logged per request.

With --local the signer also serves its local API, which the SDKs use when
they run the signer as a child process: POST /propose turns an order or a
cancel into a proposal signed with this agent's key (one-shot where it can),
and the agent's reads pass through. unix:/path makes a socket only this user
can reach; host:port must be a loopback address and needs a token in
` + signer.EnvLocalToken + `, sent as a bearer token.

One signer runs per config: it holds a lock on <config>.lock while it runs,
and a second one on the same config stops at once, naming the first's pid.
Several processes of one agent share one signer through its local API.

--exit-on-stdin-close is for a parent that starts the signer as its child
(the SDKs' Signer does): give it a pipe on stdin and never write to it. The
signer stops when the pipe closes, as on SIGTERM, which happens when the
parent exits however it exits, SIGKILL and crashes included, so a signer
never outlives the bot that started it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := configPath(config)
			if err != nil {
				return err
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
			log, err := newLogger(level)
			if err != nil {
				return err
			}
			s, err := signer.New(cfg, nil, log)
			if err != nil {
				return err
			}
			log.Info("signer: config loaded", "path", path, "hook", cfg.Hook != "")
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			if exitOnStdinClose {
				go untilClosed(cmd.InOrStdin(), log, cancel)
			}
			if local == "" {
				return s.Run(ctx)
			}
			errc := make(chan error, 2)
			go func() { errc <- s.ServeLocal(ctx, local, os.Getenv(signer.EnvLocalToken)) }()
			go func() { errc <- s.Run(ctx) }()
			err = <-errc
			cancel()
			<-errc
			return err
		},
	}
	configFlag(cmd, &config)
	cmd.Flags().StringVar(&level, "log-level", "info", "debug | info | warn | error")
	cmd.Flags().StringVar(&local, "local", "", "also serve the local API at unix:/path or 127.0.0.1:port (the latter with a token in "+signer.EnvLocalToken+")")
	cmd.Flags().BoolVar(&exitOnStdinClose, "exit-on-stdin-close", false, "stop when standard input closes: for a parent that runs the signer as its child, with a pipe it never writes to")
	return cmd
}

// untilClosed reads r until it ends, then stops the signer: the parent that
// started it with a pipe on stdin has gone.
func untilClosed(r io.Reader, log *slog.Logger, stop context.CancelFunc) {
	_, err := io.Copy(io.Discard, r)
	log.Info("signer: standard input closed; stopping", "err", err)
	stop()
}

// warnRunningSigner says so when a signer runs with the config just written:
// it read the config when it started and will not see the new key until it
// is restarted.
func warnRunningSigner(out io.Writer, path string) {
	if pid, held, err := configlock.Holder(path); err == nil && held {
		fmt.Fprintf(out, "a signer is running with %s (pid %d): restart it so it uses the new key\n", path, pid)
	}
}
