package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmar-io/mishmar/kms"
	"github.com/mishmar-io/mishmar/kms/kmstest"
	"github.com/mishmar-io/mishmar/signerapi"
)

func clearMasterEnv(t *testing.T) {
	t.Helper()
	t.Setenv(MasterKeyEnv, "")
	t.Setenv(WrappedMasterKeyEnv, "")
	t.Setenv(kms.KeyEnv, "")
}

// Self-hosted stays as it was: the raw key from the environment.
func TestTheRawMasterKeyStillWorks(t *testing.T) {
	clearMasterEnv(t)
	ctx := context.Background()
	if _, err := MasterKeyFromEnv(ctx); err == nil || !strings.Contains(err.Error(), MasterKeyEnv) {
		t.Fatalf("nothing set: %v", err)
	}
	raw := bytes.Repeat([]byte{7}, 32)
	t.Setenv(MasterKeyEnv, hex.EncodeToString(raw))
	got, err := MasterKeyFromEnv(ctx)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("raw: %v", err)
	}
	t.Setenv(MasterKeyEnv, "abc")
	if _, err := MasterKeyFromEnv(ctx); err == nil {
		t.Fatal("a short key was accepted")
	}
}

// An existing raw key is wrapped and the shares sealed under it keep opening, through
// a key the service only ever gets from KMS. A new one can be made the same way.
func TestAWrappedMasterKeyOpensTheSameShares(t *testing.T) {
	clearMasterEnv(t)
	ctx := context.Background()
	f := kmstest.New(t)
	f.Use(t)
	raw := bytes.Repeat([]byte{7}, 32)
	path := filepath.Join(t.TempDir(), "signer-keys.json")
	backend, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(backend, raw)
	if err != nil {
		t.Fatal(err)
	}
	share := json.RawMessage(`{"share":"the engine's half"}`)
	if err := store.Put("vk_a", Entry{Scheme: signerapi.SchemeFrostEd25519, Share: share}); err != nil {
		t.Fatal(err)
	}

	t.Setenv(MasterKeyEnv, hex.EncodeToString(raw))
	t.Setenv(kms.KeyEnv, kmstest.Key)
	wrapped, err := WrapMasterKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wrapped, hex.EncodeToString(raw)) || bytes.Contains([]byte(wrapped), raw) {
		t.Fatal("the wrapped form holds the key")
	}

	// A raw key left next to the wrapped one is refused, not used.
	t.Setenv(WrappedMasterKeyEnv, wrapped)
	if _, err := MasterKeyFromEnv(ctx); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("both set: %v", err)
	}
	t.Setenv(MasterKeyEnv, "")
	master, err := MasterKeyFromEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backend, err = OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(backend, master)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := reopened.Get("vk_a"); !ok || !bytes.Equal(e.Share, share) {
		t.Fatalf("the share did not open: %v", ok)
	}

	// With no raw key to wrap, it makes a new one, different each time.
	t.Setenv(WrappedMasterKeyEnv, "")
	one, err := WrapMasterKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(WrappedMasterKeyEnv, one)
	first, err := MasterKeyFromEnv(ctx)
	if err != nil || len(first) != 32 {
		t.Fatalf("new key: %d %v", len(first), err)
	}
	t.Setenv(WrappedMasterKeyEnv, "")
	two, err := WrapMasterKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(WrappedMasterKeyEnv, two)
	second, err := MasterKeyFromEnv(ctx)
	if err != nil || bytes.Equal(first, second) {
		t.Fatalf("second new key: %v", err)
	}
}

// With a KMS key configured there is no fallback: down, refused, or wrongly set, the
// service does not start, and it never reads a raw key instead.
func TestAWrappedMasterKeyHasNoFallback(t *testing.T) {
	clearMasterEnv(t)
	ctx := context.Background()
	f := kmstest.New(t)
	f.Use(t)
	t.Setenv(kms.KeyEnv, kmstest.Key)
	wrapped, err := WrapMasterKey(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(WrappedMasterKeyEnv, wrapped)
	f.SetDown(true)
	if _, err := MasterKeyFromEnv(ctx); err == nil || !strings.Contains(err.Error(), "does not start") {
		t.Fatalf("KMS down: %v", err)
	}
	f.SetDown(false)
	f.RefuseDecrypt(true)
	if _, err := MasterKeyFromEnv(ctx); err == nil || !strings.Contains(err.Error(), "does not start") {
		t.Fatalf("permission gone: %v", err)
	}
	f.RefuseDecrypt(false)
	if _, err := MasterKeyFromEnv(ctx); err != nil {
		t.Fatalf("back: %v", err)
	}

	// A ciphertext from another KMS, or nonsense.
	kmstest.New(t).Use(t)
	if _, err := MasterKeyFromEnv(ctx); err == nil || !strings.Contains(err.Error(), "does not start") {
		t.Fatalf("another KMS: %v", err)
	}
	f.Use(t)
	t.Setenv(WrappedMasterKeyEnv, "not base64!")
	if _, err := MasterKeyFromEnv(ctx); err == nil {
		t.Fatal("nonsense was accepted")
	}

	// Half configured, either way round.
	t.Setenv(WrappedMasterKeyEnv, wrapped)
	t.Setenv(kms.KeyEnv, "")
	if _, err := MasterKeyFromEnv(ctx); err == nil || !strings.Contains(err.Error(), kms.KeyEnv) {
		t.Fatalf("no KMS key: %v", err)
	}
	t.Setenv(WrappedMasterKeyEnv, "")
	t.Setenv(kms.KeyEnv, kmstest.Key)
	t.Setenv(MasterKeyEnv, hex.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if _, err := MasterKeyFromEnv(ctx); err == nil || !strings.Contains(err.Error(), WrappedMasterKeyEnv) {
		t.Fatalf("a raw key with a KMS key configured: %v", err)
	}
}
