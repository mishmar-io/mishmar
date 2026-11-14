package signin

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// env is a made-up environment: variables, files that exist, file contents.
func env(goos string, vars map[string]string, files map[string]string) Environment {
	return Environment{
		Getenv: func(k string) string { return vars[k] },
		Exists: func(p string) bool { _, ok := files[p]; return ok },
		ReadFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("no file")
		},
		GOOS: goos,
	}
}

func TestDetect(t *testing.T) {
	desktop := map[string]string{"DISPLAY": ":0"}
	cases := []struct {
		name      string
		e         Environment
		hints     []string
		noBrowser bool
	}{
		{"a Mac", env("darwin", nil, nil), nil, false},
		{"a Linux desktop", env("linux", desktop, map[string]string{"/sys/class/dmi/id/sys_vendor": "LENOVO\n"}), nil, false},
		{"Wayland", env("linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, nil), nil, false},
		{"WSL without a display", env("linux", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil), nil, false},
		{"WSL by its kernel", env("linux", nil, map[string]string{"/proc/sys/kernel/osrelease": "5.15.153.1-microsoft-standard-WSL2"}), nil, false},
		{"SSH into a Mac", env("darwin", map[string]string{"SSH_CONNECTION": "10.0.0.2 51234 10.0.0.3 22"}, nil), []string{"ssh"}, false},
		{"SSH with only a TTY", env("darwin", map[string]string{"SSH_TTY": "/dev/ttys004"}, nil), []string{"ssh"}, false},
		{"SSH into Linux with X forwarding", env("linux", map[string]string{"SSH_CLIENT": "10.0.0.2 51234 22", "DISPLAY": "localhost:10.0"}, nil), []string{"ssh"}, false},
		{"Docker", env("linux", desktop, map[string]string{"/.dockerenv": ""}), []string{"container"}, false},
		{"Podman", env("linux", desktop, map[string]string{"/run/.containerenv": ""}), []string{"container"}, false},
		{"Kubernetes", env("linux", map[string]string{"KUBERNETES_SERVICE_HOST": "10.96.0.1"}, nil), []string{"container"}, true},
		{"a VS Code dev container", env("linux", map[string]string{"REMOTE_CONTAINERS": "true", "DISPLAY": ":1"}, map[string]string{"/.dockerenv": ""}), []string{"container"}, false},
		{"Codespaces", env("linux", map[string]string{"CODESPACES": "true"}, map[string]string{"/.dockerenv": ""}), []string{"container", "cloud"}, true},
		{"Gitpod", env("linux", map[string]string{"GITPOD_WORKSPACE_ID": "x"}, nil), []string{"cloud"}, true},
		{"Cloud Shell", env("linux", map[string]string{"CLOUD_SHELL": "true"}, nil), []string{"cloud"}, true},
		{"Replit", env("linux", map[string]string{"REPL_ID": "x"}, nil), []string{"cloud"}, true},
		{"EC2", env("linux", desktop, map[string]string{"/sys/class/dmi/id/sys_vendor": "Amazon EC2\n"}), []string{"cloud"}, false},
		{"Google Cloud", env("linux", desktop, map[string]string{"/sys/class/dmi/id/sys_vendor": "Google"}), []string{"cloud"}, false},
		{"a headless Linux box", env("linux", nil, nil), nil, true},
		{"a variable set to spaces is not set", env("darwin", map[string]string{"SSH_CONNECTION": "  "}, nil), nil, false},
	}
	for _, c := range cases {
		r := Detect(c.e)
		if !reflect.DeepEqual(r.Hints, c.hints) || r.NoBrowser != c.noBrowser {
			t.Errorf("%s: hints %v no browser %v (signals %v), want %v %v", c.name, r.Hints, r.NoBrowser, r.Signals, c.hints, c.noBrowser)
		}
		if r.Refused() != (len(c.hints) > 0 || c.noBrowser) {
			t.Errorf("%s: refused %v", c.name, r.Refused())
		}
		if r.Refused() && r.Describe() == "" {
			t.Errorf("%s: refused without saying why", c.name)
		}
	}
	d := Detect(env("darwin", map[string]string{"SSH_CONNECTION": "x"}, nil)).Describe()
	if d != "an SSH session (SSH_CONNECTION)" {
		t.Fatalf("describe %q", d)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Ishai's MacBook Pro":            "Ishai's MacBook Pro",
		"  two   spaces\tand a tab  ":    "two spaces and a tab",
		"evil‮orP kooBcaM":               "evilorP kooBcaM",
		"zero​width":                     "zerowidth",
		"bell\x07":                       "bell",
		strings.Repeat("x", 80):          strings.Repeat("x", 64),
		strings.Repeat("y", 63) + " zzz": strings.Repeat("y", 63),
	} {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
	if n := MachineName(); n == "" || strings.HasSuffix(n, ".local") || len([]rune(n)) > 64 {
		t.Fatalf("machine name %q", n)
	}
	for _, ok := range []string{"claude-code", "cursor", "a", "x1-2"} {
		if !ValidApp(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-x", "Claude", "a b", "a_b", strings.Repeat("a", 33), "<your-app>"} {
		if ValidApp(bad) {
			t.Errorf("%q taken", bad)
		}
	}
}
