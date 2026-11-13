package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/mishmar-io/mishmar/signer/configlock"
)

// Daemon serves a Manager on its home's socket: one per user and home,
// held by run/daemon.lock, answering only this user's processes.
type Daemon struct {
	Manager *Manager

	// wantUID is the user a connection must come from (os.Getuid unless a
	// test sets it).
	wantUID *int
}

type connKey struct{}

// Run serves until ctx ends or a POST /shutdown is taken, then stops every
// signer (the answers in flight finish), and returns. ErrRunning: another
// daemon holds the home.
func (d *Daemon) Run(ctx context.Context) error {
	m := d.Manager
	if m.Logger == nil {
		m.Logger = slog.New(slog.DiscardHandler)
	}
	h := m.Home
	if err := PrivateDir(h.RunDir()); err != nil {
		return err
	}
	lock, err := configlock.Acquire(h.lockBase())
	if err != nil {
		var held *configlock.LockedError
		if errors.As(err, &held) {
			return ErrRunning
		}
		return err
	}
	defer lock.Close()
	sock := h.Socket()
	if limit := len(syscall.RawSockaddrUnix{}.Path) - 1; len(sock) > limit {
		return fmt.Errorf("daemon: the socket path %s is %d bytes, over the %d a Unix socket takes here; set %s to a shorter directory", sock, len(sock), limit, EnvHome)
	}
	// The lock is held, so a socket there is a dead daemon's.
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	defer os.Remove(sock)
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	if err := m.Start(ctx); err != nil {
		ln.Close()
		return err
	}
	if err := writePrivateJSON(h.Info(), info{PID: os.Getpid(), Version: m.Version, API: APIVersion, Socket: sock, StartedAt: m.startedAt}); err != nil {
		ln.Close()
		m.Stop()
		return err
	}
	defer os.Remove(h.Info())
	srv := &http.Server{
		Handler:           d.handler(stop),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext:       func(ctx context.Context, c net.Conn) context.Context { return context.WithValue(ctx, connKey{}, c) },
	}
	go func() { _ = srv.Serve(ln) }()
	m.Logger.Info("daemon: background signer running", "version", m.Version, "pid", os.Getpid(), "socket", sock)
	<-ctx.Done()
	m.Logger.Info("daemon: stopping: no new polls, the answers in flight finish")
	m.Stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		_ = srv.Close()
	}
	m.Logger.Info("daemon: stopped")
	return nil
}

func (d *Daemon) uid() int {
	if d.wantUID != nil {
		return *d.wantUID
	}
	return os.Getuid()
}

// handler checks who asks and in which version, then serves: a process of
// another user is refused (403 other_user) whatever it asks, and a client
// of another local API version (426 wrong_api_version) everything but the
// handshake, GET /health and POST /shutdown.
func (d *Daemon) handler(stop context.CancelFunc) http.Handler {
	inner := d.Manager.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := -1, errors.New("not a Unix socket connection")
		if c, ok := r.Context().Value(connKey{}).(*net.UnixConn); ok {
			uid, err = peerUID(c)
		}
		if err != nil || uid != d.uid() {
			d.Manager.Logger.Warn("daemon: refused a connection from another user", "uid", uid, "err", err)
			writeErr(w, http.StatusForbidden, "other_user", "the background signer answers only its own user's processes")
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/shutdown" {
			d.shutdown(w, r, stop)
			return
		}
		if got := r.Header.Get(HeaderAPI); r.URL.Path != "/health" && got != strconv.Itoa(APIVersion) {
			writeErr(w, http.StatusUpgradeRequired, "wrong_api_version", fmt.Sprintf("this background signer (mishmar %s) speaks local API %d, and the request was for %q; stop it (mishmar daemon stop) to let this mishmar start its own, or update the app's mishmar", d.Manager.Version, APIVersion, got))
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// shutdown takes a person's stop, or a newer binary's upgrade; an older or
// unknown binary may not replace this daemon.
func (d *Daemon) shutdown(w http.ResponseWriter, r *http.Request, stop context.CancelFunc) {
	var in ShutdownRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "validation", "body: "+err.Error())
		return
	}
	switch in.Reason {
	case "stop":
	case "upgrade":
		if !Newer(in.Version, d.Manager.Version) {
			writeErr(w, http.StatusConflict, "not_newer", fmt.Sprintf("this background signer is mishmar %s; %q is not newer, and a background signer is never downgraded", d.Manager.Version, in.Version))
			return
		}
	default:
		writeErr(w, http.StatusUnprocessableEntity, "validation", "reason is stop or upgrade")
		return
	}
	d.Manager.Logger.Info("daemon: asked to stop", "reason", in.Reason, "by", in.Version)
	writeJSON(w, http.StatusAccepted, map[string]string{"stopping": d.Manager.Version})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go stop()
}
