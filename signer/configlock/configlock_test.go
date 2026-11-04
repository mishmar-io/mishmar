package configlock

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecondHolderRefusedUntilClose(t *testing.T) {
	config := filepath.Join(t.TempDir(), "agent.json")
	first, err := Acquire(config)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(Path(config)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file: %v %v", fi, err)
	}

	_, err = Acquire(config)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("second Acquire: %v, want a LockedError", err)
	}
	if locked.PID != os.Getpid() || locked.Config != config {
		t.Fatalf("LockedError %+v, want pid %d", locked, os.Getpid())
	}
	if msg := err.Error(); !strings.Contains(msg, config) || !strings.Contains(msg, "Signer.connect") {
		t.Fatalf("message %q", msg)
	}
	if pid, held, err := Holder(config); err != nil || !held || pid != os.Getpid() {
		t.Fatalf("Holder = %d %v %v", pid, held, err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, held, err := Holder(config); err != nil || held {
		t.Fatalf("Holder after Close: held %v, %v", held, err)
	}
	again, err := Acquire(config)
	if err != nil {
		t.Fatalf("Acquire after Close: %v", err)
	}
	again.Close()
}

func TestHolderOfAConfigNeverLocked(t *testing.T) {
	if _, held, err := Holder(filepath.Join(t.TempDir(), "agent.json")); err != nil || held {
		t.Fatalf("held %v, %v", held, err)
	}
}

// The lock goes with the process that held it, however it ends.
func TestReleasedWhenTheHolderIsKilled(t *testing.T) {
	config := filepath.Join(t.TempDir(), "agent.json")
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldsTheLock")
	cmd.Env = append(os.Environ(), "CONFIGLOCK_HELPER="+config)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper said %q, %v", line, err)
	}
	if _, err := Acquire(config); !errors.As(err, new(*LockedError)) {
		t.Fatalf("Acquire while the helper holds it: %v", err)
	} else if pid, _, _ := Holder(config); pid != cmd.Process.Pid {
		t.Fatalf("holder pid %d, want the helper's %d", pid, cmd.Process.Pid)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l, err := Acquire(config)
		if err == nil {
			l.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still locked after the holder died: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHelperHoldsTheLock(t *testing.T) {
	config := os.Getenv("CONFIGLOCK_HELPER")
	if config == "" {
		t.Skip("helper process for TestReleasedWhenTheHolderIsKilled")
	}
	if _, err := Acquire(config); err != nil {
		os.Stdout.WriteString("error " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("locked\n")
	time.Sleep(time.Minute)
	os.Exit(0)
}
