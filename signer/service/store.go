package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/signerapi"
)

// Entry is one key's share in the clear, as the scheme's package marshals it.
type Entry struct {
	Scheme signerapi.Scheme
	Share  json.RawMessage
}

// Sealed is an entry as a backend keeps it: the share under AES-256-GCM with
// the master key, the key id and scheme bound as associated data so an
// entry cannot be moved to another id or scheme.
type Sealed struct {
	Scheme     signerapi.Scheme `json:"scheme"`
	Nonce      string           `json:"nonce"`      // base64
	Ciphertext string           `json:"ciphertext"` // base64
}

// Backend keeps sealed entries and never sees a share in the clear: the key
// file when self-hosted, object storage on the hosted tier. Put creates and
// refuses an id already present (ErrHeld), which is what keeps two key
// generations for one id from both succeeding across replicas. The Store
// calls List and Get at open and on each sync, never on a request.
type Backend interface {
	List() ([]string, error)
	Get(id string) (Sealed, bool, error)
	Put(id string, e Sealed) error
	Delete(id string) error
}

// ErrHeld is Put's refusal of an id already present.
var ErrHeld = errors.New("key is held already")

// MasterKeyEnv names the variable the master key is read from: 64 hex
// characters (`openssl rand -hex 32`), kept apart from the backend.
const MasterKeyEnv = "MISHMAR_SIGNER_KEY"

// ParseMasterKey accepts the 64-hex-character master key.
func ParseMasterKey(hexKey string) ([]byte, error) {
	k, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil || len(k) != 32 {
		return nil, fmt.Errorf("%s must be 64 hex characters (32 bytes)", MasterKeyEnv)
	}
	return k, nil
}

// Store is the service's shares: every entry of the backend, opened under
// the master key and held in memory, so no request waits on the backing
// store. Open loads and opens everything, which is also how a wrong master
// key fails at startup and not on the first order. Sync picks up entries
// another replica wrote and drops the ones it deleted. Nonces and sessions
// never go here: they live in memory only.
type Store struct {
	backend Backend
	aead    cipher.AEAD
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]held
	missing map[string]time.Time // ids the backend lacked, and until when that answer stands
	gen     uint64               // counts Puts, so a sync never drops an entry put after it listed
}

type held struct {
	Entry
	gen uint64
}

// MissingTTL is how long a miss in the backend is remembered, so a stream
// of unknown ids never reaches the backend on every request.
const MissingTTL = 5 * time.Second

// OpenStore opens every entry of the backend under the master key.
func OpenStore(b Backend, masterKey []byte) (*Store, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("signer service: master key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{backend: b, aead: aead, now: time.Now, entries: map[string]held{}, missing: map[string]time.Time{}}
	if _, _, err := s.Sync(); err != nil {
		return nil, err
	}
	return s, nil
}

// Get is the key's share, if held. A key not in memory is looked for in the
// backend once, which is how a cell that takes on an account finds the
// account's shares without waiting for a sync; an id the backend lacks is
// remembered as missing for MissingTTL. The backend is read outside the
// lock, so other requests go on meanwhile.
func (s *Store) Get(id string) (Entry, bool) {
	s.mu.Lock()
	if h, ok := s.entries[id]; ok {
		s.mu.Unlock()
		return h.Entry, true
	}
	if until, ok := s.missing[id]; ok && s.now().Before(until) {
		s.mu.Unlock()
		return Entry{}, false
	}
	s.mu.Unlock()

	sealed, ok, err := s.backend.Get(id)
	if err != nil {
		return Entry{}, false // not remembered: the next request asks again
	}
	if !ok {
		s.mu.Lock()
		s.missing[id] = s.now().Add(MissingTTL)
		s.mu.Unlock()
		return Entry{}, false
	}
	e, err := s.open(id, sealed)
	if err != nil {
		return Entry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, dup := s.entries[id]; dup {
		return h.Entry, true
	}
	s.entries[id] = held{Entry: e}
	delete(s.missing, id)
	return e, true
}

// Put seals and stores a key's share; an id already held, here or in the
// backend, is refused with ErrHeld.
func (s *Store) Put(id string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.entries[id]; dup {
		return fmt.Errorf("signer service: %s: %w", id, ErrHeld)
	}
	sealed, err := s.seal(id, e)
	if err != nil {
		return err
	}
	if err := s.backend.Put(id, sealed); err != nil {
		return fmt.Errorf("signer service: %w", err)
	}
	s.gen++
	s.entries[id] = held{Entry: e, gen: s.gen}
	delete(s.missing, id)
	return nil
}

// Delete forgets a key's share; an id not held is no error.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.backend.Delete(id); err != nil {
		return fmt.Errorf("signer service: %w", err)
	}
	delete(s.entries, id)
	return nil
}

