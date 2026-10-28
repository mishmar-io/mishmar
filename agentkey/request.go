package agentkey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/mishmar-io/mishmar/canon"
)

// Request header names of the agentAuth scheme (spec/openapi.yaml).
const (
	HeaderKeyID     = "X-Mishmar-Key-Id"
	HeaderSignedAt  = "X-Mishmar-Signed-At"
	HeaderNonce     = "X-Mishmar-Nonce"
	HeaderSignature = "X-Mishmar-Signature"
)

// Request is one HTTP request as the agentAuth scheme sees it: what the
// signer fills in before signing, and what the engine reads back from the
// request and its headers before verifying.
type Request struct {
	// Method in any case; it is signed upper case.
	Method string
	// Path without the server prefix: "/agent/decisions", not "/v1/agent/decisions".
	Path string
	// Query is the raw query string without the leading "?"; empty when none.
	Query string
	// Body is the request body; nil or empty when there is none.
	Body []byte
	// KeyID is the agent key's id (key_…), as sent in X-Mishmar-Key-Id.
	KeyID string
	// Nonce is unique per request for the key, as sent in X-Mishmar-Nonce.
	Nonce string
	// SignedAt is the time of signing exactly as sent in X-Mishmar-Signed-At:
	// RFC 3339 in UTC, the fraction trimmed of trailing zeros (SignedAtString).
	SignedAt string
}

// signedRequest is the canonical shape: absent fields are left out, so a GET
// without a query signs five keys, a POST with a body seven.
type signedRequest struct {
	BodySha256 string `json:"bodySha256,omitempty"`
	KeyID      string `json:"keyId"`
	Method     string `json:"method"`
	Nonce      string `json:"nonce"`
	Path       string `json:"path"`
	Query      string `json:"query,omitempty"`
	SignedAt   string `json:"signedAt"`
}

// SignedAtString renders a time the way the header carries it and the
// signature covers it: RFC 3339 in UTC, the fraction trimmed of trailing zeros.
func SignedAtString(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// NewNonce makes a fresh nonce: 16 random bytes, hex. The engine refuses a
// nonce it has seen from the same key within its replay window.
func NewNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// RequestSigningBytes is what the agent signs for a request: the canonical
// JSON of {bodySha256, keyId, method, nonce, path, query, signedAt}, with
// bodySha256 (lower-case hex SHA-256 of the body) only when there is a body
// and query only when it is not empty.
func RequestSigningBytes(r Request) ([]byte, error) {
	s := signedRequest{
		KeyID: r.KeyID, Method: strings.ToUpper(r.Method), Nonce: r.Nonce, Path: r.Path, Query: r.Query, SignedAt: r.SignedAt,
	}
	if len(r.Body) > 0 {
		sum := sha256.Sum256(r.Body)
		s.BodySha256 = hex.EncodeToString(sum[:])
	}
	return canon.JSON(s)
}

// SignRequest fills what the request lacks (a nonce, the time) and returns the
// four agentAuth headers for it.
func (k *Key) SignRequest(r Request) (map[string]string, error) {
	if r.Nonce == "" {
		nonce, err := NewNonce()
		if err != nil {
			return nil, err
		}
		r.Nonce = nonce
	}
	if r.SignedAt == "" {
		r.SignedAt = SignedAtString(time.Now())
	}
	msg, err := RequestSigningBytes(r)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		HeaderKeyID:     r.KeyID,
		HeaderSignedAt:  r.SignedAt,
		HeaderNonce:     r.Nonce,
		HeaderSignature: k.Sign(msg),
	}, nil
}

// VerifyRequest checks a request's signature against the agent's public key
// in the API's base64 form: the engine's side of the scheme. Freshness of
// SignedAt and nonce replay are the caller's checks.
func VerifyRequest(publicKeyB64 string, r Request, signatureB64 string) bool {
	msg, err := RequestSigningBytes(r)
	if err != nil {
		return false
	}
	return Verify(publicKeyB64, msg, signatureB64)
}
