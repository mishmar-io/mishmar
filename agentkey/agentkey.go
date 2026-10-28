// Package agentkey is an agent's identity: an Ed25519 key whose public half
// the engine stores when the key is issued, and whose private half never
// leaves the agent. Every proposal is signed with it, so the engine knows
// which agent asked, and the ledger carries that signature for anyone to check.
//
// The engine issues the key and shows its secret once, as
// msk_<prefix>_<seed>: a label, then the 32-byte seed in base64url without
// padding. The key id (key_…) is separate and public; the agent sends it with
// every proposal so the engine can look the public key up.
package agentkey

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
)

// Key is the agent's private key.
type Key struct {
	priv ed25519.PrivateKey
}

// Parse reads the secret the engine showed when the key was issued.
func Parse(secret string) (*Key, error) {
	// msk_<prefix>_<seed>: the seed is base64url and may itself contain '_',
	// so split on the first two underscores only.
	parts := strings.SplitN(strings.TrimSpace(secret), "_", 3)
	if len(parts) != 3 || parts[0] != "msk" {
		return nil, errors.New("agentkey: malformed agent secret: expected msk_<prefix>_<seed>")
	}
	seed, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("agentkey: malformed agent secret: the seed is not 32 base64url bytes")
	}
	return FromSeed(seed), nil
}

// FromSeed makes the key from its 32-byte seed.
func FromSeed(seed []byte) *Key {
	return &Key{priv: ed25519.NewKeyFromSeed(seed)}
}

// Secret renders a seed the way the engine shows it, with the given prefix
// (the engine uses "msk_" plus six characters of the key id).
func Secret(prefix string, seed []byte) string {
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(seed)
}

// Sign returns the base64 (standard, padded) Ed25519 signature of msg.
func (k *Key) Sign(msg []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, msg))
}

// PublicKey is the public half.
func (k *Key) PublicKey() ed25519.PublicKey {
	return k.priv.Public().(ed25519.PublicKey)
}

// PublicKeyB64 is the public half as the API stores and returns it: base64,
// standard alphabet, padded.
func (k *Key) PublicKeyB64() string {
	return base64.StdEncoding.EncodeToString(k.PublicKey())
}

// ParsePublicKey turns the API's base64 form back into a key.
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("agentkey: malformed public key")
	}
	return ed25519.PublicKey(raw), nil
}

// Verify checks a base64 signature over msg with a public key in the API's
// base64 form. A malformed key or signature is simply not valid.
func Verify(publicKeyB64 string, msg []byte, signatureB64 string) bool {
	pub, err := ParsePublicKey(publicKeyB64)
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}
