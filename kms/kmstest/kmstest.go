// Package kmstest is a stand-in for Cloud KMS and the metadata server, for tests:
// an HTTP server that takes the same requests, checks the token and the checksums,
// and encrypts for real, under a key of its own.
package kmstest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mishmar-io/mishmar/kms"
)

// Key is the resource name the fake answers for.
const Key = "projects/test/locations/global/keyRings/test/cryptoKeys/test"

const token = "fake-token"

// Fake is the stand-in. Point a client at it with Use.
type Fake struct {
	srv  *httptest.Server
	aead cipher.AEAD

	mu            sync.Mutex
	down          bool
	refuseDecrypt bool
	tokenFails    int
	encrypts      int
	decrypts      int
	tokens        int
}

// New starts a fake with a key of its own, so a ciphertext from one does not open
// on another.
func New(t testing.TB) *Fake {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	f := &Fake{aead: aead}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// Use points every client made during the test at this fake.
func (f *Fake) Use(t *testing.T) {
	t.Helper()
	t.Setenv(kms.EndpointEnv, f.srv.URL)
	t.Setenv(kms.MetadataHostEnv, strings.TrimPrefix(f.srv.URL, "http://"))
}

// Client is a client for this fake's key.
func (f *Fake) Client(t *testing.T) *kms.Client {
	t.Helper()
	f.Use(t)
	c, err := kms.New(Key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// SetDown makes the API answer 503, as an outage would.
func (f *Fake) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// RefuseDecrypt makes decrypt answer 403, as a key disabled or a permission lost.
func (f *Fake) RefuseDecrypt(refuse bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuseDecrypt = refuse
}

// FailTokens makes the metadata server fail the next n requests, as on a new pod.
func (f *Fake) FailTokens(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenFails = n
}

// Counts is how many calls were served: encrypts and decrypts that succeeded.
func (f *Fake) Counts() (encrypts, decrypts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.encrypts, f.decrypts
}

func crc(b []byte) string {
	return strconv.FormatUint(uint64(crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))), 10)
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": msg}})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/instance/service-accounts/default/token") {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			fail(w, http.StatusForbidden, "missing Metadata-Flavor")
			return
		}
		if f.tokenFails > 0 {
			f.tokenFails--
			fail(w, http.StatusServiceUnavailable, "the metadata server is starting")
			return
		}
		f.tokens++
		json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": 3599, "token_type": "Bearer"})
		return
	}
	if f.down {
		fail(w, http.StatusServiceUnavailable, "the service is down")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		fail(w, http.StatusUnauthorized, "bad token")
		return
	}
	switch r.URL.Path {
	case "/v1/" + Key + ":encrypt":
		f.encrypt(w, r)
	case "/v1/" + Key + ":decrypt":
		f.decrypt(w, r)
	default:
		fail(w, http.StatusNotFound, "no such key or method: "+r.URL.Path)
	}
}

func (f *Fake) encrypt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Plaintext []byte `json:"plaintext"`
		PlainCrc  string `json:"plaintextCrc32c"`
		AAD       []byte `json:"additionalAuthenticatedData"`
		AADCrc    string `json:"additionalAuthenticatedDataCrc32c"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.PlainCrc != crc(in.Plaintext) || in.AADCrc != crc(in.AAD) {
		fail(w, http.StatusBadRequest, "bad request or checksum")
		return
	}
	nonce := make([]byte, f.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ct := f.aead.Seal(nonce, nonce, in.Plaintext, in.AAD)
	f.encrypts++
	json.NewEncoder(w).Encode(map[string]any{
		"name": Key + "/cryptoKeyVersions/1", "ciphertext": ct, "ciphertextCrc32c": crc(ct),
		"verifiedPlaintextCrc32c": true, "verifiedAdditionalAuthenticatedDataCrc32c": true, "protectionLevel": "SOFTWARE",
	})
}

func (f *Fake) decrypt(w http.ResponseWriter, r *http.Request) {
	if f.refuseDecrypt {
		fail(w, http.StatusForbidden, "Permission 'cloudkms.cryptoKeyVersions.useToDecrypt' denied")
		return
	}
	var in struct {
		Ciphertext []byte `json:"ciphertext"`
		CtCrc      string `json:"ciphertextCrc32c"`
		AAD        []byte `json:"additionalAuthenticatedData"`
		AADCrc     string `json:"additionalAuthenticatedDataCrc32c"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.CtCrc != crc(in.Ciphertext) || in.AADCrc != crc(in.AAD) {
		fail(w, http.StatusBadRequest, "bad request or checksum")
		return
	}
	n := f.aead.NonceSize()
	if len(in.Ciphertext) < n {
		fail(w, http.StatusBadRequest, "Decryption failed: the ciphertext is invalid.")
		return
	}
	pt, err := f.aead.Open(nil, in.Ciphertext[:n], in.Ciphertext[n:], in.AAD)
	if err != nil {
		fail(w, http.StatusBadRequest, "Decryption failed: the ciphertext is invalid.")
		return
	}
	f.decrypts++
	json.NewEncoder(w).Encode(map[string]any{"plaintext": pt, "plaintextCrc32c": crc(pt), "usedPrimary": true, "protectionLevel": "SOFTWARE"})
}
