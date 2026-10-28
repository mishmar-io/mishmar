package agentkey

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite spec/vectors/agent-request-signing.json from this test's cases")

const requestVectorPath = "../spec/vectors/agent-request-signing.json"

func vectorSeed() []byte {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	return seed
}

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// escapedAmp is "&" as the canonical JSON writes it, built from pieces so no
// tool rewrites the escape in this source file.
var escapedAmp = "\\" + "u0026"

type requestCase struct {
	Name         string `json:"name"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Query        string `json:"query,omitempty"`
	Body         string `json:"body,omitempty"`
	KeyID        string `json:"keyId"`
	Nonce        string `json:"nonce"`
	SignedAt     string `json:"signedAt"`
	SigningBytes string `json:"signingBytes"`
	Signature    string `json:"signature"`
}

type requestVectorFile struct {
	Description string        `json:"description"`
	Secret      string        `json:"secret"`
	PublicKey   string        `json:"publicKey"`
	Cases       []requestCase `json:"cases"`
}

func requestCases() []Request {
	return []Request{
		{Method: "GET", Path: "/agent/decisions", KeyID: "key_a", Nonce: "n1", SignedAt: SignedAtString(t0)},
		{Method: "get", Path: "/agent/decisions", Query: "verdict=require_approval&limit=20", KeyID: "key_a", Nonce: "n2",
			SignedAt: SignedAtString(t0.Add(250 * time.Millisecond).In(time.FixedZone("X", 7200)))},
		{Method: "POST", Path: "/agent/signer/poll", Body: []byte(`{"waitSeconds":25}`), KeyID: "key_a", Nonce: "n3", SignedAt: SignedAtString(t0)},
		{Method: "POST", Path: "/agent/signer/requests/sr_1/answer", Body: []byte(`{"kind":"refusal","refusal":{"reason":"not my order"}}`),
			KeyID: "key_a", Nonce: "n4", SignedAt: SignedAtString(t0.Add(time.Second))},
	}
}

var caseNames = []string{
	"GET without a query",
	"GET with a query, lower-case method, non-UTC time",
	"POST with a body",
	"POST with a body, a second later",
}

func TestRequestSigningBytes(t *testing.T) {
	cases := requestCases()
	got, err := RequestSigningBytes(cases[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"keyId":"key_a","method":"GET","nonce":"n1","path":"/agent/decisions","signedAt":"2026-10-26T12:00:00Z"}`
	if string(got) != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	// A lower-case method is signed upper case; a non-UTC time was rendered as
	// UTC with the fraction trimmed; the query is present, its & escaped.
	got, _ = RequestSigningBytes(cases[1])
	want = `{"keyId":"key_a","method":"GET","nonce":"n2","path":"/agent/decisions","query":"verdict=require_approval` + escapedAmp + `limit=20","signedAt":"2026-10-26T12:00:00.25Z"}`
	if string(got) != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	// A body is signed by its hash, not its bytes.
	got, _ = RequestSigningBytes(cases[2])
	if !strings.Contains(string(got), `"bodySha256":"`) || strings.Contains(string(got), "waitSeconds") {
		t.Fatalf("body not hashed: %s", got)
	}
}

func TestSignAndVerifyRequest(t *testing.T) {
	key := FromSeed(vectorSeed())
	r := requestCases()[2]
	h, err := key.SignRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if h[HeaderKeyID] != "key_a" || h[HeaderNonce] != "n3" || h[HeaderSignedAt] != "2026-10-26T12:00:00Z" {
		t.Fatalf("headers %v", h)
	}
	if !VerifyRequest(key.PublicKeyB64(), r, h[HeaderSignature]) {
		t.Fatal("does not verify")
	}
	tampered := r
	tampered.Body = []byte(`{"waitSeconds":30}`)
	if VerifyRequest(key.PublicKeyB64(), tampered, h[HeaderSignature]) {
		t.Fatal("verifies with another body")
	}
	tampered = r
	tampered.Path = "/agent/approvals"
	if VerifyRequest(key.PublicKeyB64(), tampered, h[HeaderSignature]) {
		t.Fatal("verifies for another path")
	}
	// Left empty, the nonce and the time are filled in.
	h, err = key.SignRequest(Request{Method: "GET", Path: "/agent/positions", KeyID: "key_a"})
	if err != nil || len(h[HeaderNonce]) != 32 || !strings.HasSuffix(h[HeaderSignedAt], "Z") {
		t.Fatalf("defaults not filled: %v %v", h, err)
	}
}

// TestRequestVectors keeps spec/vectors/agent-request-signing.json equal to
// the cases above. `go test ./agentkey -update` rewrites it.
func TestRequestVectors(t *testing.T) {
	key := FromSeed(vectorSeed())
	file := requestVectorFile{
		Description: "The agentAuth scheme: Ed25519 over the canonical JSON of {bodySha256, keyId, method, nonce, path, query, signedAt}. method upper case; path without the server prefix; query the raw query string (absent when empty); bodySha256 the lower-case hex SHA-256 of the body (absent when none); signedAt as sent in X-Mishmar-Signed-At, RFC 3339 UTC with the fraction trimmed. For each case the bytes must equal signingBytes and the signature with secret must equal signature.",
		Secret:      Secret("msk_vector", vectorSeed()),
		PublicKey:   key.PublicKeyB64(),
	}
	for i, r := range requestCases() {
		msg, err := RequestSigningBytes(r)
		if err != nil {
			t.Fatal(err)
		}
		file.Cases = append(file.Cases, requestCase{
			Name: caseNames[i], Method: r.Method, Path: r.Path, Query: r.Query, Body: string(r.Body), KeyID: r.KeyID, Nonce: r.Nonce,
			SignedAt: r.SignedAt, SigningBytes: string(msg), Signature: key.Sign(msg),
		})
	}
	want, _ := json.MarshalIndent(file, "", "  ")
	want = append(want, '\n')
	if *update {
		if err := os.MkdirAll(filepath.Dir(requestVectorPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(requestVectorPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(requestVectorPath)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("spec/vectors/agent-request-signing.json is out of date: run `go test ./agentkey -update`")
	}
}
