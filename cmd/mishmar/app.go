package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/mishmar-io/mishmar/mcp"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/signer/daemon"
	"github.com/mishmar-io/mishmar/signer/signin"
)

// The app's server's environment.
const (
	// envNoBrowser set keeps an app's server from opening the browser: the
	// sign-in link is in the tool's answer either way.
	envNoBrowser = "MISHMAR_NO_BROWSER"
	// envDaemon "off" runs an app's signer inside its MCP server instead of
	// the background signer (a sandbox that cannot start one); approvals then
	// need the app open.
	envDaemon = "MISHMAR_SIGNER_DAEMON"
)

// What an app's server does to the machine, replaced by tests: open the
// browser, look at the machine, start the background signer detached.
var (
	openBrowser  = defaultOpenBrowser
	detectRemote = func() signin.Remote { return signin.Detect(signin.Here()) }
	startDaemon  = func(_ context.Context, home daemon.Home) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return daemon.Spawn(home, exe, "signer", "daemon", "--home", home.Root)
	}
	// appWait is how long a tool waits for the person to approve the sign-in.
	appWait = 30 * time.Second
)

// appTarget is the hosting API an app's server signs in at, and the
// environment its keys name.
func appTarget(env, apiFlag string, envSet bool) (apiURL, environment string, err error) {
	if apiFlag != "" {
		u, perr := url.Parse(apiFlag)
		if perr != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") {
			return "", "", fmt.Errorf("--api %q is not the hosting API's base URL (https://api.mishmar.io)", apiFlag)
		}
		if envSet {
			environment = env
		}
		return strings.TrimRight(apiFlag, "/"), environment, nil
	}
	host, ok := signer.HostedAPI[env]
	if !ok {
		return "", "", fmt.Errorf("--env %q is not prod, testnet or stage (or give --api)", env)
	}
	return host, env, nil
}

// runApp is `mishmar mcp --app <slug>`: the MCP server as a thin client of
// the background signer, which it starts on first use.
func runApp(cmd *cobra.Command, slug, env, apiFlag string, envSet bool, config string, log *slog.Logger) error {
	if !signin.ValidApp(slug) {
		return fmt.Errorf("--app %q is not a short name of lowercase letters, digits and dashes (claude-code, cursor)", slug)
	}
	if config != "" {
		return errors.New("--app keeps its key in the Mishmar home (~/.mishmar/apps/<app>); --config is for a bot's agent, without --app")
	}
	apiURL, environment, err := appTarget(env, apiFlag, envSet)
	if err != nil {
		return err
	}
	home, err := daemon.DefaultHome()
	if err != nil {
		return err
	}
	if os.Getenv(signer.EnvAPIKey) != "" {
		log.Info("mcp: --app signs this computer in through the browser; " + signer.EnvAPIKey + " is not used")
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	conn := &appConn{home: home, log: log, ctx: ctx}
	defer conn.stop()
	backend := mcp.AppBackend(mcp.AppOptions{
		Slug: slug, API: apiURL, Environment: environment, Connect: conn.connect, Remote: detectRemote(),
		Open: func(link string) error { return openBrowser(link) }, Wait: appWait,
		HasKey: func() bool { _, err := os.Stat(home.Config(slug)); return err == nil },
	})
	return mcp.New(backend, version, log).Serve(ctx, cmd.InOrStdin(), cmd.OutOrStdout())
}

// appConn reaches the background signer, or runs the apps' Manager in this
// process where there is none (Windows; MISHMAR_SIGNER_DAEMON=off; a daemon
// that cannot start).
type appConn struct {
	home daemon.Home
	log  *slog.Logger
	ctx  context.Context

	mu     sync.Mutex
	local  *daemon.Manager
	client *daemon.Client
}

func (a *appConn) connect(ctx context.Context, start bool) (*daemon.Client, daemon.Health, error) {
	if !daemon.Supported || os.Getenv(envDaemon) == "off" {
		return a.inProcess()
	}
	if !start {
		c := daemon.Dial(a.home.Socket())
		hctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		h, err := c.Health(hctx)
		if err != nil || h.API != daemon.APIVersion {
			return nil, daemon.Health{}, nil
		}
		return c, h, nil
	}
	c, h, err := daemon.Ensure(ctx, daemon.EnsureOptions{
		Home: a.home, Version: version, Logger: a.log,
		Start: func(ctx context.Context) error { return startDaemon(ctx, a.home) },
	})
	if err == nil {
		return c, h, nil
	}
	a.log.Warn("mcp: the background signer could not run, so this app's signer runs in this process: approvals need the app open", "err", err)
	return a.inProcess()
}

func (a *appConn) inProcess() (*daemon.Client, daemon.Health, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.local == nil {
		m := &daemon.Manager{Home: a.home, Version: version, Logger: a.log, InProcess: true, Detect: detectRemote}
		if err := m.Start(a.ctx); err != nil {
			return nil, daemon.Health{}, err
		}
		a.local, a.client = m, daemon.Local(m.Handler())
	}
	return a.client, daemon.Health{Version: version, API: daemon.APIVersion, PID: os.Getpid(), InProcess: true}, nil
}

func (a *appConn) stop() {
	a.mu.Lock()
	m := a.local
	a.mu.Unlock()
	if m != nil {
		m.Stop()
	}
}

// defaultOpenBrowser opens an http(s) link in the person's browser, with
// nothing of this process's stdio: stdout is the MCP protocol.
func defaultOpenBrowser(link string) error {
	if os.Getenv(envNoBrowser) != "" {
		return errors.New("opening the browser is off (" + envNoBrowser + ")")
	}
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("not a web link: %q", link)
	}
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", link)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		c = exec.Command("xdg-open", link)
	}
	c.Stdin, c.Stdout, c.Stderr = nil, io.Discard, io.Discard
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}

