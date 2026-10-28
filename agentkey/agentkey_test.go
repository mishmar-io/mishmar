package agentkey

import (
	"bytes"
	"crypto/rand"
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
