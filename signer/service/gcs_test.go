package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mishmar-io/mishmar/kms/kmstest"
)

// fakeBucket is enough of the Cloud Storage JSON API for the backend: list by
// prefix, get, create-only upload, delete.
type fakeBucket struct {
	mu      sync.Mutex
	objects map[string][]byte
	bearer  []string
	lists   []string // the prefix of each list request
}

func newFakeBucket(t *testing.T) (*fakeBucket, *httptest.Server) {
	f := &fakeBucket{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bearer = append(f.bearer, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/shares/o"):
			name := r.URL.Query().Get("name")
			if r.URL.Query().Get("ifGenerationMatch") != "0" {
				http.Error(w, `{"error":{"message":"no precondition"}}`, http.StatusBadRequest)
				return
			}
			if _, exists := f.objects[name]; exists {
				http.Error(w, `{"error":{"message":"exists"}}`, http.StatusPreconditionFailed)
				return
			}
			body, _ := io.ReadAll(r.Body)
			f.objects[name] = body
			_, _ = w.Write([]byte(`{"name":"` + name + `"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/shares/o":
			prefix := r.URL.Query().Get("prefix")
			f.lists = append(f.lists, prefix)
			var items []map[string]string
			for name := range f.objects {
				if strings.HasPrefix(name, prefix) {
					items = append(items, map[string]string{"name": name})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case strings.HasPrefix(r.URL.Path, "/storage/v1/b/shares/o/"):
			name, _ := unescapePath(strings.TrimPrefix(r.URL.Path, "/storage/v1/b/shares/o/"))
			body, exists := f.objects[name]
			switch r.Method {
			case http.MethodGet:
				if !exists {
					http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
					return
				}
				_, _ = w.Write(body)
			case http.MethodDelete:
				if !exists {
					http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
					return
				}
				delete(f.objects, name)
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			http.Error(w, `{"error":{"message":"`+r.Method+` `+r.URL.Path+`"}}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func unescapePath(s string) (string, error) {
	return strings.NewReplacer("%2F", "/").Replace(s), nil
}

func TestGCSBackendKeepsEntriesUnderItsPrefixAsTheWorkload(t *testing.T) {
	kmstest.New(t).Use(t) // the metadata server the token comes from
	bucket, srv := newFakeBucket(t)
	t.Setenv(EnvGCSEndpoint, srv.URL)
	b, err := OpenBackend("gs://shares/5nowodujy4ni/shares")
	if err != nil {
		t.Fatal(err)
	}
	g, ok := b.(*GCSBackend)
	if !ok || g.Bucket() != "shares" || g.Prefix() != "5nowodujy4ni/shares/" {
		t.Fatalf("backend %#v", b)
	}
	if ids, err := g.List(); err != nil || len(ids) != 0 {
		t.Fatalf("empty list: %v %v", ids, err)
	}
	e := Sealed{Scheme: "frost-secp256k1", Nonce: "bm9uY2U=", Ciphertext: "Y3Q="}
	if err := g.Put("key_a", e); err != nil {
		t.Fatal(err)
	}
	if err := g.Put("key_a", e); err != ErrHeld {
		t.Fatalf("a second create must be refused as held, got %v", err)
	}
	if err := g.Put("key_b", e); err != nil {
		t.Fatal(err)
	}
	got, found, err := g.Get("key_a")
	if err != nil || !found || got != e {
		t.Fatalf("get: %+v %v %v", got, found, err)
	}
	if _, found, err := g.Get("key_zzz"); err != nil || found {
		t.Fatalf("a missing entry: %v %v", found, err)
	}
	ids, err := g.List()
	if err != nil || len(ids) != 2 {
		t.Fatalf("list: %v %v", ids, err)
	}
	if err := g.Delete("key_a"); err != nil {
		t.Fatal(err)
	}
	if err := g.Delete("key_a"); err != nil {
		t.Fatalf("deleting a gone entry: %v", err)
	}
	if ids, _ := g.List(); len(ids) != 1 || ids[0] != "key_b" {
		t.Fatalf("after delete: %v", ids)
	}
	for _, name := range []string{"5nowodujy4ni/shares/key_b.json"} {
		if _, exists := bucket.objects[name]; !exists {
			t.Fatalf("objects %v", bucket.objects)
		}
	}
	for _, bearer := range bucket.bearer {
		if !strings.HasPrefix(bearer, "Bearer ") || len(bearer) < 10 {
			t.Fatalf("a call without the workload's token: %q", bearer)
		}
	}
	for _, prefix := range bucket.lists {
		if prefix != "5nowodujy4ni/shares/" {
			t.Fatalf("a list without the prefix, which a prefix grant refuses: %q", prefix)
		}
	}
}

func TestOpenBackendTellsS3AndGCSApart(t *testing.T) {
	kmstest.New(t).Use(t)
	if b, err := OpenBackend("gs://b/p"); err != nil {
		t.Fatal(err)
	} else if _, ok := b.(*GCSBackend); !ok {
		t.Fatalf("gs:// opened %T", b)
	}
	if b, err := OpenBackend("s3://b/p"); err != nil {
		t.Fatal(err)
	} else if _, ok := b.(*S3Backend); !ok {
		t.Fatalf("s3:// opened %T", b)
	}
}
