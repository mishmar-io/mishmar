package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/mcp"
	"github.com/mishmar-io/mishmar/signer"
	"github.com/mishmar-io/mishmar/signer/daemon"
	"github.com/mishmar-io/mishmar/signer/signin"
	"github.com/mishmar-io/mishmar/signer/signin/signintest"
	"golang.org/x/sys/unix"
)

// TestMain lets this test binary be the mishmar binary for the test that
// starts the background signer as a real detached process.
func TestMain(m *testing.M) {
	if os.Getenv("MISHMAR_TEST_RUN_CLI") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// appWorld is what an app's server sees in a test: a short Mishmar home,
// the stand-in control plane and engine, a browser that the test plays, a
// machine that is not remote, and a background signer started in this
// process (its starts counted, and stopped when the test ends).
type appWorld struct {
	t      *testing.T
	home   daemon.Home
	cp     *signintest.Server
	opened chan string
	starts atomic.Int32
	ran    atomic.Int32
	mu     sync.Mutex
	stops  []func()
	remote signin.Remote
}

func newAppWorld(t *testing.T) *appWorld {
	t.Helper()
	dir, err := os.MkdirTemp("", "mm")
	if err != nil {
		t.Fatal(err)
	}
	w := &appWorld{t: t, home: daemon.Home{Root: dir}, cp: signintest.New(), opened: make(chan string, 8)}
	t.Setenv(daemon.EnvHome, dir)
	t.Setenv(envNoBrowser, "1")
	saved := []any{openBrowser, detectRemote, startDaemon}
	openBrowser = func(link string) error { w.opened <- link; return nil }
	detectRemote = func() signin.Remote { return w.remote }
	startDaemon = w.start
	t.Cleanup(func() {
		openBrowser, detectRemote, startDaemon = saved[0].(func(string) error), saved[1].(func() signin.Remote), saved[2].(func(context.Context, daemon.Home) error)
		w.mu.Lock()
		for _, stop := range w.stops {
			stop()
		}
		w.mu.Unlock()
		w.cp.Close()
		os.RemoveAll(dir)
	})
	return w
}

// start runs a background signer in this process the way Spawn starts one.
func (w *appWorld) start(_ context.Context, home daemon.Home) error {
	w.starts.Add(1)
	d := &daemon.Daemon{Manager: &daemon.Manager{
		Home: home, Version: version, Logger: slog.New(slog.DiscardHandler),
		Detect: func() signin.Remote { return signin.Remote{} },
		Tune: func(s *signer.Signer) {
			s.PollWait, s.KeySyncInterval, s.PermissionInterval = time.Second, 20*time.Millisecond, 50*time.Millisecond
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	w.mu.Lock()
	w.stops = append(w.stops, func() { cancel(); <-done })
	w.mu.Unlock()
	go func() {
		defer close(done)
		if err := d.Run(ctx); err == nil {
			w.ran.Add(1)
		}
	}()
	return nil
}

// approveWhenOpened plays the person: when the server opens a link, open it
// and approve on the page.
func (w *appWorld) approveWhenOpened() {
	go func() {
		link, ok := <-w.opened
		if !ok {
			return
		}
		id, err := w.cp.Open(link)
		if err != nil {
			w.t.Errorf("the browser: %v", err)
			return
		}
		if _, err := w.cp.Approve(id, signintest.Approval{}); err != nil {
			w.t.Errorf("approving: %v", err)
		}
	}()
}

// mcpSession is one `mishmar mcp --app` run, driven one request at a time.
type mcpSession struct {
	t     *testing.T
	in    *io.PipeWriter
	out   *bufio.Scanner
	done  chan error
	seq   int
	close func()
}

func (w *appWorld) session(args ...string) *mcpSession {
	w.t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	root := newRootCmd()
	root.SetIn(inR)
	root.SetOut(outW)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"mcp", "--log-level", "error"}, args...))
	s := &mcpSession{t: w.t, in: inW, out: bufio.NewScanner(outR), done: make(chan error, 1)}
	s.out.Buffer(make([]byte, 0, 64<<10), 4<<20)
	go func() {
		s.done <- root.ExecuteContext(context.Background())
		outW.Close()
	}()
	s.close = func() {
		inW.Close()
		select {
		case err := <-s.done:
			if err != nil {
				w.t.Errorf("mcp: %v", err)
			}
		case <-time.After(20 * time.Second):
			w.t.Error("mcp did not stop when its stdin closed")
		}
	}
	w.t.Cleanup(s.close)
	s.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	return s
}

type rpcAnswer struct {
	Result struct {
		Instructions string `json:"instructions"`
		Tools        []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *mcpSession) call(method string, params any) rpcAnswer {
	s.t.Helper()
	s.seq++
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.seq, "method": method, "params": params})
	if _, err := s.in.Write(append(req, '\n')); err != nil {
		s.t.Fatal(err)
	}
	lines := make(chan string, 1)
	go func() {
		if s.out.Scan() {
			lines <- s.out.Text()
		}
		close(lines)
	}()
	select {
	case line, ok := <-lines:
		if !ok {
			s.t.Fatalf("%s: the server closed its output", method)
		}
		var a rpcAnswer
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			s.t.Fatalf("%s: %v: %s", method, err, line)
		}
		return a
	case <-time.After(60 * time.Second):
		s.t.Fatalf("%s: no answer", method)
	}
	return rpcAnswer{}
}

