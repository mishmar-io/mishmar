//go:build darwin || linux

package daemon

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Supported says whether a background signer runs on this platform.
const Supported = true

// maxLog is how large the log grows before it is moved to signer.log.1.
const maxLog = 10 << 20

// Spawn starts exe with args as a background process of its own: a new
// session, so the app that started it (and its process group) can end
// without taking it along; stdin from /dev/null and stdout and stderr to
// the home's log, so the MCP protocol's pipes are never held open; the root
// directory as its working directory; the environment without the agent's
// API key, which it does not use. Nothing is installed to start it again.
func Spawn(h Home, exe string, args ...string) error {
	if err := PrivateDir(h.LogDir()); err != nil {
		return err
	}
	if info, err := os.Stat(h.Log()); err == nil && info.Size() > maxLog {
		_ = os.Rename(h.Log(), h.Log()+".1")
	}
	log, err := os.OpenFile(h.Log(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr, cmd.Dir = log, log, "/"
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "MISHMAR_API_KEY=") && !strings.HasPrefix(kv, "MISHMAR_LOCAL_TOKEN=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reaped when it ends, if this process is still there.
	go func() { _ = cmd.Wait() }()
	return nil
}
