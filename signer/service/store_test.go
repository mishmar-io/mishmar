package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/signerapi"
)

var testMasterKey = bytes.Repeat([]byte{7}, 32)

// A share as the frost package would marshal it; the base64 is what must
// never appear on disk.
var testShare = json.RawMessage(`{"scheme":"frost-ed25519","share":"c2VjcmV0LXNoYXJlLWJ5dGVz"}`)

func TestStoreSealsAtRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	backend, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(backend, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put("vk_1", Entry{Scheme: signerapi.SchemeFrostEd25519, Share: testShare}); err != nil {
		t.Fatal(err)
	}
	if err := st.Put("vk_1", Entry{Scheme: signerapi.SchemeFrostEd25519, Share: testShare}); !errors.Is(err, ErrHeld) {
		t.Fatalf("a second Put of the same id: %v, want ErrHeld", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("c2VjcmV0")) || bytes.Contains(raw, []byte(`"share"`)) {
		t.Fatal("the share is in the clear on disk")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", info.Mode(), err)
	}

	reopen := func(key []byte) (*Store, error) {
		b, err := OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return OpenStore(b, key)
	}
	st2, err := reopen(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := st2.Get("vk_1")
	if !ok || e.Scheme != signerapi.SchemeFrostEd25519 || !bytes.Equal(e.Share, testShare) {
		t.Fatalf("reopened entry: %v %v", ok, e)
	}
	if ids := st2.IDs(); len(ids) != 1 || ids[0] != "vk_1" {
		t.Fatalf("ids: %v", ids)
	}
	if _, err := reopen(bytes.Repeat([]byte{8}, 32)); err == nil {
		t.Fatal("a wrong master key opened the store")
	}

	// An entry moved to another id is refused: the id is associated data.
	var f storeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	f.Keys["vk_2"] = f.Keys["vk_1"]
	delete(f.Keys, "vk_1")
	moved, _ := json.Marshal(f)
	if err := os.WriteFile(path, moved, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopen(testMasterKey); err == nil {
		t.Fatal("an entry moved to another id was opened")
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	st3, err := reopen(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := st3.Delete("vk_1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st3.Get("vk_1"); ok {
		t.Fatal("deleted entry still held")
	}
	st4, err := reopen(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st4.Get("vk_1"); ok {
		t.Fatal("deleted entry came back from disk")
	}
}

func TestOpenFileRefusesUnsealedFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte(`{"keys":{"vk_1":{"scheme":"ecdsa-2p","share":{}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFile(path); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("an unsealed file opened: %v", err)
	}
}

// memBackend is a backend two stores share, as object storage would be.
type memBackend struct {
	mu   sync.Mutex
	keys map[string]Sealed
	gets int
}

func (b *memBackend) List() ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.keys))
	for id := range b.keys {
		out = append(out, id)
	}
	return out, nil
}

func (b *memBackend) Get(id string) (Sealed, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gets++
	e, ok := b.keys[id]
	return e, ok, nil
}

func (b *memBackend) Put(id string, e Sealed) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, dup := b.keys[id]; dup {
		return ErrHeld
	}
	b.keys[id] = e
	return nil
}

func (b *memBackend) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.keys, id)
	return nil
}

func TestStoreSyncFollowsTheBackend(t *testing.T) {
	backend := &memBackend{keys: map[string]Sealed{}}
	a, err := OpenStore(backend, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := OpenStore(backend, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c.now = func() time.Time { return now }
	// An id the backend lacks is asked once, then remembered as missing.
	for range 3 {
		if _, ok := c.Get("vk_none"); ok {
			t.Fatal("an unknown id was found")
		}
	}
	if backend.gets != 1 {
		t.Fatalf("backend asked %d times for a missing id, want 1", backend.gets)
	}
	now = now.Add(MissingTTL + time.Second)
	c.Get("vk_none")
	if backend.gets != 2 {
		t.Fatalf("backend asked %d times after the miss expired, want 2", backend.gets)
	}
	if err := a.Put("vk_1", Entry{Scheme: signerapi.SchemeEcdsa2p, Share: testShare}); err != nil {
		t.Fatal(err)
	}
	// A key another store created is found in the backend on first use, and
	// held from then on.
	gets := backend.gets
	if e, ok := c.Get("vk_1"); !ok || !bytes.Equal(e.Share, testShare) {
		t.Fatal("the other store's entry was not found in the backend")
	}
	c.Get("vk_1")
	if backend.gets != gets+1 {
		t.Fatalf("backend asked %d times for a held key, want 1", backend.gets-gets)
	}
	// A Put of an id the backend holds already is refused.
	if err := c.Put("vk_1", Entry{Scheme: signerapi.SchemeEcdsa2p, Share: testShare}); !errors.Is(err, ErrHeld) {
		t.Fatalf("Put of an id another store created: %v, want ErrHeld", err)
	}
	if err := c.Put("vk_2", Entry{Scheme: signerapi.SchemeEcdsa2p, Share: testShare}); err != nil {
		t.Fatal(err)
	}
	// A sync adds what the backend has that memory lacks: nothing now, vk_1 is held.
	added, dropped, err := c.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 || len(dropped) != 0 {
		t.Fatalf("sync: added %v dropped %v", added, dropped)
	}
	d, err := OpenStore(backend, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if ids := d.IDs(); len(ids) != 2 {
		t.Fatalf("a store opened on the shared backend holds %v", ids)
	}
	if err := a.Delete("vk_1"); err != nil {
		t.Fatal(err)
	}
	added, dropped, err = c.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 || len(dropped) != 1 || dropped[0] != "vk_1" {
		t.Fatalf("sync after a delete: added %v dropped %v", added, dropped)
	}
	if _, ok := c.Get("vk_1"); ok {
		t.Fatal("the deleted entry is still held after the sync")
	}
	if _, ok := c.Get("vk_2"); !ok {
		t.Fatal("the store's own entry was dropped by the sync")
	}
}

func TestParseMasterKey(t *testing.T) {
	if _, err := ParseMasterKey("abc"); err == nil {
		t.Fatal("a short key was accepted")
	}
	if _, err := ParseMasterKey(strings.Repeat("zz", 32)); err == nil {
		t.Fatal("a non-hex key was accepted")
	}
	k, err := ParseMasterKey(strings.Repeat("0f", 32))
	if err != nil || len(k) != 32 {
		t.Fatalf("a 64-hex key: %v %d", err, len(k))
	}
}

func TestOpenBackend(t *testing.T) {
	dir := t.TempDir()
	for _, spec := range []string{filepath.Join(dir, "a.json"), "file:" + filepath.Join(dir, "b.json")} {
		b, err := OpenBackend(spec)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		if _, ok := b.(*FileBackend); !ok {
			t.Fatalf("%s: %T, want a file", spec, b)
		}
	}
	for _, spec := range []string{"s3://bucket/prefix", "gs://bucket/prefix"} {
		if _, err := OpenBackend(spec); err == nil || !strings.Contains(err.Error(), "hosted") {
			t.Fatalf("%s: %v", spec, err)
		}
	}
	if _, err := OpenBackend(""); err == nil {
		t.Fatal("an empty spec was accepted")
	}
}
