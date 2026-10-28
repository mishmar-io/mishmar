// Package ecdsa2p holds one agent's two-party ECDSA key over secp256k1: the
// key behind a Hyperliquid API wallet, or any other EVM-style signer.
//
// The secret is shared additively, x = x1 + x2 mod n: the agent holds x1,
// the engine x2, the public key is x1·G + x2·G, and neither party ever knows
// x. Dealing and the public side (the key, the address, the checks a share
// passes) are here. The signing protocol itself (two-party ECDSA) is not
// built yet, so Sign returns ErrNotImplemented; keys dealt now stay valid
// for it, since every two-party ECDSA scheme starts from exactly this sharing.
package ecdsa2p

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/sha3"
)

// Scheme names this key type in share files and trading account setups.
const Scheme = "ecdsa-2p-secp256k1"

// The two parties' identifiers.
const (
	AgentID  uint16 = 1
	EngineID uint16 = 2
)

// ErrNotImplemented is returned by Sign until the two-party signing protocol lands.
var ErrNotImplemented = errors.New("ecdsa2p: two-party signing is not implemented yet")

// Share is one party's additive share of the key with the public side of the
// group: both parties' public shares, from which the group's public key and
// address follow.
type Share struct {
	id     uint16
	secret *secp256k1.PrivateKey
	public [2]*secp256k1.PublicKey // by party, index id-1
}

// Deal makes a fresh key and splits it in two. It runs on the owner's machine;
// each share is a random scalar, so the whole secret is never even formed:
// the group's public key is computed from the two public shares.
func Deal() (agent, engine *Share, err error) {
	x1, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, fmt.Errorf("ecdsa2p: deal: %w", err)
	}
	x2, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, fmt.Errorf("ecdsa2p: deal: %w", err)
	}
	public := [2]*secp256k1.PublicKey{x1.PubKey(), x2.PubKey()}
	return &Share{id: AgentID, secret: x1, public: public}, &Share{id: EngineID, secret: x2, public: public}, nil
}

// ID is the party this share belongs to: AgentID or EngineID.
func (s *Share) ID() uint16 { return s.id }

// PublicKey is the group's public key, compressed (33 bytes).
func (s *Share) PublicKey() []byte {
	return groupKey(s.public).SerializeCompressed()
}

// Address is the group key's EVM address: 0x and 40 lower-case hex characters.
func (s *Share) Address() string {
	return AddressOf(groupKey(s.public))
}

// AddressOf is the EVM address of a public key: the last 20 bytes of the
// Keccak-256 of the uncompressed key without its 0x04 prefix.
func AddressOf(pub *secp256k1.PublicKey) string {
	raw := pub.SerializeUncompressed()[1:]
	h := sha3.NewLegacyKeccak256()
	h.Write(raw)
	sum := h.Sum(nil)
	return "0x" + hex.EncodeToString(sum[12:])
}

// Sign is the agent's or the engine's part of a two-party signature. Not yet.
func (s *Share) Sign(digest []byte) ([]byte, error) {
	return nil, ErrNotImplemented
}

func groupKey(public [2]*secp256k1.PublicKey) *secp256k1.PublicKey {
	var p1, p2, sum secp256k1.JacobianPoint
	public[0].AsJacobian(&p1)
	public[1].AsJacobian(&p2)
	secp256k1.AddNonConst(&p1, &p2, &sum)
	sum.ToAffine()
	return secp256k1.NewPublicKey(&sum.X, &sum.Y)
}

type shareFile struct {
	Scheme       string    `json:"scheme"`
	ID           uint16    `json:"id"`
	Secret       string    `json:"secret"`
	PublicShares [2]string `json:"publicShares"`
}

// MarshalJSON encodes the share for a file or a keystore entry. It holds the
// party's secret share: store it like a private key.
func (s *Share) MarshalJSON() ([]byte, error) {
	return json.Marshal(shareFile{
		Scheme: Scheme,
		ID:     s.id,
		Secret: base64.StdEncoding.EncodeToString(s.secret.Serialize()),
		PublicShares: [2]string{
			base64.StdEncoding.EncodeToString(s.public[0].SerializeCompressed()),
			base64.StdEncoding.EncodeToString(s.public[1].SerializeCompressed()),
		},
	})
}

// ParseShare decodes what MarshalJSON wrote and checks the secret share
// against the public share it claims to be.
func ParseShare(data []byte) (*Share, error) {
	var f shareFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("ecdsa2p: share: %w", err)
	}
	if f.Scheme != Scheme {
		return nil, fmt.Errorf("ecdsa2p: share: scheme %q is not %s", f.Scheme, Scheme)
	}
	if f.ID != AgentID && f.ID != EngineID {
		return nil, fmt.Errorf("ecdsa2p: share: party %d is neither the agent (%d) nor the engine (%d)", f.ID, AgentID, EngineID)
	}
	rawSecret, err := base64.StdEncoding.DecodeString(f.Secret)
	if err != nil || len(rawSecret) != 32 {
		return nil, errors.New("ecdsa2p: share: the secret is not 32 base64 bytes")
	}
	var public [2]*secp256k1.PublicKey
	for i, p := range f.PublicShares {
		raw, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			return nil, fmt.Errorf("ecdsa2p: share: public share %d: %w", i+1, err)
		}
		if public[i], err = secp256k1.ParsePubKey(raw); err != nil {
			return nil, fmt.Errorf("ecdsa2p: share: public share %d: %w", i+1, err)
		}
	}
	var scalar secp256k1.ModNScalar
	if overflow := scalar.SetByteSlice(rawSecret); overflow || scalar.IsZero() {
		return nil, errors.New("ecdsa2p: share: the secret is not a valid scalar")
	}
	secret := secp256k1.NewPrivateKey(&scalar)
	if subtle.ConstantTimeCompare(secret.PubKey().SerializeCompressed(), public[f.ID-1].SerializeCompressed()) != 1 {
		return nil, errors.New("ecdsa2p: share: the secret does not match the party's public share")
	}
	return &Share{id: f.ID, secret: secret, public: public}, nil
}
