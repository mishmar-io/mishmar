package agentkey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

const (
	fingerprintVectorPath  = "../spec/vectors/agent-key-fingerprint.json"
	machineProofVectorPath = "../spec/vectors/machine-proof.json"
)

// contractPattern is AgentKey.fingerprint's pattern in spec/openapi.yaml.
var contractPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){3}$`)

type fingerprintCase struct {
	Name        string `json:"name"`
	Seed        string `json:"seed,omitempty"`
	PublicKey   string `json:"publicKey"`
	Sha256      string `json:"sha256"`
	Fingerprint string `json:"fingerprint"`
	Code        string `json:"code"`
}

type fingerprintFile struct {
	Description string            `json:"description"`
	Cases       []fingerprintCase `json:"cases"`
}

func seedOf(b byte, step byte) []byte {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = b + byte(i)*step
	}
	return seed
}

func fingerprintCases() []fingerprintCase {
	type src struct {
		name string
		seed []byte
		pub  ed25519.PublicKey
	}
	srcs := []src{
		{name: "the vectors' key (seed 00 01 … 1f)", seed: vectorSeed()},
		{name: "seed all zero", seed: make([]byte, 32)},
		{name: "seed all 0xff", seed: seedOf(0xff, 0)},
		{name: "seed 0x42 rising by 7", seed: seedOf(0x42, 7)},
		{name: "raw public key 00 01 … 1f (any 32 bytes have a fingerprint)", pub: ed25519.PublicKey(vectorSeed())},
	}
	var out []fingerprintCase
	for _, s := range srcs {
		c := fingerprintCase{Name: s.name}
		pub := s.pub
		if s.seed != nil {
			pub = FromSeed(s.seed).PublicKey()
			c.Seed = base64.RawURLEncoding.EncodeToString(s.seed)
		}
		sum := sha256.Sum256(pub)
		c.PublicKey = base64.StdEncoding.EncodeToString(pub)
		c.Sha256 = hex.EncodeToString(sum[:])
		c.Fingerprint = Fingerprint(pub)
		c.Code = FingerprintCode(c.Fingerprint)
		out = append(out, c)
	}
	return out
}

func TestFingerprint(t *testing.T) {
	for _, c := range fingerprintCases() {
		if !contractPattern.MatchString(c.Fingerprint) || !ValidFingerprint(c.Fingerprint) {
			t.Fatalf("%s: %q does not match the contract's pattern", c.Name, c.Fingerprint)
		}
		if c.Code != c.Fingerprint[:9] || len(c.Code) != 9 {
			t.Fatalf("%s: code %q", c.Name, c.Code)
		}
		if got, err := FingerprintOf(c.PublicKey); err != nil || got != c.Fingerprint {
			t.Fatalf("%s: FingerprintOf %q %v", c.Name, got, err)
		}
	}
	// The first 80 bits, five at a time, most significant first: the vectors'
	// key worked by hand from its hash.
	k := FromSeed(vectorSeed())
	sum := sha256.Sum256(k.PublicKey())
	var bits uint64
	var n uint
	var want []byte
	for _, b := range sum[:10] {
		bits = bits<<8 | uint64(b)
		n += 8
		for n >= 5 {
			want = append(want, crockford[(bits>>(n-5))&31])
			n -= 5
		}
	}
	w := string(want)
	if got := k.Fingerprint(); got != w[0:4]+"-"+w[4:8]+"-"+w[8:12]+"-"+w[12:16] {
		t.Fatalf("fingerprint %s, by hand %s", got, w)
	}
	// What is not a 32-byte key has none, and a malformed one is refused.
	if Fingerprint(ed25519.PublicKey(make([]byte, 31))) != "" || FingerprintCode("ABCD-EFGH") != "" {
		t.Fatal("a short key or a short string has a fingerprint")
	}
	if _, err := FingerprintOf("not base64"); err == nil {
		t.Fatal("a malformed key has a fingerprint")
	}
	for _, bad := range []string{"ABCD-EFGH-JKMN-PQRI", "ABCD-EFGH-JKMN-PQRL", "abcd-efgh-jkmn-pqrs", "ABCDEFGH-JKMN-PQRST", "ABCD-EFGH-JKMN-PQRSU"} {
		if ValidFingerprint(bad) || contractPattern.MatchString(bad) != ValidFingerprint(bad) {
			t.Fatalf("%q taken as a fingerprint", bad)
		}
	}
}

// TestFingerprintVectors keeps spec/vectors/agent-key-fingerprint.json equal
// to the cases above. `go test ./agentkey -update` rewrites it.
func TestFingerprintVectors(t *testing.T) {
	file := fingerprintFile{
		Description: "AgentKey.fingerprint: the first 80 bits (10 bytes) of SHA-256 of the raw 32-byte Ed25519 public key, in Crockford base32 (alphabet 0123456789ABCDEFGHJKMNPQRSTVWXYZ, five bits a character, most significant bit first, no padding: 16 characters), in four groups of four joined by '-'. code is the first two groups, what a trade sign-in shows on the machine and on the approval page. seed is base64url without padding (absent when the case starts from a raw public key); publicKey is base64 standard padded, as the API carries it; sha256 is the hash of the raw key, lower-case hex.",
		Cases:       fingerprintCases(),
	}
	checkVectorFile(t, fingerprintVectorPath, file)
}

type machineProofCase struct {
	Name         string `json:"name"`
	Code         string `json:"code"`
	CodeSha256   string `json:"codeSha256"`
	Issuer       string `json:"issuer"`
	SignedAt     string `json:"signedAt"`
	SigningBytes string `json:"signingBytes"`
	Signature    string `json:"signature"`
}

type machineProofRefusal struct {
	Name      string `json:"name"`
	Code      string `json:"code"`
	Issuer    string `json:"issuer"`
	PublicKey string `json:"publicKey"`
	SignedAt  string `json:"signedAt"`
	Signature string `json:"signature"`
	Now       string `json:"now"`
	Why       string `json:"why"`
}

type machineProofFile struct {
	Description string                `json:"description"`
	Secret      string                `json:"secret"`
	Seed        string                `json:"seed"`
	PublicKey   string                `json:"publicKey"`
	Fingerprint string                `json:"fingerprint"`
	Cases       []machineProofCase    `json:"cases"`
	Refused     []machineProofRefusal `json:"refused"`
}

var proofTime = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func machineProofCases(t *testing.T, k *Key) []machineProofCase {
	in := []struct {
		name, code, issuer string
		at                 time.Time
	}{
		{"prod", "mac_Q2xhdWRlIENvZGUgb24gYSBNYWNCb29r", "https://api.mishmar.io", proofTime},
		{"testnet, a fraction of a second, a non-UTC clock", "mac_dGVzdG5ldCBjb2Rl", "https://api.testnet.mishmar.io", proofTime.Add(250 * time.Millisecond).In(time.FixedZone("X", 7200))},
		{"a local control plane on a port", "mac_bG9jYWw", "http://localhost:8090", proofTime.Add(time.Second)},
	}
	var out []machineProofCase
	for _, c := range in {
		sig, signedAt, err := k.SignMachineProof(c.code, c.issuer, c.at)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := MachineProofBytes(MachineProof{Code: c.code, Issuer: c.issuer, PublicKey: k.PublicKeyB64(), SignedAt: signedAt})
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(c.code))
		out = append(out, machineProofCase{Name: c.name, Code: c.code, CodeSha256: hex.EncodeToString(sum[:]), Issuer: c.issuer, SignedAt: signedAt, SigningBytes: string(msg), Signature: sig})
	}
	return out
}

func TestMachineProof(t *testing.T) {
	k := FromSeed(vectorSeed())
	cases := machineProofCases(t, k)
	want := `{"code":"` + cases[0].CodeSha256 + `","issuer":"https://api.mishmar.io","kind":"mishmar-machine-proof","publicKey":"` + k.PublicKeyB64() + `","signedAt":"2026-11-13T12:00:00Z"}`
	if cases[0].SigningBytes != want {
		t.Fatalf("\n got %s\nwant %s", cases[0].SigningBytes, want)
	}
	if cases[1].SignedAt != "2026-11-13T12:00:00.25Z" {
		t.Fatalf("signedAt %q", cases[1].SignedAt)
	}
	for _, c := range cases {
		p := MachineProof{Code: c.Code, Issuer: c.Issuer, PublicKey: k.PublicKeyB64(), SignedAt: c.SignedAt}
		if why := VerifyMachineProof(p, c.Signature, proofTime); why != "" {
			t.Fatalf("%s: refused: %s", c.Name, why)
		}
	}
	for _, r := range machineProofRefusals(t, k) {
		now, _ := time.Parse(time.RFC3339Nano, r.Now)
		if why := VerifyMachineProof(MachineProof{Code: r.Code, Issuer: r.Issuer, PublicKey: r.PublicKey, SignedAt: r.SignedAt}, r.Signature, now); why != r.Why {
			t.Fatalf("%s: %q, want %q", r.Name, why, r.Why)
		}
	}
	if _, err := MachineProofBytes(MachineProof{Code: "mac_x", Issuer: "https://api.mishmar.io", PublicKey: k.PublicKeyB64()}); err == nil {
		t.Fatal("a proof without a time")
	}
}

// machineProofRefusals are proofs the control plane must refuse, each with
// the reason VerifyMachineProof gives.
func machineProofRefusals(t *testing.T, k *Key) []machineProofRefusal {
	other := FromSeed(seedOf(0x42, 7))
	good := machineProofCases(t, k)[0]
	sigOther, _, err := other.SignMachineProof(good.Code, good.Issuer, proofTime)
	if err != nil {
		t.Fatal(err)
	}
	now := SignedAtString(proofTime)
	bad := "key_proof is not the key's signature over this code"
	return []machineProofRefusal{
		{"another code", "mac_another", good.Issuer, k.PublicKeyB64(), good.SignedAt, good.Signature, now, bad},
		{"another issuer", good.Code, "https://api.testnet.mishmar.io", k.PublicKeyB64(), good.SignedAt, good.Signature, now, bad},
		{"signed by another key than the one on the authorize request", good.Code, good.Issuer, k.PublicKeyB64(), good.SignedAt, sigOther, now, bad},
		{"another time than the one signed", good.Code, good.Issuer, k.PublicKeyB64(), "2026-11-13T12:00:01Z", good.Signature, now, bad},
		{"signed 61 seconds before now", good.Code, good.Issuer, k.PublicKeyB64(), good.SignedAt, good.Signature, SignedAtString(proofTime.Add(61 * time.Second)), "key_signed_at is more than 60 seconds from now"},
		{"signed 61 seconds after now", good.Code, good.Issuer, k.PublicKeyB64(), good.SignedAt, good.Signature, SignedAtString(proofTime.Add(-61 * time.Second)), "key_signed_at is more than 60 seconds from now"},
	}
}

// TestMachineProofVectors keeps spec/vectors/machine-proof.json equal to the
// cases above. `go test ./agentkey -update` rewrites it.
func TestMachineProofVectors(t *testing.T) {
	k := FromSeed(vectorSeed())
	file := machineProofFile{
		Description: "The trade sign-in's proof of possession (key_proof at POST /oauth/token): Ed25519 with the machine's key over the canonical JSON of {code, issuer, kind, publicKey, signedAt}: code the lower-case hex SHA-256 of the authorization code's bytes (never the code itself), issuer the hosting API's base URL exactly as the callback's iss carried it, kind \"mishmar-machine-proof\", publicKey the key base64 standard padded (the key the authorize request named, as AgentKey.publicKey writes it), signedAt RFC 3339 UTC with the fraction trimmed, sent as key_signed_at. The signature is base64 standard padded. For each case the bytes must equal signingBytes and the signature with secret must equal signature. The control plane takes a proof within 60 seconds of signedAt either way, and refuses each case under refused with the reason given (its own words may differ; the refusal may not).",
		Secret:      Secret("msk_vector", vectorSeed()),
		Seed:        base64.RawURLEncoding.EncodeToString(vectorSeed()),
		PublicKey:   k.PublicKeyB64(),
		Fingerprint: k.Fingerprint(),
		Cases:       machineProofCases(t, k),
		Refused:     machineProofRefusals(t, k),
	}
	checkVectorFile(t, machineProofVectorPath, file)
}

// checkVectorFile writes v to path under -update and fails when the file is
// not v.
func checkVectorFile(t *testing.T, path string, v any) {
	t.Helper()
	want, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if *update {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./agentkey -update` to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date: run `go test ./agentkey -update`", path)
	}
}
