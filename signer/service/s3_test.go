package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/mishmar-io/mishmar/signerapi"
)

// fakeS3 is as much of the S3 API as the backend uses: path-style objects
// with conditional creation, and ListObjectsV2.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte // "bucket/key"
	puts    int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(name, "/")
	xmlError := func(status int, code string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
	}
	switch {
	case r.Method == http.MethodGet && key == "" && r.URL.Query().Get("list-type") == "2":
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range f.objects {
			b, obj, _ := strings.Cut(k, "/")
			if b == bucket && strings.HasPrefix(obj, prefix) {
				keys = append(keys, obj)
			}
		}
		sort.Strings(keys)
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
		fmt.Fprintf(&sb, "<Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>", bucket, prefix, len(keys))
		for _, k := range keys {
			fmt.Fprintf(&sb, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", k, len(f.objects[bucket+"/"+k]))
		}
		sb.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, sb.String())
	case r.Method == http.MethodPut && key != "":
		f.puts++
		if _, exists := f.objects[name]; exists && r.Header.Get("If-None-Match") == "*" {
			xmlError(http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.objects[name] = body
		w.Header().Set("ETag", `"etag"`)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && key != "":
		body, ok := f.objects[name]
		if !ok {
			xmlError(http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	case r.Method == http.MethodDelete && key != "":
		delete(f.objects, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		xmlError(http.StatusBadRequest, "NotImplemented")
	}
}

func newS3Backend(t *testing.T, fake *fakeS3, spec string) *S3Backend {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	b, err := OpenS3(context.Background(), spec, S3Options{Endpoint: srv.URL, Region: "us-east-1", AccessKey: "test", SecretKey: "test", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestS3BackendHoldsSealedEntriesAndRefusesASecondCreate(t *testing.T) {
	fake := &fakeS3{objects: map[string][]byte{}}
	b := newS3Backend(t, fake, "s3://keys-bucket/signer/keys/")
	if b.Bucket() != "keys-bucket" || b.Prefix() != "signer/keys/" {
		t.Fatalf("parsed: %q %q", b.Bucket(), b.Prefix())
	}
	st, err := OpenStore(b, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put("vk_1", Entry{Scheme: signerapi.SchemeFrostEd25519, Share: testShare}); err != nil {
		t.Fatal(err)
	}
	// The object is sealed JSON under the prefix, and the share is not in it.
	raw, ok := fake.objects["keys-bucket/signer/keys/vk_1.json"]
	if !ok {
		t.Fatalf("objects: %v", fake.objects)
	}
	var sealed Sealed
	if err := json.Unmarshal(raw, &sealed); err != nil || sealed.Scheme != signerapi.SchemeFrostEd25519 || sealed.Ciphertext == "" {
		t.Fatalf("object: %s %v", raw, err)
	}
	if bytes.Contains(raw, []byte("c2VjcmV0")) {
		t.Fatal("the share is in the clear in the bucket")
	}
	// Creating is conditional: a second store on the same bucket cannot
	// create the same id, and it finds the first one's entry on first use.
	other, err := OpenStore(b, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Put("vk_1", Entry{Scheme: signerapi.SchemeFrostEd25519, Share: testShare}); !errors.Is(err, ErrHeld) {
		t.Fatalf("second create: %v, want ErrHeld", err)
	}
	if e, ok := other.Get("vk_1"); !ok || !bytes.Equal(e.Share, testShare) {
		t.Fatal("the other store did not find the entry in the bucket")
	}
	if ids, err := b.List(); err != nil || len(ids) != 1 || ids[0] != "vk_1" {
		t.Fatalf("list: %v %v", ids, err)
	}
	if _, ok, err := b.Get("vk_none"); ok || err != nil {
		t.Fatalf("missing object: %v %v", ok, err)
	}
	if err := st.Delete("vk_1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.objects["keys-bucket/signer/keys/vk_1.json"]; ok {
		t.Fatal("the object was not deleted")
	}
	if err := b.Delete("vk_none"); err != nil {
		t.Fatalf("deleting a missing object: %v", err)
	}
	// Objects outside the layout are ignored by List.
	fake.objects["keys-bucket/signer/keys/notes.txt"] = []byte("x")
	fake.objects["keys-bucket/signer/keys/sub/vk_2.json"] = []byte("{}")
	fake.objects["keys-bucket/other/vk_3.json"] = []byte("{}")
	if ids, err := b.List(); err != nil || len(ids) != 0 {
		t.Fatalf("list with strangers: %v %v", ids, err)
	}
}

func TestOpenS3Specs(t *testing.T) {
	for _, bad := range []string{"s3://", "http://bucket/x", "s3:///prefix"} {
		if _, err := OpenS3(context.Background(), bad, S3Options{}); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
	if _, err := OpenS3(context.Background(), "s3://b/p", S3Options{AccessKey: "k"}); err == nil {
		t.Fatal("an access key without a secret was accepted")
	}
	b, err := OpenS3(context.Background(), "gs://b", S3Options{AccessKey: "k", SecretKey: "s"})
	if err != nil || b.Prefix() != "" {
		t.Fatalf("gs: %v %q", err, b.Prefix())
	}
	t.Setenv(EnvS3Endpoint, "http://127.0.0.1:1")
	t.Setenv(EnvS3AccessKey, "k")
	t.Setenv(EnvS3SecretKey, "s")
	if back, err := OpenBackend("s3://bucket/keys"); err != nil {
		t.Fatal(err)
	} else if _, ok := back.(*S3Backend); !ok {
		t.Fatalf("OpenBackend(s3://): %T", back)
	}
}
