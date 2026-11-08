package agentkey

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func TestSecretRoundTrip(t *testing.T) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	secret := Secret("msk_ab12cd", seed)
	k, err := Parse(secret)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k.PublicKey(), FromSeed(seed).PublicKey()) {
		t.Fatal("parsed key differs from the seed's")
	}
	msg := []byte("hello")
	sig := k.Sign(msg)
	if !Verify(k.PublicKeyB64(), msg, sig) {
		t.Fatal("signature does not verify")
	}
	if Verify(k.PublicKeyB64(), []byte("other"), sig) {
		t.Fatal("signature verifies another message")
	}
	if Verify("not base64!", msg, sig) || Verify(k.PublicKeyB64(), msg, "not base64!") {
		t.Fatal("malformed inputs verified")
	}
}

func TestNewSecretNamesTheEngineAndTheKey(t *testing.T) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	secret, err := NewSecret("testnet", "key_k7f2q9x1m3n4_abcdefgh2345", seed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "msk_testnet_k7f2q9x1m3n4_abcdefgh2345_") {
		t.Fatalf("secret %q", secret)
	}
	p, err := ParseSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	if p.Environment != "testnet" || p.AccountID != "acc_k7f2q9x1m3n4" || p.KeyID != "key_k7f2q9x1m3n4_abcdefgh2345" {
		t.Fatalf("parsed %+v", p)
	}
	if !bytes.Equal(p.Key.PublicKey(), FromSeed(seed).PublicKey()) {
		t.Fatal("parsed key differs from the seed's")
	}
	// Parse reads the same key, and a trailing newline (a pasted secret) is fine.
	if k, err := Parse(secret + "\n"); err != nil || !bytes.Equal(k.PublicKey(), p.Key.PublicKey()) {
		t.Fatalf("Parse: %v", err)
	}
	// What NewSecret refuses.
	for _, c := range []struct{ env, id string }{
		{"Testnet", "key_main_abcdefgh2345"},
		{"", "key_main_abcdefgh2345"},
		{"testnet", "key_main_short"},
		{"testnet", "agt_main_abcdefgh2345"},
		{"testnet", "key_main_abcdefgh2345_extra"},
	} {
		if _, err := NewSecret(c.env, c.id, seed); err == nil {
			t.Errorf("NewSecret(%q, %q) was accepted", c.env, c.id)
		}
	}
	if _, err := NewSecret("testnet", "key_main_abcdefgh2345", seed[:31]); err == nil {
		t.Error("a 31-byte seed was accepted")
	}
}

func TestOlderSecretsStillParse(t *testing.T) {
	// A seed whose base64url is all underscores: the worst case for telling
	// the two formats apart.
	seed := bytes.Repeat([]byte{0xff}, 32)
	secret := Secret("msk_ab12cd", seed)
	if !strings.Contains(secret, "___") {
		t.Fatalf("test seed %q holds no underscores", secret)
	}
	p, err := ParseSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	if p.Environment != "" || p.AccountID != "" || p.KeyID != "" {
		t.Fatalf("an older secret named %+v", p)
	}
	if !bytes.Equal(p.Key.PublicKey(), FromSeed(seed).PublicKey()) {
		t.Fatal("parsed key differs from the seed's")
	}
	// And a new-format secret whose seed is all underscores still parses as new.
	newSecret, err := NewSecret("prod", "key_main_abcdefgh2345", seed)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ParseSecret(newSecret); err != nil || p.KeyID != "key_main_abcdefgh2345" {
		t.Fatalf("new secret with an underscored seed: %+v %v", p, err)
	}
}

func TestParseRefusesMalformedSecrets(t *testing.T) {
	for _, s := range []string{"", "msk_", "msk_a", "msk_a_b", "key_a_" + Secret("", make([]byte, 32))[1:], "msk_a_" + "AAAA", "abc_def_" + Secret("", make([]byte, 32))[1:]} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%q was accepted", s)
		}
	}
	// A secret from a 33-byte seed is refused as well.
	if _, err := Parse(Secret("msk_x", make([]byte, 33))); err == nil {
		t.Error("a 33-byte seed was accepted")
	}
}