// tool calls a tool and returns its text and whether it is an error.
func (s *mcpSession) tool(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	a := s.call("tools/call", map[string]any{"name": name, "arguments": args})
	if a.Error != nil {
		s.t.Fatalf("%s: %s", name, a.Error.Message)
	}
	if len(a.Result.Content) == 0 {
		s.t.Fatalf("%s: no content", name)
	}
	return a.Result.Content[0].Text, a.Result.IsError
}

// An app's server, end to end against the stand-ins of the control plane
// and the engine: it lists its tools without touching anything; the status
// before sign-in starts nothing; the first tool that needs Mishmar starts
// the background signer and the sign-in, opens the browser, waits for the
// person, and then runs; the trading key is made by the background signer
// and the status says what the person does at the exchange, then that it is
// ready; a proposal lives 15 minutes by default and goes through the
// daemon; a second session of the same app shares the daemon and the agent.
func TestMCPAppEndToEnd(t *testing.T) {
	w := newAppWorld(t)
	s := w.session("--app", "claude-code", "--api", w.cp.URL, "--env", signintest.Environment)
	init := s.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if !strings.Contains(init.Result.Instructions, mcp.AppTrading) {
		t.Fatal("the instructions lack the app's part")
	}
	list := s.call("tools/list", nil)
	var names []string
	var validFor string
	for _, tl := range list.Result.Tools {
		names = append(names, tl.Name)
		if tl.Name == "propose_order" {
			validFor = tl.InputSchema["properties"].(map[string]any)["validForSeconds"].(map[string]any)["description"].(string)
		}
	}
	if len(names) != 17 || names[16] != mcp.StatusToolName || validFor != mcp.AppValidForDesc {
		t.Fatalf("tools %v, validForSeconds %q", names, validFor)
	}
	if w.starts.Load() != 0 {
		t.Fatal("listing the tools started the background signer")
	}

	text, isErr := s.tool(mcp.StatusToolName, nil)
	if isErr || !strings.Contains(text, "is not signed in to Mishmar for claude-code") || !strings.Contains(text, mcp.StatusNoDaemon) || w.starts.Load() != 0 {
		t.Fatalf("status before sign-in (%d starts): %s", w.starts.Load(), text)
	}

	// The first tool signs in: the person approves while the call waits.
	w.approveWhenOpened()
	text, isErr = s.tool("list_connected_accounts", nil)
	if isErr || strings.TrimSpace(text) != "[]" {
		t.Fatalf("the first tool: %v %s", isErr, text)
	}
	if w.starts.Load() != 1 {
		t.Fatalf("%d background signers started", w.starts.Load())
	}
	cfg, err := signer.Load(w.home.Config("claude-code"))
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(w.home.Config("claude-code")); info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %o", info.Mode().Perm())
	}

	// The status follows the trading key: made here, waiting at Binance,
	// then ready.
	var status string
	eventually(t, "the trading key is made", func() bool {
		status, _ = s.tool(mcp.StatusToolName, nil)
		return strings.Contains(status, "waits at Binance")
	})
	code := w.cp.Requests()[0].Code
	for _, want := range []string{"Signed in to Mishmar for claude-code as agent Claude Code", "key code " + code, "-----BEGIN PUBLIC KEY-----", "Main · USDⓈ-M Futures (ta_standin)", "runs in the background", "not after the computer restarts"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}
	w.cp.Engine.Activate(w.cp.Engine.VenueKeys()[0].Id)
	eventually(t, "ready", func() bool {
		status, _ = s.tool(mcp.StatusToolName, nil)
		return strings.Contains(status, "Ready to trade on Main · USDⓈ-M Futures")
	})

	// A proposal through the daemon, 15 minutes by default.
	text, isErr = s.tool("propose_order", map[string]any{"connectedAccountId": signintest.ConnectedAccountID, "symbol": "BTCUSDT", "side": "buy", "size": "0.01", "price": "50000"})
	if isErr || !strings.Contains(text, "require_approval") {
		t.Fatalf("propose: %s", text)
	}
	props := w.cp.Engine.Proposals()
	if len(props) != 1 || props[0].ExpiresAt.Sub(props[0].ProposedAt) != 15*time.Minute || props[0].KeyId != cfg.KeyID {
		t.Fatalf("proposals %+v", props)
	}
	text, _ = s.tool("propose_order", map[string]any{"connectedAccountId": signintest.ConnectedAccountID, "symbol": "BTCUSDT", "side": "buy", "size": "0.01", "price": "50000", "validForSeconds": 7200})
	if props = w.cp.Engine.Proposals(); len(props) != 2 || props[1].ExpiresAt.Sub(props[1].ProposedAt) != 2*time.Hour {
		t.Fatalf("a life the model set: %s %+v", text, props)
	}

	// A second session of the same app: the same daemon, the same agent, no
	// second sign-in.
	s2 := w.session("--app", "claude-code", "--api", w.cp.URL, "--env", signintest.Environment)
	text, isErr = s2.tool("get_identity", nil)
	if isErr || !strings.Contains(text, cfg.AgentID) || !strings.Contains(text, cfg.KeyID) {
		t.Fatalf("the second session: %s", text)
	}
	if n := len(w.cp.Requests()); n != 1 {
		t.Fatalf("%d sign-ins", n)
	}
	if w.starts.Load() != 1 {
		t.Fatalf("the second session started a background signer of its own (%d starts)", w.starts.Load())
	}

	// Disconnected in the console: the next tool says so once, then signs in again.
	w.cp.Engine.Revoke(cfg.KeyID)
	eventually(t, "the end is seen", func() bool {
		status, _ = s.tool(mcp.StatusToolName, nil)
		return strings.Contains(status, "no longer takes this computer's key")
	})
	text, isErr = s.tool("list_positions", nil)
	if !isErr || !strings.Contains(text, "Key revoked") {
		t.Fatalf("the first tool after the end: %s", text)
	}
	appWait = 300 * time.Millisecond
	t.Cleanup(func() { appWait = 30 * time.Second })
	s3 := w.session("--app", "claude-code", "--api", w.cp.URL, "--env", signintest.Environment)
	text, isErr = s3.tool("list_positions", nil)
	if !isErr || !strings.Contains(text, "Key revoked") {
		t.Fatalf("another session is told once too: %s", text)
	}
	text, isErr = s3.tool("list_positions", nil)
	if !isErr || !strings.Contains(text, "This computer is not signed in to Mishmar yet. I opened Mishmar in your browser to approve it: "+w.cp.URL) {
		t.Fatalf("the second tool after the end: %s", text)
	}
	if len(w.cp.Requests()) != 1 || len(w.opened) != 1 {
		t.Fatalf("the new sign-in's link was not opened once: %d requests, %d links", len(w.cp.Requests()), len(w.opened))
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A machine that looks remote is told so, and nothing starts: no background
// signer, no sign-in, no file.
func TestMCPAppRefusesARemoteMachine(t *testing.T) {
	w := newAppWorld(t)
	w.remote = signin.Remote{Hints: []string{"ssh"}, Signals: []string{"SSH_CONNECTION"}}
	s := w.session("--app", "cursor", "--api", w.cp.URL)
	for _, tool := range []string{"list_connected_accounts", mcp.StatusToolName} {
		text, _ := s.tool(tool, nil)
		if !strings.Contains(text, "not supported yet: it looks like an SSH session (SSH_CONNECTION)") || !strings.Contains(text, "Nothing was started") {
			t.Fatalf("%s: %s", tool, text)
		}
	}
	if w.starts.Load() != 0 || len(w.cp.Requests()) != 0 {
		t.Fatalf("%d starts, %d sign-ins", w.starts.Load(), len(w.cp.Requests()))
	}
	if _, err := os.Stat(w.home.AppsDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("files were made: %v", err)
	}
}

// Nobody approves within the call's wait: the tool answers with the link and
// the code, and the sign-in goes on for the next call.
func TestMCPAppAnswersWithTheLinkWhileThePersonApproves(t *testing.T) {
	w := newAppWorld(t)
	openBrowser = func(string) error { return errors.New("no browser here") }
	appWait = time.Second
	t.Cleanup(func() { appWait = 30 * time.Second })
	s := w.session("--app", "cursor", "--api", w.cp.URL)
	start := time.Now()
	text, isErr := s.tool("get_identity", nil)
	if !isErr || !strings.Contains(text, "Open this link in your browser to approve it: "+w.cp.URL+"/oauth/authorize?") || time.Since(start) < time.Second {
		t.Fatalf("after %s: %s", time.Since(start), text)
	}
	code := w.cp.Requests()
	if len(code) != 0 {
		t.Fatalf("the stand-in saw a request nobody opened: %v", code)
	}
	if !strings.Contains(text, "Check the page shows this code: ") {
		t.Fatalf("no code: %s", text)
	}
	// The next call picks the same sign-in up: the same link, no second start.
	again, _ := s.tool("get_identity", nil)
	link := text[strings.Index(text, w.cp.URL):]
	link = link[:strings.Index(link, ". Check")]
	if !strings.Contains(again, link) {
		t.Fatalf("a second sign-in started: %s / %s", text, again)
	}
	// The person approves now; the next call runs.
	id, err := w.cp.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.cp.Approve(id, signintest.Approval{}); err != nil {
		t.Fatal(err)
	}
	if text, isErr := s.tool("get_identity", nil); isErr || !strings.Contains(text, `"agentName":"Cursor"`) {
		t.Fatalf("after the approval: %s", text)
	}
}

// The bot path is untouched: --env and --api need --app, and --app takes no
// --config.
func TestMCPAppFlags(t *testing.T) {
	for _, args := range [][]string{
		{"mcp", "--env", "testnet"},
		{"mcp", "--api", "http://localhost:8090"},
		{"mcp", "--app", "claude-code", "--config", "/tmp/x.json"},
		{"mcp", "--app", "Claude Code"},
		{"mcp", "--app", "claude-code", "--env", "dev"},
		{"mcp", "--app", "claude-code", "--api", "https://api.mishmar.io/v1"},
	} {
		if _, err := execute(context.Background(), t, strings.NewReader(""), 10*time.Second, args...); err == nil {
			t.Fatalf("mishmar %s ran", strings.Join(args, " "))
		}
	}
}

// The background signer started for real: a detached process of this test
// binary (as `mishmar signer daemon`), in its own session, answering on the
// socket, logging to the home's log, and gone after `mishmar daemon stop`.
func TestTheBackgroundSignerRunsDetached(t *testing.T) {
	dir, err := os.MkdirTemp("", "mm")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	home := daemon.Home{Root: dir}
	t.Setenv("MISHMAR_TEST_RUN_CLI", "1")
	_, h, err := daemon.Ensure(context.Background(), daemon.EnsureOptions{Home: home, Version: version, Start: func(ctx context.Context) error { return startDaemon(ctx, home) }})
	if err != nil {
		t.Fatal(err)
	}
	pid := h.PID
	t.Cleanup(func() {
		if syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if pid == os.Getpid() || pid <= 0 {
		t.Fatalf("pid %d", pid)
	}
	mySID, _ := unix.Getsid(0)
	if sid, err := unix.Getsid(pid); err != nil || sid == mySID || sid != pid {
		t.Fatalf("the daemon's session %d (%v), this one's %d: not detached", sid, err, mySID)
	}
	if info, err := os.Stat(home.Log()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log: %v", err)
	}
	out, err := execute(context.Background(), t, strings.NewReader(""), 20*time.Second, "daemon", "status", "--home", dir)
	if err != nil || !strings.Contains(out, "Background signer: mishmar "+version) || !strings.Contains(out, "No app is signed in") {
		t.Fatalf("status: %v %s", err, out)
	}
	out, err = execute(context.Background(), t, strings.NewReader(""), 30*time.Second, "daemon", "stop", "--home", dir)
	if err != nil || !strings.Contains(out, "The background signer stopped.") {
		t.Fatalf("stop: %v %s", err, out)
	}
	eventually(t, "the process is gone", func() bool { return syscall.Kill(pid, 0) != nil })
	out, _ = execute(context.Background(), t, strings.NewReader(""), 20*time.Second, "daemon", "status", "--home", dir)
	if !strings.Contains(out, "No background signer is running") {
		t.Fatalf("status after stop: %s", out)
	}
}

// Where no background signer can run (Windows, a sandbox; here forced with
// MISHMAR_SIGNER_DAEMON=off), the app's signer runs inside its MCP server:
// the same sign-in, and the status says approvals need the app open.
func TestMCPAppWithoutTheBackgroundSigner(t *testing.T) {
	w := newAppWorld(t)
	t.Setenv(envDaemon, "off")
	s := w.session("--app", "vscode", "--api", w.cp.URL, "--env", signintest.Environment)
	w.approveWhenOpened()
	if text, isErr := s.tool("get_identity", nil); isErr || !strings.Contains(text, `"keyScope":"trade"`) {
		t.Fatalf("get_identity: %s", text)
	}
	text, _ := s.tool(mcp.StatusToolName, nil)
	if !strings.Contains(text, "runs inside this app") || !strings.Contains(text, "only while the app is open") || w.starts.Load() != 0 {
		t.Fatalf("status (%d starts): %s", w.starts.Load(), text)
	}
	if _, err := os.Stat(w.home.Config("vscode")); err != nil {
		t.Fatal(err)
	}
}

// After a restart of the computer the background signer is gone; the app's
// key is still here, so the status (or any tool) starts it again, and the
// app is signed in as before, with no new sign-in.
func TestMCPAppAfterARestart(t *testing.T) {
	w := newAppWorld(t)
	s := w.session("--app", "claude-code", "--api", w.cp.URL, "--env", signintest.Environment)
	w.approveWhenOpened()
	if text, isErr := s.tool("get_identity", nil); isErr {
		t.Fatal(text)
	}
	w.mu.Lock()
	for _, stop := range w.stops {
		stop()
	}
	w.stops = nil
	w.mu.Unlock()
	s2 := w.session("--app", "claude-code", "--api", w.cp.URL, "--env", signintest.Environment)
	text, _ := s2.tool(mcp.StatusToolName, nil)
	if !strings.Contains(text, "Signed in to Mishmar for claude-code") || w.starts.Load() != 2 || len(w.cp.Requests()) != 1 {
		t.Fatalf("after the restart (%d starts, %d sign-ins): %s", w.starts.Load(), len(w.cp.Requests()), text)
	}
}
