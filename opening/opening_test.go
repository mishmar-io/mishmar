package opening

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

const specPath = "../spec/opening.json"

// The old opening, which must be gone from every copy.
const oldOpening = "is a risk management layer"

// The MCP registry refuses a longer description.
const registryMax = 100

// opening reads spec/opening.json: exactly the fields first and more, neither empty.
func opening(t *testing.T) (first, more string) {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("spec/opening.json does not read: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("spec/opening.json is not a JSON object: %v", err)
	}
	var names []string
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "first,more" {
		t.Fatalf("spec/opening.json has the fields %v; it must have first and more and nothing else", names)
	}
	if err := json.Unmarshal(fields["first"], &first); err != nil {
		t.Fatalf("spec/opening.json: first is not a string: %v", err)
	}
	if err := json.Unmarshal(fields["more"], &more); err != nil {
		t.Fatalf("spec/opening.json: more is not a string: %v", err)
	}
	if strings.TrimSpace(first) == "" || strings.TrimSpace(more) == "" {
		t.Fatalf("spec/opening.json: first and more must both be written (first %q, more %q)", first, more)
	}
	return first, more
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile("../" + path)
	if err != nil {
		t.Fatalf("%s does not read: %v", path, err)
	}
	return string(b)
}

// collapse turns every run of whitespace into one space, so a wrapped
// paragraph reads as one line.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// jsonDescription is the description field of a JSON file.
func jsonDescription(t *testing.T, path string) string {
	t.Helper()
	var v struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &v); err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	return v.Description
}

func TestReadmes(t *testing.T) {
	first, more := opening(t)
	want := first + " " + more
	for _, path := range []string{"README.md", "python/README.md", "typescript/README.md"} {
		if !strings.Contains(collapse(read(t, path)), want) {
			t.Errorf("%s does not hold the opening of spec/opening.json (first, a space, more): %q", path, want)
		}
	}
}

func TestPyproject(t *testing.T) {
	first, _ := opening(t)
	const path = "python/pyproject.toml"
	var value string
	found := false
	for _, line := range strings.Split(read(t, path), "\n") {
		if rest, ok := strings.CutPrefix(line, "description = "); ok {
			v, err := strconv.Unquote(strings.TrimSpace(rest))
			if err != nil {
				t.Fatalf("%s: the description line is not one quoted string: %s", path, line)
			}
			value, found = v, true
			break
		}
	}
	if !found {
		t.Fatalf("%s has no description = \"…\" line", path)
	}
	if !strings.HasPrefix(value, first+" ") {
		t.Errorf("%s: the description does not start with the first line of spec/opening.json and a space: %q", path, value)
	}
}

func TestPackageJSON(t *testing.T) {
	first, _ := opening(t)
	const path = "typescript/package.json"
	if d := jsonDescription(t, path); !strings.HasPrefix(d, first+" ") {
		t.Errorf("%s: the description does not start with the first line of spec/opening.json and a space: %q", path, d)
	}
}

func TestServerJSON(t *testing.T) {
	first, _ := opening(t)
	const path = "server.json"
	d := jsonDescription(t, path)
	if d != first {
		t.Errorf("%s: the description must be exactly the first line of spec/opening.json, %q; it is %q", path, first, d)
	}
	if n := utf8.RuneCountInString(d); n > registryMax {
		t.Errorf("%s: the description is %d characters; the MCP registry takes at most %d", path, n, registryMax)
	}
}

func TestDocsHome(t *testing.T) {
	first, more := opening(t)
	const path = "docs/src/content/docs/index.md"
	text := read(t, path)
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		t.Fatalf("%s does not start with front matter", path)
	}
	_, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		t.Fatalf("%s: the front matter does not end", path)
	}
	if !strings.HasPrefix(strings.TrimLeft(body, " \t\r\n"), first+" "+more) {
		t.Errorf("%s: the page does not start with the opening of spec/opening.json (first, a space, more)", path)
	}
}

func TestLlmsTxt(t *testing.T) {
	first, more := opening(t)
	const path = "docs/src/pages/llms.txt.ts"
	want := "'> " + first + " " + more + "'"
	if !strings.Contains(read(t, path), want) {
		t.Errorf("%s does not hold the opening of spec/opening.json as its quote: %s", path, want)
	}
}

func TestOldOpeningGone(t *testing.T) {
	for _, path := range []string{
		"README.md", "python/README.md", "typescript/README.md",
		"python/pyproject.toml", "typescript/package.json", "server.json",
		"docs/src/content/docs/index.md", "docs/src/pages/llms.txt.ts",
	} {
		if strings.Contains(collapse(read(t, path)), oldOpening) {
			t.Errorf("%s still says Mishmar %q: the old opening must be gone from it", path, oldOpening)
		}
	}
}