// ---- the background signer, for people ----

func newSignerDaemonCmd() *cobra.Command {
	var homeFlag, level string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the background signer of the AI apps that trade from this computer (started by \"mishmar mcp --app\")",
		Long: `The background signer: one per user and Mishmar home, holding each app's
key and its half of each trading key (~/.mishmar/apps/<app>, files 0600 in
directories 0700) and running one signer per app, so an order a person
approves goes out after the app that proposed it has closed. It answers
"mishmar mcp --app" over a Unix socket only this user can reach
(~/.mishmar/run/signer.sock) and logs to ~/.mishmar/logs/signer.log.

"mishmar mcp --app" starts it on first use; nothing starts it at login, so it
does not survive a restart of the computer (the next tool an app uses starts
it again). A second one for the same home exits at once. "mishmar daemon
stop" stops it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := homeOf(homeFlag)
			if err != nil {
				return err
			}
			log, err := newLogger(level)
			if err != nil {
				return err
			}
			d := &daemon.Daemon{Manager: &daemon.Manager{Home: home, Version: version, Logger: log}}
			err = d.Run(cmd.Context())
			if errors.Is(err, daemon.ErrRunning) {
				log.Info("daemon: a background signer runs already for this home", "home", home.Root)
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&homeFlag, "home", "", "the Mishmar home (default $"+daemon.EnvHome+" or ~/.mishmar)")
	cmd.Flags().StringVar(&level, "log-level", "info", "debug | info | warn | error")
	return cmd
}

func homeOf(flag string) (daemon.Home, error) {
	if flag != "" {
		return daemon.Home{Root: flag}, nil
	}
	return daemon.DefaultHome()
}

func newDaemonCmd() *cobra.Command {
	var homeFlag string
	cmd := &cobra.Command{
		Use:     "daemon",
		GroupID: groupSigner,
		Short:   "See or stop the background signer of the AI apps that trade from this computer",
	}
	cmd.PersistentFlags().StringVar(&homeFlag, "home", "", "the Mishmar home (default $"+daemon.EnvHome+" or ~/.mishmar)")
	status := &cobra.Command{
		Use:   "status",
		Short: "Say whether the background signer runs, and each app's state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := homeOf(homeFlag)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			c := daemon.Dial(home.Socket())
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			h, err := c.Health(ctx)
			if err != nil {
				fmt.Fprintln(out, "No background signer is running. The next Mishmar tool an app uses starts it.")
				for _, slug := range daemon.SortedApps(home) {
					if _, err := os.Stat(home.Config(slug)); err == nil {
						fmt.Fprintf(out, "%s: signed in (%s); its signer runs once the background signer starts\n", slug, home.Config(slug))
					}
				}
				return nil
			}
			fmt.Fprintf(out, "Background signer: mishmar %s, pid %d, running since %s. It stops when the computer restarts.\n", h.Version, h.PID, h.StartedAt.Local().Format(time.DateTime))
			slugs := daemon.SortedApps(home)
			if len(slugs) == 0 {
				fmt.Fprintln(out, "No app is signed in on this computer.")
			}
			for _, slug := range slugs {
				st, err := c.Status(ctx, slug)
				if err != nil {
					fmt.Fprintf(out, "%s: %v\n", slug, err)
					continue
				}
				line := fmt.Sprintf("%s: %s", slug, strings.ReplaceAll(string(st.State), "_", " "))
				if r := st.Record; r != nil && st.State == daemon.SignedIn {
					line += fmt.Sprintf(", agent %s (%s), key code %s, expires %s", r.AgentName, r.AgentID, st.Code, r.KeyExpiresAt.Local().Format(time.DateOnly))
				}
				if st.Ended != "" {
					line += " (" + st.Ended + ")"
				}
				if st.Signer.Error != "" {
					line += "; " + st.Signer.Error
				}
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the background signer: it finishes the answers in flight, then exits",
		Long: `Stops the background signer. Orders a person approves while it is stopped
wait for it until their deadline; the next Mishmar tool an app uses starts
it again. The apps' keys stay; to end an app's trading, disconnect the
computer in the console.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := homeOf(homeFlag)
			if err != nil {
				return err
			}
			stopped, err := daemon.Stop(cmd.Context(), home)
			if err != nil {
				return err
			}
			if !stopped {
				fmt.Fprintln(cmd.OutOrStdout(), "No background signer was running.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "The background signer stopped.")
			return nil
		},
	}
	cmd.AddCommand(status, stop)
	return cmd
}
