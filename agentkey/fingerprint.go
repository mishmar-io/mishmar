package agentkey

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// A key's fingerprint is what a person compares between the console and the
// machine that holds the key (contract 0.21.0, AgentKey.fingerprint): the
// first 80 bits of SHA-256 of the raw 32-byte Ed25519 public key, in
// Crockford's base32 (digits and upper-case letters without I, L, O and U,
// five bits a character, most significant first), sixteen characters in
// four groups of four joined by hyphens: 7KQ2-M9XD-4TPB-H3ZC. Its first two
// groups are the short code a trade sign-in shows on the machine and on the
// approval page (FingerprintCode). spec/vectors/agent-key-fingerprint.json
// pins the bytes.

// crockford is Crockford's base32 alphabet.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var fingerprintEncoding = base32.NewEncoding(crockford).WithPadding(base32.NoPadding)

// fingerprintBytes is how much of the SHA-256 a fingerprint shows: 80 bits.
const fingerprintBytes = 10

// Fingerprint is the fingerprint of an Ed25519 public key. A key that is not
// 32 bytes has none: "".
func Fingerprint(pub ed25519.PublicKey) string {
	if len(pub) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(pub)
	s := fingerprintEncoding.EncodeToString(sum[:fingerprintBytes])
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// FingerprintOf is the fingerprint of a public key in the API's base64 form
// (AgentKey.publicKey).
func FingerprintOf(publicKeyB64 string) (string, error) {
	pub, err := ParsePublicKey(publicKeyB64)
	if err != nil {
		return "", err
	}
	return Fingerprint(pub), nil
}

// FingerprintCode is the short code of a fingerprint: its first two groups,
// ABCD-EFGH. A string that is not a fingerprint has none: "".
func FingerprintCode(fingerprint string) string {
	if !ValidFingerprint(fingerprint) {
		return ""
	}
	return fingerprint[:9]
}

// ValidFingerprint says whether s has a fingerprint's shape (the contract's
// pattern ^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){3}$).
func ValidFingerprint(s string) bool {
	if len(s) != 19 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i%5 == 4 {
			if s[i] != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune(crockford, rune(s[i])) {
			return false
		}
	}
	return true
}

// Fingerprint is the fingerprint of the key's public half.
func (k *Key) Fingerprint() string { return Fingerprint(k.PublicKey()) }
