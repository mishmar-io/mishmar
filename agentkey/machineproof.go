package agentkey

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/mishmar-io/mishmar/canon"
)

// The machine proof is how a computer that signs in to trade shows, at the
// token step, that it holds the private half of the key it put on the
// authorize request (the trade sign-in of contract 0.21.0; the wiki's
// design/app-trading-plan.md section 2.2, review point 3). The machine signs,
// with that key, the canonical JSON of
//
//	{"code":"<SHA-256 of the authorization code, lower-case hex>",
//	 "issuer":"<the hosting API's base URL, as its iss parameter names it>",
//	 "kind":"mishmar-machine-proof",
//	 "publicKey":"<the key, 32 bytes, base64 standard padded>",
//	 "signedAt":"<RFC 3339 UTC, the fraction trimmed (SignedAtString)>"}
//
// and sends the signature (base64, standard, padded) as key_proof and the
// signedAt string as key_signed_at. The control plane rebuilds the bytes from
// the code it issued, its own base URL and the key stored at authorize, and
// takes the proof within 60 s of signedAt. Hashing the code keeps the code out
// of anything a log might print of the proof; the kind keeps the signature
// from passing for any other message the key signs (a request's or a
// proposal's canonical JSON has no "kind"). spec/vectors/machine-proof.json
// pins the bytes.

// MachineProofKind is the proof's kind field.
const MachineProofKind = "mishmar-machine-proof"

// MachineProofMaxAge is how far from now the control plane takes signedAt.
const MachineProofMaxAge = 60 * time.Second

// MachineProof is what a machine proves possession over.
type MachineProof struct {
	// Code is the authorization code as the callback carried it; the proof
	// signs its hash, never the code.
	Code string
	// Issuer is the hosting API's base URL, as the callback's iss names it
	// (https://api.mishmar.io).
	Issuer string
	// PublicKey is the key, base64 standard padded (AgentKey.publicKey).
	PublicKey string
	// SignedAt is the time as sent in key_signed_at (SignedAtString).
	SignedAt string
}

type machineProofBody struct {
	Code      string `json:"code"`
	Issuer    string `json:"issuer"`
	Kind      string `json:"kind"`
	PublicKey string `json:"publicKey"`
	SignedAt  string `json:"signedAt"`
}

// MachineProofBytes is what the machine signs.
func MachineProofBytes(p MachineProof) ([]byte, error) {
	if p.Code == "" || p.Issuer == "" || p.PublicKey == "" || p.SignedAt == "" {
		return nil, errors.New("agentkey: a machine proof needs the code, the issuer, the public key and the time")
	}
	sum := sha256.Sum256([]byte(p.Code))
	return canon.JSON(machineProofBody{
		Code: hex.EncodeToString(sum[:]), Issuer: p.Issuer, Kind: MachineProofKind, PublicKey: p.PublicKey, SignedAt: p.SignedAt,
	})
}

// SignMachineProof proves possession of the key for a code from issuer, at
// the given time: the signature and the signedAt string to send with it.
func (k *Key) SignMachineProof(code, issuer string, at time.Time) (proof, signedAt string, err error) {
	signedAt = SignedAtString(at)
	msg, err := MachineProofBytes(MachineProof{Code: code, Issuer: issuer, PublicKey: k.PublicKeyB64(), SignedAt: signedAt})
	if err != nil {
		return "", "", err
	}
	return k.Sign(msg), signedAt, nil
}

// VerifyMachineProof is the control plane's check of a proof: the signature
// over the bytes rebuilt from what it holds, and signedAt within
// MachineProofMaxAge of now. It returns why it refuses, or "" when the proof
// holds.
func VerifyMachineProof(p MachineProof, proofB64 string, now time.Time) string {
	at, err := time.Parse(time.RFC3339Nano, p.SignedAt)
	switch {
	case err != nil:
		return "key_signed_at is not an RFC 3339 time"
	case at.Sub(now) > MachineProofMaxAge || now.Sub(at) > MachineProofMaxAge:
		return "key_signed_at is more than 60 seconds from now"
	}
	msg, err := MachineProofBytes(p)
	if err != nil {
		return err.Error()
	}
	if !Verify(p.PublicKey, msg, proofB64) {
		return "key_proof is not the key's signature over this code"
	}
	return ""
}
