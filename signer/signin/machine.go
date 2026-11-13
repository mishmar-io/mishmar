package signin

import (
	"os"
	"regexp"
	"runtime"
	"strings"
	"unicode"
)

// appSlug is an app's name on the command line and on the request.
var appSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidApp says whether s can name an app (--app): up to 32 lowercase
// letters, digits and dashes, not starting with a dash.
func ValidApp(s string) bool { return appSlug.MatchString(s) }

// MachineName is this computer's name as the person is likely to know it:
// the host name without a .local or .lan ending, cleaned (CleanName).
func MachineName() string {
	h, err := os.Hostname()
	if err != nil {
		return "this computer"
	}
	for _, suffix := range []string{".local", ".lan", ".localdomain"} {
		h = strings.TrimSuffix(h, suffix)
	}
	if n := CleanName(h); n != "" {
		return n
	}
	return "this computer"
}

// CleanName is a name as it may go on the request: control, bidirectional
// and zero-width characters out, spaces collapsed, at most 64 characters.
// The control plane cleans it again; the page marks it as the program's
// claim.
func CleanName(s string) string {
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff {
			continue
		}
		if n >= 64 {
			break
		}
		if space {
			if n+1 >= 64 {
				break
			}
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// Remote is what makes this machine look like one the person is not sitting
// at, or one without a browser to approve in.
type Remote struct {
	// Hints are the request's machine_hint values found: ssh, container,
	// cloud.
	Hints []string
	// Signals name what was seen (SSH_CONNECTION, /.dockerenv, …).
	Signals []string
	// NoBrowser is a Linux machine with no display to open a browser on.
	NoBrowser bool
}

// Refused says whether signing in is refused on this machine: in this
// version, whenever anything was found, since signing in by typing a code
// on another device is not built.
func (r Remote) Refused() bool { return len(r.Hints) > 0 || r.NoBrowser }

// Describe is what was found, in words: "an SSH session (SSH_CONNECTION)".
func (r Remote) Describe() string {
	var parts []string
	for _, h := range r.Hints {
		switch h {
		case "ssh":
			parts = append(parts, "an SSH session")
		case "container":
			parts = append(parts, "a container")
		case "cloud":
			parts = append(parts, "a cloud machine")
		}
	}
	if r.NoBrowser {
		parts = append(parts, "a machine without a display for a browser")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + " (" + strings.Join(r.Signals, ", ") + ")"
}

// Environment is what Detect looks at: the process environment, files, and
// the operating system. Here() is this machine's.
type Environment struct {
	Getenv   func(string) string
	Exists   func(string) bool
	ReadFile func(string) ([]byte, error)
	GOOS     string
}

// Here is this process's environment.
func Here() Environment {
	return Environment{
		Getenv:   os.Getenv,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		ReadFile: os.ReadFile,
		GOOS:     runtime.GOOS,
	}
}

// Detect looks for the signs of a remote, container or cloud machine (the
// wiki's design/app-trading-plan.md, section 2.3): SSH's variables, a
// container's marker files or Kubernetes, the variables cloud workspaces
// set, the DMI vendor of an EC2 or Google Cloud machine; and, on Linux
// outside WSL, no display to open a browser on. VS Code Remote forwards
// loopback ports, so a remote callback can look local; these signs are what
// catch it.
func Detect(e Environment) Remote {
	var r Remote
	hint := func(h, signal string) {
		r.Signals = append(r.Signals, signal)
		for _, have := range r.Hints {
			if have == h {
				return
			}
		}
		r.Hints = append(r.Hints, h)
	}
	set := func(name string) bool { return strings.TrimSpace(e.Getenv(name)) != "" }
	for _, v := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if set(v) {
			hint("ssh", v)
		}
	}
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if e.Exists != nil && e.Exists(f) {
			hint("container", f)
		}
	}
	for _, v := range []string{"KUBERNETES_SERVICE_HOST", "REMOTE_CONTAINERS"} {
		if set(v) {
			hint("container", v)
		}
	}
	for _, v := range []string{"CODESPACES", "GITPOD_WORKSPACE_ID", "CLOUD_SHELL", "REPL_ID"} {
		if set(v) {
			hint("cloud", v)
		}
	}
	if e.GOOS == "linux" && e.ReadFile != nil {
		if raw, err := e.ReadFile("/sys/class/dmi/id/sys_vendor"); err == nil {
			switch v := strings.TrimSpace(string(raw)); v {
			case "Amazon EC2", "Google":
				hint("cloud", "sys_vendor "+v)
			}
		}
		if !set("DISPLAY") && !set("WAYLAND_DISPLAY") && !wsl(e) {
			r.NoBrowser = true
			r.Signals = append(r.Signals, "no DISPLAY or WAYLAND_DISPLAY")
		}
	}
	return r
}

// wsl says whether this is Linux under Windows (WSL), where the Windows
// browser reaches the loopback port.
func wsl(e Environment) bool {
	if e.Getenv("WSL_DISTRO_NAME") != "" || e.Getenv("WSL_INTEROP") != "" {
		return true
	}
	if e.ReadFile == nil {
		return false
	}
	raw, err := e.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(raw)), "microsoft")
}
