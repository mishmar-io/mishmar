// Package daemon is the background signer of an AI app that trades through
// Mishmar (contract 0.21.0; the wiki's design/app-trading-plan.md, section
// 2.3): one process per user and Mishmar home that holds each app's key and
// trading-key shares and runs one signer per app, so an order a person
// approves goes out after the chat that proposed it has closed. The MCP
// server each app starts (`mishmar mcp --app <slug>`) holds no key: it starts
// the daemon on first use as a detached process and asks it, over a Unix
// socket in a directory only the user can enter, to sign in, to propose and
// to read.
//
// Not in this version: starting at login (no launch agent, systemd unit or
// login item is ever written, so the daemon lives until the computer
// restarts or it is stopped), a daemon on Windows (there the MCP server runs
// the Manager in its own process: approvals need the app open), and the OS
// keychain (keys are in files of mode 0600 in directories of mode 0700).
//
// Files, under the Mishmar home (~/.mishmar, or $MISHMAR_HOME):
//
//	apps/<slug>/agent.json   the app's signer config, today's signer.Config
//	apps/<slug>/app.json     what the sign-in said: agent, key, expiry, console link
//	run/signer.sock          the local API
//	run/daemon.lock          held by the running daemon
//	run/daemon.json          its pid, version and local API version
//	logs/signer.log          what the daemon logs; no secrets
//
// A bot's ~/.mishmar/agent.json is never touched.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnvHome names the variable that moves the Mishmar home away from
// ~/.mishmar (the apps', run and logs directories; a bot's config keeps its
// own default).
const EnvHome = "MISHMAR_HOME"

// Home is a Mishmar home: where the apps' keys, the socket and the log are.
type Home struct{ Root string }

// DefaultHome is $MISHMAR_HOME, else ~/.mishmar.
func DefaultHome() (Home, error) {
	if v := os.Getenv(EnvHome); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return Home{}, err
		}
		return Home{Root: abs}, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return Home{}, fmt.Errorf("daemon: no home directory; set %s: %w", EnvHome, err)
	}
	return Home{Root: filepath.Join(h, ".mishmar")}, nil
}

// AppsDir holds one directory per app.
func (h Home) AppsDir() string { return filepath.Join(h.Root, "apps") }

// AppDir is one app's directory.
func (h Home) AppDir(slug string) string { return filepath.Join(h.AppsDir(), slug) }

// Config is an app's signer config.
func (h Home) Config(slug string) string { return filepath.Join(h.AppDir(slug), "agent.json") }

// Record is what an app's sign-in said.
func (h Home) Record(slug string) string { return filepath.Join(h.AppDir(slug), "app.json") }

// RunDir holds the socket, the lock and the daemon's info.
func (h Home) RunDir() string { return filepath.Join(h.Root, "run") }

// Socket is the local API's Unix socket.
func (h Home) Socket() string { return filepath.Join(h.RunDir(), "signer.sock") }

// lockBase is what configlock locks: run/daemon, so the file is run/daemon.lock.
func (h Home) lockBase() string { return filepath.Join(h.RunDir(), "daemon") }

// Info is the running daemon's pid, version and API version.
func (h Home) Info() string { return filepath.Join(h.RunDir(), "daemon.json") }

// LogDir and Log are where the daemon logs.
func (h Home) LogDir() string { return filepath.Join(h.Root, "logs") }

// Log is the daemon's log file.
func (h Home) Log() string { return filepath.Join(h.LogDir(), "signer.log") }

// PrivateDir makes dir (and any parent missing) with mode 0700 and checks
// that it is a directory, not a link, owned by this user, that nobody else
// can enter: group and other bits on a directory of this user's are taken
// away, and one of another user's is refused.
func PrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("daemon: %s is not a directory of its own (a link, or a file); remove it", dir)
	}
	if uid, other := ownedByOther(info); other {
		return fmt.Errorf("daemon: %s belongs to another user (uid %d); remove it or set %s", dir, uid, EnvHome)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("daemon: making %s private: %w", dir, err)
		}
	}
	return nil
}

// ErrRunning is a daemon's start when another holds the home's lock.
var ErrRunning = errors.New("daemon: another background signer runs for this Mishmar home")
