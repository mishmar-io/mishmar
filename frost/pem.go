package frost

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
)

// PublicKeyPEM renders the group's public key the way an exchange's key
// registration form takes it: SPKI, PEM.
func PublicKeyPEM(pub ed25519.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}
