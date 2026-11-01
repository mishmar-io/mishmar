package kms_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mishmar-io/mishmar/kms"
	"github.com/mishmar-io/mishmar/kms/kmstest"
)

func TestWrapAndUnwrap(t *testing.T) {
	f := kmstest.New(t)
	c := f.Client(t)
	ctx := context.Background()
	secret := []byte("thirty-two bytes of a data key..")
	wrapped, err := c.Encrypt(ctx, secret, []byte("account a"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wrapped, secret) {
		t.Fatal("the wrapped form holds the secret")
	}
	got, err := c.Decrypt(ctx, wrapped, []byte("account a"))
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("unwrapped %v %v", len(got), err)
	}
	// The associated data is part of it: another account's cannot open it.
	_, err = c.Decrypt(ctx, wrapped, []byte("account b"))
	var ke *kms.Error
	if !errors.As(err, &ke) || ke.Status != http.StatusBadRequest {
		t.Fatalf("another account's: %v", err)
	}
	// Nor can another key.
	other := kmstest.New(t).Client(t)
	if _, err := other.Decrypt(ctx, wrapped, []byte("account a")); !errors.As(err, &ke) {
		t.Fatalf("another key: %v", err)
	}
}

func TestOnlyAWholeKeyNameIsAccepted(t *testing.T) {
	for _, bad := range []string{"", "my-key", "projects/p/locations/l/keyRings/r", kmstest.Key + "/cryptoKeyVersions/1"} {
		if _, err := kms.New(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A new pod's metadata server takes a few seconds to answer, so the token is tried
// again. An API that is down is an error at once.
func TestTheMetadataServerGetsAFewTries(t *testing.T) {
	f := kmstest.New(t)
	c := f.Client(t)
	ctx := context.Background()
	f.FailTokens(2)
	if _, err := c.Encrypt(ctx, []byte("x"), []byte("y")); err != nil {
		t.Fatalf("after a slow start: %v", err)
	}
	f.SetDown(true)
	if _, err := c.Encrypt(ctx, []byte("x"), []byte("y")); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("down: %v", err)
	}
}

// An answer that fails its checksums, or names another key, is not used.
func TestAnAnswerThatFailsItsChecksIsNotUsed(t *testing.T) {
	answer := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "t"})
			return
		}
		json.NewEncoder(w).Encode(answer)
	}))
	defer srv.Close()
	t.Setenv(kms.EndpointEnv, srv.URL)
	t.Setenv(kms.MetadataHostEnv, strings.TrimPrefix(srv.URL, "http://"))
	c, err := kms.New(kmstest.Key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, a := range map[string]map[string]any{
		"plaintext not verified": {"name": kmstest.Key + "/cryptoKeyVersions/1", "ciphertext": []byte{}, "ciphertextCrc32c": "0", "verifiedAdditionalAuthenticatedDataCrc32c": true},
		"aad not verified":       {"name": kmstest.Key + "/cryptoKeyVersions/1", "ciphertext": []byte{}, "ciphertextCrc32c": "0", "verifiedPlaintextCrc32c": true},
		"damaged ciphertext":     {"name": kmstest.Key + "/cryptoKeyVersions/1", "ciphertext": []byte("x"), "ciphertextCrc32c": "1", "verifiedPlaintextCrc32c": true, "verifiedAdditionalAuthenticatedDataCrc32c": true},
		"another key":            {"name": "projects/x/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1", "ciphertext": []byte{}, "ciphertextCrc32c": "0", "verifiedPlaintextCrc32c": true, "verifiedAdditionalAuthenticatedDataCrc32c": true},
	} {
		answer = a
		if _, err := c.Encrypt(ctx, []byte("x"), []byte("y")); err == nil {
			t.Errorf("encrypt, %s: accepted", name)
		}
	}
	answer = map[string]any{"plaintext": []byte("x"), "plaintextCrc32c": "1"}
	if _, err := c.Decrypt(ctx, []byte("x"), []byte("y")); err == nil {
		t.Error("decrypt, damaged plaintext: accepted")
	}
}

// The client ignores HTTPS_PROXY: in a cell that is the way out to the venues.
func TestTheProxyIsNotUsed(t *testing.T) {
	f := kmstest.New(t)
	proxied := false
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = true
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	c := f.Client(t)
	if _, err := c.Encrypt(context.Background(), []byte("x"), []byte("y")); err != nil {
		t.Fatal(err)
	}
	if proxied {
		t.Fatal("the call went through the proxy")
	}
}
