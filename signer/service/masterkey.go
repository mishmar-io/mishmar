package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"github.com/mishmar-io/mishmar/kms"
)

// WrappedMasterKeyEnv names the variable that holds the master key wrapped by a KMS
// key, in base64, with the key's resource name in kms.KeyEnv. The value is no secret:
// it opens only for the identity KMS lets decrypt, and every opening is in the KMS
// audit log. `mishmar signer wrap-key` makes it.
const WrappedMasterKeyEnv = "MISHMAR_SIGNER_KEY_WRAPPED"

// masterKeyAAD is bound to the wrapped form, so nothing else wrapped under the same
// KMS key can be passed off as the master key. For gcloud kms encrypt it is the
// content of --additional-authenticated-data-file, with no line ending.
var masterKeyAAD = []byte("mishmar signer master key")

// MasterKeyFromEnv is the master key the service seals its shares under: the raw one
// in MISHMAR_SIGNER_KEY when self-hosted, or the wrapped one, unwrapped through KMS.
// Having a KMS key configured means the wrapped form is the only one: a raw key next
// to it is refused rather than used, and if KMS cannot unwrap, the service does not
// start.
func MasterKeyFromEnv(ctx context.Context) ([]byte, error) {
	raw, wrapped, kmsKey := os.Getenv(MasterKeyEnv), os.Getenv(WrappedMasterKeyEnv), os.Getenv(kms.KeyEnv)
	switch {
	case wrapped != "" && raw != "":
		return nil, fmt.Errorf("both %s and %s are set: remove the raw key, or the wrapping protects nothing", MasterKeyEnv, WrappedMasterKeyEnv)
	case wrapped != "" && kmsKey == "":
		return nil, fmt.Errorf("%s is set without %s, the KMS key that unwraps it", WrappedMasterKeyEnv, kms.KeyEnv)
	case wrapped == "" && kmsKey != "":
		return nil, fmt.Errorf("%s is set without %s: make it with mishmar signer wrap-key", kms.KeyEnv, WrappedMasterKeyEnv)
	case wrapped == "" && raw == "":
		return nil, fmt.Errorf("%s is not set: the master key the shares are sealed under (openssl rand -hex 32; keep it apart from the store)", MasterKeyEnv)
	case wrapped == "":
		return ParseMasterKey(raw)
	}
	client, err := kms.New(kmsKey)
	if err != nil {
		return nil, err
	}
	sealed, err := kms.Decode(wrapped)
	if err != nil {
		return nil, fmt.Errorf("%s is not base64", WrappedMasterKeyEnv)
	}
	master, err := client.Decrypt(ctx, sealed, masterKeyAAD)
	if err != nil {
		return nil, fmt.Errorf("signer service: KMS did not unwrap the master key, so the service does not start: %w", err)
	}
	if len(master) != 32 {
		return nil, errors.New("signer service: the unwrapped master key is not 32 bytes")
	}
	return master, nil
}

// WrapMasterKey wraps the raw master key in MISHMAR_SIGNER_KEY, so the shares already
// sealed under it keep opening, or a new one when that is unset, and returns the
// wrapped form in base64. It unwraps the result again first, so a key this identity
// could wrap and not open is found now. The raw key is never returned.
func WrapMasterKey(ctx context.Context) (string, error) {
	kmsKey := os.Getenv(kms.KeyEnv)
	if kmsKey == "" {
		return "", fmt.Errorf("%s is not set: the KMS key to wrap under", kms.KeyEnv)
	}
	client, err := kms.New(kmsKey)
	if err != nil {
		return "", err
	}
	var master []byte
	if raw := os.Getenv(MasterKeyEnv); raw != "" {
		if master, err = ParseMasterKey(raw); err != nil {
			return "", err
		}
	} else {
		master = make([]byte, 32)
		if _, err := rand.Read(master); err != nil {
			return "", err
		}
	}
	sealed, err := client.Encrypt(ctx, master, masterKeyAAD)
	if err != nil {
		return "", err
	}
	back, err := client.Decrypt(ctx, sealed, masterKeyAAD)
	if err != nil {
		return "", fmt.Errorf("this identity can wrap under the key and not unwrap: %w", err)
	}
	if !bytes.Equal(back, master) {
		return "", errors.New("the wrapped master key does not unwrap to itself")
	}
	return kms.Encode(sealed), nil
}
