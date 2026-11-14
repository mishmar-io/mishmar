package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/mishmar-io/mishmar/signer/configlock"
)

// EnsureOptions is how an MCP server finds the daemon, or starts it.
type EnsureOptions struct {
	Home Home
	// Version is the asking binary's: a daemon older than it is replaced,
	// one newer is used as it is (never downgraded).
	Version string
	// Start starts a daemon for the home, detached from the caller; nil is
	// Spawn of this executable (`mishmar signer daemon --home <root>`).
	Start func(ctx context.Context) error
	// Wait is how long a daemon has to answer once started (10 s).
	Wait   time.Duration
	Logger *slog.Logger
}

// Ensure is a client of a daemon of the home that this binary can use:
// the one running, unless it is older than this binary (then it is asked to
// stop, finishes its answers in flight, and this binary's is started), or
// one started now. Two callers at once share one daemon: the home's lock
// lets one run and the other's start ends at once.
func Ensure(ctx context.Context, o EnsureOptions) (*Client, Health, error) {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Wait <= 0 {
		o.Wait = 10 * time.Second
	}
	start := o.Start
	if start == nil {
		start = func(context.Context) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			return Spawn(o.Home, exe, "signer", "daemon", "--home", o.Home.Root)
		}
	}
	c := Dial(o.Home.Socket())
	if h, err := health(ctx, c, 2*time.Second); err == nil {
		if !Newer(o.Version, h.Version) {
			return usable(c, h, o)
		}
		o.Logger.Info("daemon: replacing an older background signer", "running", h.Version, "this", o.Version)
		err := c.Shutdown(ctx, ShutdownRequest{Reason: "upgrade", Version: o.Version})
		switch {
		case IsCode(err, "not_newer"):
			return usable(c, h, o)
		case err != nil:
			return nil, h, fmt.Errorf("daemon: asking the background signer %s to make way: %w", h.Version, err)
		}
		if err := gone(ctx, c, o.Home, o.Wait+DrainTimeout); err != nil {
			return nil, h, err
		}
	} else if _, held, _ := configlock.Holder(o.Home.lockBase()); held {
		// A daemon is starting, or stopping: wait for it to answer, or to
		// let go of the home.
		deadline := time.Now().Add(o.Wait + DrainTimeout)
		for {
			if h, err := health(ctx, c, 500*time.Millisecond); err == nil {
				if !Newer(o.Version, h.Version) {
					return usable(c, h, o)
				}
				return Ensure(ctx, o)
			}
			if _, held, _ := configlock.Holder(o.Home.lockBase()); !held {
				break
			}
			if time.Now().After(deadline) {
				return nil, Health{}, errors.New("daemon: a background signer holds the home but does not answer; stop it (mishmar daemon stop) or end its process")
			}
			select {
			case <-ctx.Done():
				return nil, Health{}, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	if err := start(ctx); err != nil {
		return nil, Health{}, fmt.Errorf("daemon: starting the background signer: %w", err)
	}
	deadline := time.Now().Add(o.Wait)
	for {
		h, err := health(ctx, c, time.Second)
		if err == nil {
			return usable(c, h, o)
		}
		if time.Now().After(deadline) {
			return nil, Health{}, fmt.Errorf("daemon: the background signer did not answer within %s (its log: %s): %w", o.Wait, o.Home.Log(), err)
		}
		select {
		case <-ctx.Done():
			return nil, Health{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// usable takes a running daemon if it speaks this binary's local API.
func usable(c *Client, h Health, o EnsureOptions) (*Client, Health, error) {
	if h.API != APIVersion {
		return nil, h, fmt.Errorf("daemon: the background signer running is mishmar %s, which speaks local API %d, and this mishmar %s speaks %d; stop it (mishmar daemon stop) or update the app's mishmar", h.Version, h.API, o.Version, APIVersion)
	}
	return c, h, nil
}

func health(ctx context.Context, c *Client, timeout time.Duration) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Health(ctx)
}

// gone waits until the daemon at c no longer answers and has let go of the
// home's lock, so the next one can take it.
func gone(ctx context.Context, c *Client, h Home, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		if _, err := health(ctx, c, 500*time.Millisecond); err != nil {
			if _, held, _ := configlock.Holder(h.lockBase()); !held {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("daemon: the older background signer did not stop in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Stop asks the home's daemon to stop and waits until it has: what `mishmar
// daemon stop` does. False when none was running.
func Stop(ctx context.Context, h Home) (bool, error) {
	c := Dial(h.Socket())
	if _, err := health(ctx, c, 2*time.Second); err != nil {
		return false, nil
	}
	if err := c.Shutdown(ctx, ShutdownRequest{Reason: "stop"}); err != nil {
		return true, err
	}
	return true, gone(ctx, c, h, DrainTimeout+10*time.Second)
}
