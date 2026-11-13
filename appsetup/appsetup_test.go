package appsetup

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite spec/vectors/app-setup.json from Text")

const vectorPath = "../spec/vectors/app-setup.json"

type rendered struct {
	App   App    `json:"app"`
	Env   string `json:"env"`
	Flags string `json:"flags"`
	Text  string `json:"text"`
}

type vectorFile struct {
	Description    string     `json:"description"`
	Package        string     `json:"package"`
	ServerName     string     `json:"serverName"`
	Texts          []rendered `json:"texts"`
	AlreadyTrading string     `json:"alreadyTrading"`
}

func TestTexts(t *testing.T) {
	pins := regexp.MustCompile(`mishmar@[0-9][0-9.]*`)
	code := regexp.MustCompile("`([^`]*)`")
	for _, app := range Apps {
		for _, env := range []string{"prod", "testnet", "stage"} {
			text, err := Text(app, env)
			if err != nil {
				t.Fatal(err)
			}
			for _, must := range []string{Intro, OwnComputer, Restart, FirstUse, SayAfter, Windows, ServerName, `"mcp","--app"`} {
				if !strings.Contains(text, must) {
					t.Fatalf("%s %s lacks %q:\n%s", app, env, must, text)
				}
			}
			for _, pin := range pins.FindAllString(text, -1) {
				if pin != Package {
					t.Fatalf("%s %s pins %s, not %s", app, env, pin, Package)
				}
			}
			if !strings.Contains(text, PythonRequirement()) {
				t.Fatalf("%s %s has no uvx fallback", app, env)
			}
			hasEnv := strings.Contains(text, "--env")
			if (env == "prod") == hasEnv || (env != "prod" && !strings.Contains(text, `"--env","`+env+`"`)) {
				t.Fatalf("%s %s: --env wrong:\n%s", app, env, text)
			}
			// Every JSON in it parses: a person pastes it.
			for _, m := range code.FindAllStringSubmatch(text, -1) {
				s := m[1]
				if strings.HasPrefix(s, `"`+ServerName+`": `) {
					s = "{" + s + "}"
				}
				if strings.HasPrefix(s, "{") && !json.Valid([]byte(s)) {
					t.Fatalf("%s %s: not JSON: %s", app, env, s)
				}
				if strings.HasPrefix(s, "code --add-mcp '") && !json.Valid([]byte(strings.TrimSuffix(strings.TrimPrefix(s, "code --add-mcp '"), "'"))) {
					t.Fatalf("%s %s: not JSON: %s", app, env, s)
				}
			}
		}
	}
	if !strings.Contains(mustText(t, ClaudeCode, "prod"), "claude mcp add --scope user mishmar-trade -- npx -y mishmar@0.5 mcp --app claude-code`") {
		t.Fatal("the Claude Code command")
	}
	if !strings.Contains(mustText(t, Other, "prod"), "npx -y mishmar@0.5 mcp --app <your-app>") {
		t.Fatal("the placeholder for another app")
	}
	if PythonRequirement() != "mishmar>=0.5,<0.6" {
		t.Fatalf("python requirement %s", PythonRequirement())
	}
	if _, err := Text("windsurf", "prod"); err == nil {
		t.Fatal("an app without steps")
	}
	if _, err := Text(ClaudeCode, "dev"); err == nil {
		t.Fatal("an environment that is not hosted")
	}
}

func mustText(t *testing.T, app App, env string) string {
	t.Helper()
	s, err := Text(app, env)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestVectors keeps spec/vectors/app-setup.json equal to the texts. `go test
// ./appsetup -update` rewrites it.
func TestVectors(t *testing.T) {
	file := vectorFile{
		Description:    "The setup text for trading from an AI app (appsetup.Text), per app and hosted environment: what the hosted MCP server's set_up_trading tool and the console show. Rendered from the appsetup package; a copy elsewhere is checked against this file. package is the npm package pin every text runs; flags the server's arguments after mcp. alreadyTrading is AlreadyTrading for the machine 'Ishai's MacBook Pro', signed in 13 Nov 2026, code AS3N-N9TM.",
		Package:        Package,
		ServerName:     ServerName,
		AlreadyTrading: AlreadyTrading("Ishai's MacBook Pro", time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC), "AS3N-N9TM"),
	}
	for _, app := range Apps {
		for _, env := range []string{"prod", "testnet", "stage"} {
			file.Texts = append(file.Texts, rendered{App: app, Env: env, Flags: strings.Join(Flags(app, env), " "), Text: mustText(t, app, env)})
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(file); err != nil {
		t.Fatal(err)
	}
	want := buf.Bytes()
	if *update {
		if err := os.WriteFile(vectorPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatalf("%v (run `go test ./appsetup -update` to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("spec/vectors/app-setup.json is out of date: run `go test ./appsetup -update`")
	}
}
