// Package appsetup holds the text that tells an AI app how to start trading
// through Mishmar on the person's computer: what the hosted MCP server's
// set_up_trading tool answers and what the console shows when a person turns
// trading on for an app (the wiki's design/app-trading-plan.md, section 2.5).
// It is the one place the text lives. It imports only the standard library,
// so the hosted control plane can import it, or copy this file; either way
// spec/vectors/app-setup.json pins the rendered texts, the way the signing
// vectors pin bytes, and a copy is checked against it.
//
// The text is written to the model, which relays the steps to the person.
// It says plainly that a server added during a conversation is not usable
// in it: the app has to restart, or the conversation continue in a new
// session (seen on 13 Nov 2026 with Claude Code).
package appsetup

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Package is what every app runs: the npm package (and the PyPI one, by the
// same version) pinned to a minor release, so patch releases arrive by
// themselves. Bumped with each minor release of the CLI; an installed
// app keeps the pin it was added with.
const Package = "mishmar@0.5"

// ServerName is the name the trading server is added under in every app.
const ServerName = "mishmar-trade"

// App is an AI app the text is written for; its value is the --app slug the
// server runs with, and the name the person's computer is approved for.
type App string

// The apps with their own steps. Other covers any MCP client that runs a
// local (stdio) server.
const (
	ClaudeCode    App = "claude-code"
	Cursor        App = "cursor"
	VSCode        App = "vscode"
	ClaudeDesktop App = "claude-desktop"
	Other         App = "other"
)

// Apps lists them in the order they are offered.
var Apps = []App{ClaudeCode, Cursor, VSCode, ClaudeDesktop, Other}

// The environments a hosted account lives in. prod needs no flag.
var environments = map[string]bool{"prod": true, "testnet": true, "stage": true}

// Intro opens every text.
const Intro = "Trading through Mishmar needs Mishmar's signer on the person's computer. It makes and keeps their half of each trading key there; Mishmar never holds it, and every order is still checked against their limits."

// OwnComputer is what may trade: a machine the person sits at. Signing in by
// typing a code on another device is not built, so a remote machine is
// refused by the server itself; the text says so first.
const OwnComputer = "It has to be the person's own computer, with a browser: a machine reached over SSH, a container or a cloud workspace cannot trade through Mishmar yet."

// Restart is the honest part: a server added during a conversation is not
// in it.
const Restart = "The new server is usable after the app restarts or the conversation continues in a new session, not before: tools added during a conversation never reach it."

// FirstUse is what happens once the server runs.
const FirstUse = "The first Mishmar trading tool opens the browser to approve this computer and answers with a code; the page must show the same code, and the person approves only if it does. Then the console shows the one step left at the exchange, and the signer keeps running in the background after the app closes, until the computer restarts."

// Windows is what an app on Windows is told: the mishmar release has no
// Windows build yet.
const Windows = "On Windows, run the app inside WSL (the Linux steps work there): Mishmar's signer has no Windows release yet."

// SayAfter is what the person says to the app once the server is in.
const SayAfter = "set up trading with Mishmar"

// Flags is the server's arguments after mcp: --app and the slug, and
// --env outside prod.
func Flags(app App, env string) []string {
	out := []string{"--app", slug(app)}
	if env != "" && env != "prod" {
		out = append(out, "--env", env)
	}
	return out
}

// Args is the npx arguments: -y, the package, mcp and Flags.
func Args(app App, env string) []string {
	return append([]string{"-y", Package, "mcp"}, Flags(app, env)...)
}

// PythonRequirement is Package as a pip requirement (mishmar>=0.5,<0.6),
// for uvx.
func PythonRequirement() string {
	name, ver, _ := strings.Cut(Package, "@")
	major, minor, _ := strings.Cut(ver, ".")
	var m int
	_, _ = fmt.Sscanf(minor, "%d", &m)
	return fmt.Sprintf("%s>=%s.%d,<%s.%d", name, major, m, major, m+1)
}

// server is a stdio server's entry, its fields in the order people write them.
type server struct {
	Name    string   `json:"name,omitempty"`
	Type    string   `json:"type,omitempty"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func npxServer(app App, env string) server { return server{Command: "npx", Args: Args(app, env)} }

func uvxServer(app App, env string) server {
	return server{Command: "uvx", Args: append([]string{"--from", PythonRequirement(), "mishmar", "mcp"}, Flags(app, env)...)}
}

// Entry is the server's entry as an app's mcpServers map takes it.
func Entry(app App, env string) string { return compact(npxServer(app, env)) }

// UVXEntry is the same through uvx, for a computer without Node.js.
func UVXEntry(app App, env string) string { return compact(uvxServer(app, env)) }

// inFile is the server under ServerName in a config file's map (mcpServers,
// or VS Code's servers).
func inFile(key string, s server) string {
	return compact(map[string]map[string]server{key: {ServerName: s}})
}

// slug is the --app value: the app's own, or a placeholder the model fills
// in for an app without its own steps.
func slug(app App) string {
	if app == Other || app == "" {
		return "<your-app>"
	}
	return string(app)
}

func command(app App, env string) string {
	return "npx " + strings.Join(Args(app, env), " ")
}

func compact(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}

// Text is the setup text for an app in an environment (prod, testnet or
// stage; "" is prod).
func Text(app App, env string) (string, error) {
	if env == "" {
		env = "prod"
	}
	if !environments[env] {
		return "", fmt.Errorf("appsetup: environment %q is not prod, testnet or stage", env)
	}
	var steps string
	switch app {
	case ClaudeCode:
		steps = claudeCode(env)
	case Cursor:
		steps = cursor(env)
	case VSCode:
		steps = vscode(env)
	case ClaudeDesktop:
		steps = claudeDesktop(env)
	case Other:
		steps = other(env)
	default:
		return "", fmt.Errorf("appsetup: no setup text for app %q (%s)", app, appList())
	}
	return Intro + " " + OwnComputer + "\n\n" + steps, nil
}

func appList() string {
	names := make([]string, len(Apps))
	for i, a := range Apps {
		names[i] = string(a)
	}
	return strings.Join(names, ", ")
}

func claudeCode(env string) string {
	mcpJSON := inFile("mcpServers", npxServer(ClaudeCode, env))
	return "1. Ask the person to allow this command, then run it:\n" +
		"`claude mcp add --scope user " + ServerName + " -- " + command(ClaudeCode, env) + "`\n" +
		"2. Claude Code starts new servers only when it starts, so the trading tools are not in this session. " + Restart +
		" Tell the person: 'Type /exit, then run `claude --continue` in this folder and say: " + SayAfter + ".'\n" +
		"3. " + FirstUse + "\n" +
		"If the command answers 'claude: command not found' (Claude Code inside an editor), add this to `.mcp.json` in the project folder instead (create it if missing; keep any servers already in it): `" + mcpJSON + "`. " +
		"Then the person reloads the editor window, allows the project's new server when Claude Code asks, and continues this conversation from its history.\n" +
		"npx comes with Node.js 20 or later. Without it, use uvx in the same place: `" + UVXEntry(ClaudeCode, env) + "`.\n" +
		Windows
}

func cursor(env string) string {
	file := inFile("mcpServers", npxServer(Cursor, env))
	return "1. Ask the person to add this to `~/.cursor/mcp.json` (create it if missing; keep any servers already in it): `" + file + "`.\n" +
		"2. Cursor loads a new server when it reloads its servers (Settings → MCP, or a restart of Cursor), not in a chat already open. " + Restart +
		" Then the person starts a new chat and says: " + SayAfter + ".\n" +
		"3. " + FirstUse + "\n" +
		"npx comes with Node.js 20 or later. Without it, use this entry instead: `" + UVXEntry(Cursor, env) + "`.\n" +
		Windows
}

func vscode(env string) string {
	s := npxServer(VSCode, env)
	s.Type = "stdio"
	file := inFile("servers", s)
	s.Name = ServerName
	add := compact(s)
	return "1. Ask the person to run this in a terminal: `code --add-mcp '" + add + "'`. Or to add this to `.vscode/mcp.json` in the project folder (create it if missing; keep any servers already in it): `" + file + "`.\n" +
		"2. VS Code starts the server with a later message, and may ask the person to trust it first; the message that added it cannot use it. " + Restart +
		" Then the person says: " + SayAfter + ".\n" +
		"3. " + FirstUse + "\n" +
		"npx comes with Node.js 20 or later. Without it, use uvx: `" + UVXEntry(VSCode, env) + "`.\n" +
		Windows
}

func claudeDesktop(env string) string {
	entry := `"` + ServerName + `": ` + Entry(ClaudeDesktop, env)
	return "If this conversation is in the Claude desktop app: it cannot add servers by itself. " +
		"1. Ask the person to open Settings → Developer → Edit Config, add `" + entry + "` under mcpServers, save, and quit Claude completely (not only the window), then reopen it. " + Restart +
		" Then the person starts a new conversation and says: " + SayAfter + ".\n" +
		"2. " + FirstUse + "\n" +
		"npx comes with Node.js 20 or later. Without it, use this entry instead: `\"" + ServerName + "\": " + UVXEntry(ClaudeDesktop, env) + "`.\n" +
		Windows + "\n" +
		"If this is Claude in a browser or on a phone: Mishmar can only read from here, because trading needs the signer on a computer the person owns. They can trade from Claude Code, Claude Desktop or Cursor on their computer by asking there."
}

func other(env string) string {
	return "1. Add a local (stdio) MCP server named " + ServerName + " that runs `" + command(Other, env) + "`, with <your-app> a short lowercase name for this app (letters, digits and dashes, like windsurf). As JSON: `" + Entry(Other, env) + "`.\n" +
		"2. " + Restart + " Restart the app if it does not load new servers by itself, then the person says: " + SayAfter + ".\n" +
		"3. " + FirstUse + "\n" +
		"npx comes with Node.js 20 or later. Without it, use uvx: `" + UVXEntry(Other, env) + "`.\n" +
		Windows
}

// AlreadyTrading is the text for an app that trades already: the computer's
// name as it gave it, when it signed in, and its code (the first two groups
// of its key's fingerprint).
func AlreadyTrading(machine string, signedIn time.Time, code string) string {
	return fmt.Sprintf("This app already trades through Mishmar from '%s' (signed in %s, code %s). Use the %s tools. Signing in on another computer replaces that one.",
		machine, signedIn.UTC().Format("2 Jan 2006"), code, ServerName)
}
