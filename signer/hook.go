package signer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/mishmar-io/mishmar/api"
)

// DefaultHookTimeout bounds the hook: an order waits for it, and the engine
// hands the request out again after ten seconds.
const DefaultHookTimeout = 2 * time.Second

// maxHookOutput bounds what is kept of the hook's output for the reason.
const maxHookOutput = 4096

// RunHook runs command (through the shell) with the request's JSON on
// stdin. Exit 0 lets the request through; anything else refuses it, with
// the first line the command printed (or the error) as the reason, and a
// command that outlives the timeout is killed and refused as a timeout.
func RunHook(ctx context.Context, command string, req api.SignRequest, timeout time.Duration) *Refusal {
	if timeout <= 0 {
		timeout = DefaultHookTimeout
	}
	input, err := json.Marshal(req)
	if err != nil {
		return &Refusal{RefuseHook, "could not encode the request: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Stdin = bytes.NewReader(input)
	out := &boundedBuffer{limit: maxHookOutput}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Refusal{RefuseHookTimeout, "the hook did not finish within " + timeout.String()}
	}
	reason := firstLine(out.String())
	if reason == "" {
		reason = err.Error()
	}
	return &Refusal{RefuseHook, reason}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// boundedBuffer keeps the first limit bytes written and drops the rest.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }
