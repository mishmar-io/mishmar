// Package agentkey is an agent's identity: an Ed25519 key whose public half
// the engine stores when the key is issued, and whose private half never
// leaves the agent. Every proposal is signed with it, so the engine knows
// which agent asked, and the ledger carries that signature for anyone to check.
//
// The engine issues the key and shows its secret once, the agent's API key:
//
//	msk_<environment>_<account suffix>_<key random part>_<seed>
//
// The seed is the 32-byte Ed25519 seed in base64url without padding. Before
// it comes what names the engine and the key, so an agent needs nothing
// else: the environment (testnet, prod, … for hosted Mishmar; anything else
// means the engine URL is given by hand) and the two parts of the key id,
// key_<account suffix>_<random part>, the public id the agent sends with
// every proposal so the engine can look the public key up. Keys issued
// before contract 0.19.0 are msk_<six characters>_<seed> and still parse;
// they name neither engine nor key id.
package agentkey

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Key is the agent's private key.
type Key struct {
	priv ed25519.PrivateKey
}

// Parsed is a secret taken apart: the key, and what the secret says about
// where it belongs. Environment, AccountID and KeyID are set for a secret
// of contract 0.19.0 or later and empty for an older one.
type Parsed struct {
	Key         *Key
	Environment string
	AccountID   string
	KeyID       string
}

// An environment is a short lowercase word (testnet, stage, prod, self); the
// parts of an id are lowercase letters and digits, the random part twelve of them.
var (
	environmentRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	idPartRE      = regexp.MustCompile(`^[a-z0-9]+$`)
)

// randomPartLen is the length of an id's random part (ids.New in the engine).
const randomPartLen = 12

// ParseSecret reads the secret the engine showed when the key was issued,
// in either format.
func ParseSecret(secret string) (Parsed, error) {
	s := strings.TrimSpace(secret)
	if !strings.HasPrefix(s, "msk_") {
		return Parsed{}, errors.New("agentkey: malformed agent secret: it starts with msk_")
	}
	// 0.19.0: five parts, the seed last. The seed is base64url and may itself
	// contain '_', so split for at most five. An older secret cannot pass
	// here: its seed would have to be split across the parts, leaving too
	// few characters for the last one to be a whole seed.
	if parts := strings.SplitN(s, "_", 5); len(parts) == 5 && environmentRE.MatchString(parts[1]) &&
		idPartRE.MatchString(parts[2]) && len(parts[3]) == randomPartLen && idPartRE.MatchString(parts[3]) {
		if seed, err := base64.RawURLEncoding.DecodeString(parts[4]); err == nil && len(seed) == ed25519.SeedSize {
			return Parsed{Key: FromSeed(seed), Environment: parts[1], AccountID: "acc_" + parts[2], KeyID: "key_" + parts[2] + "_" + parts[3]}, nil
		}
	}
	// Before 0.19.0: msk_<prefix>_<seed>, split on the first two underscores only.
	parts := strings.SplitN(s, "_", 3)
	if len(parts) != 3 || parts[1] == "" {
		return Parsed{}, errors.New("agentkey: malformed agent secret: expected msk_<environment>_<account>_<key>_<seed>, or msk_<prefix>_<seed>")
	}
	seed, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(seed) != ed25519.SeedSize {
		return Parsed{}, errors.New("agentkey: malformed agent secret: the seed is not 32 base64url bytes")
	}
	return Parsed{Key: FromSeed(seed)}, nil
}

// Parse reads the key out of a secret in either format.
func Parse(secret string) (*Key, error) {
	p, err := ParseSecret(secret)
	if err != nil {
		return nil, err
	}
	return p.Key, nil
}

// FromSeed makes the key from its 32-byte seed.
func FromSeed(seed []byte) *Key {
	return &Key{priv: ed25519.NewKeyFromSeed(seed)}
}

// NewSecret renders the secret of a key issued at contract 0.19.0 or later:
// the environment, the key id's account and random parts, and the seed.
func NewSecret(environment, keyID string, seed []byte) (string, error) {
	if !ValidEnvironment(environment) {
		return "", fmt.Errorf("agentkey: environment %q is not a lowercase word of up to 32 letters, digits and dashes", environment)
	}
	parts := strings.Split(keyID, "_")
	if len(parts) != 3 || parts[0] != "key" || !idPartRE.MatchString(parts[1]) || len(parts[2]) != randomPartLen || !idPartRE.MatchString(parts[2]) {
		return "", fmt.Errorf("agentkey: key id %q is not key_<account suffix>_<12 characters>", keyID)
	}
	if len(seed) != ed25519.SeedSize {
		return "", errors.New("agentkey: a seed is 32 bytes")
	}
	return "msk_" + environment + "_" + parts[1] + "_" + parts[2] + "_" + base64.RawURLEncoding.EncodeToString(seed), nil
}

// ValidEnvironment reports whether s can name an environment in a secret.
func ValidEnvironment(s string) bool { return environmentRE.MatchString(s) }

// Secret renders a seed the way engines before 0.19.0 showed it, with the
// given prefix ("msk_" plus six characters of the key id).
func Secret(prefix string, seed []byte) string {
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(seed)
}

// Seed is the key's 32-byte seed, what a secret carries.
func (k *Key) Seed() []byte {
	return k.priv.Seed()
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
