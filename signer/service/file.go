package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// FileBackend keeps the sealed entries in one JSON file, mode 0600, replaced
// atomically on every change: the self-hosted default. It is one process's
// file: List and Get answer from memory, so a change another process makes
// to the file is not seen. Replicas that share a backend use object storage.
type FileBackend struct {
	path string
	mu   sync.Mutex
	keys map[string]Sealed
}

// fileFormat is the version written. A file without one is from before
// sealing and is refused.
const fileFormat = 1

type storeFile struct {
	Version int               `json:"version"`
	Keys    map[string]Sealed `json:"keys"`
}

// OpenFile reads the file, or starts empty when there is none yet.
func OpenFile(path string) (*FileBackend, error) {
	b := &FileBackend{path: path, keys: map[string]Sealed{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return b, nil
	case err != nil:
		return nil, fmt.Errorf("signer service: key file: %w", err)
	}
	var f storeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("signer service: key file %s: %w", path, err)
	}
	if f.Version != fileFormat {
		return nil, fmt.Errorf("signer service: key file %s: format %d is not %d", path, f.Version, fileFormat)
	}
	if f.Keys != nil {
		b.keys = f.Keys
	}
	return b, nil
}

// Path is the file.
func (b *FileBackend) Path() string { return b.path }

func (b *FileBackend) List() ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.keys))
	for id := range b.keys {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (b *FileBackend) Get(id string) (Sealed, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.keys[id]
	return e, ok, nil
}

func (b *FileBackend) Put(id string, e Sealed) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, dup := b.keys[id]; dup {
		return fmt.Errorf("%s: %w", id, ErrHeld)
	}
	b.keys[id] = e
	if err := b.save(); err != nil {
		delete(b.keys, id)
		return err
	}
	return nil
}

func (b *FileBackend) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.keys[id]
	if !ok {
		return nil
	}
	delete(b.keys, id)
	if err := b.save(); err != nil {
		b.keys[id] = old
		return err
	}
	return nil
}

// save writes the file with mode 0600, replacing it atomically. b.mu is held.
func (b *FileBackend) save() error {
	raw, err := json.MarshalIndent(storeFile{Version: fileFormat, Keys: b.keys}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("signer service: key file: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".signer-keys-*.json")
	if err != nil {
		return fmt.Errorf("signer service: key file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), b.path)
}
