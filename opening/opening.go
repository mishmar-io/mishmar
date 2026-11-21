// Package opening holds no code: its test keeps Mishmar's opening the same
// everywhere it is written. spec/opening.json is the one place the opening
// lives, in two parts: first, the line that says what Mishmar is, and more,
// the two sentences that follow it. The copies, which the test compares with
// it, are README.md, python/README.md and typescript/README.md (first and
// more), the descriptions in python/pyproject.toml and typescript/package.json
// (first, then the SDK's own sentence) and in server.json (first alone, at
// most 100 characters for the MCP registry), the docs home page
// (docs/src/content/docs/index.md, which starts with first and more) and
// docs/src/pages/llms.txt.ts (the quote under llms.txt's title). The docs
// hold their copies as plain text, so their build reads nothing outside
// docs/. To change the opening, change spec/opening.json and every copy.
package opening