// IDs lists the keys held, sorted.
func (s *Store) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.entries))
	for id := range s.entries {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Sync reconciles memory with the backend: ids the backend has and memory
// lacks are fetched and opened, ids memory has and the backend lacks are
// dropped. The backend is read outside the lock, so requests go on during a
// slow listing. It returns what was added and dropped, sorted.
func (s *Store) Sync() (added, dropped []string, err error) {
	s.mu.Lock()
	start := s.gen
	known := make(map[string]bool, len(s.entries))
	for id := range s.entries {
		known[id] = true
	}
	s.mu.Unlock()

	ids, err := s.backend.List()
	if err != nil {
		return nil, nil, fmt.Errorf("signer service: %w", err)
	}
	listed := make(map[string]bool, len(ids))
	fetched := map[string]Entry{}
	for _, id := range ids {
		listed[id] = true
		if known[id] {
			continue
		}
		sealed, ok, err := s.backend.Get(id)
		if err != nil {
			return nil, nil, fmt.Errorf("signer service: %w", err)
		}
		if !ok {
			continue // deleted between the listing and the fetch
		}
		e, err := s.open(id, sealed)
		if err != nil {
			return nil, nil, err
		}
		fetched[id] = e
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range fetched {
		if _, dup := s.entries[id]; dup {
			continue
		}
		s.entries[id] = held{Entry: e}
		added = append(added, id)
	}
	for id, h := range s.entries {
		if !listed[id] && h.gen <= start {
			delete(s.entries, id)
			dropped = append(dropped, id)
		}
	}
	sort.Strings(added)
	sort.Strings(dropped)
	return added, dropped, nil
}

func (s *Store) seal(id string, e Entry) (Sealed, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, err
	}
	ct := s.aead.Seal(nil, nonce, e.Share, aad(id, e.Scheme))
	return Sealed{
		Scheme:     e.Scheme,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
	}, nil
}

func (s *Store) open(id string, se Sealed) (Entry, error) {
	nonce, err := base64.StdEncoding.DecodeString(se.Nonce)
	if err != nil {
		return Entry{}, fmt.Errorf("signer service: key %s: nonce is not base64", id)
	}
	ct, err := base64.StdEncoding.DecodeString(se.Ciphertext)
	if err != nil {
		return Entry{}, fmt.Errorf("signer service: key %s: ciphertext is not base64", id)
	}
	share, err := s.aead.Open(nil, nonce, ct, aad(id, se.Scheme))
	if err != nil {
		return Entry{}, fmt.Errorf("signer service: key %s cannot be opened: wrong %s, or the entry was moved", id, MasterKeyEnv)
	}
	return Entry{Scheme: se.Scheme, Share: share}, nil
}

// aad binds a sealed entry to its id and scheme.
func aad(id string, scheme signerapi.Scheme) []byte {
	return []byte(id + "\x00" + string(scheme))
}

// OpenBackend picks the backend from the --store spec: a path, or
// file:<path>, is the key file; s3://bucket/prefix and gs://bucket/prefix
// are object storage (OpenS3, options from the MISHMAR_S3_* variables).
func OpenBackend(spec string) (Backend, error) {
	switch {
	case spec == "":
		return nil, errors.New("signer service: --store is empty")
	case strings.HasPrefix(spec, "file:"):
		return OpenFile(strings.TrimPrefix(spec, "file:"))
	case strings.HasPrefix(spec, "s3://"), strings.HasPrefix(spec, "gs://"):
		return OpenS3(context.Background(), spec, S3OptionsFromEnv())
	case strings.Contains(spec, "://"):
		return nil, fmt.Errorf("signer service: --store %s: not a file path, file:<path>, s3:// or gs://", spec)
	}
	return OpenFile(spec)
}
