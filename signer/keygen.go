package signer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
)

// GeneratedKey is what two-party key generation leaves the signer with.
type GeneratedKey struct {
	// Scheme is SchemeECDSA2p or SchemeFrostEd25519.
	Scheme string
	// Share is the agent's share, as the config stores it.
	Share json.RawMessage
	// PublicKey is the key's public key: compressed SEC1 for ECDSA, the
	// 32-byte key for Ed25519.
	PublicKey []byte
	// Address is the API wallet's address, ECDSA keys only.
	Address string
}

// RunKeygen runs the agent's side of two-party key generation for a venue
// key the engine created without a share: two rounds over the agent API,
// the engine relaying to its own signer, after which this signer holds the
// agent's half, the engine's signer the engine's, and no machine ever held
// both. scheme is the venue key's: SchemeECDSA2p (the engine records the
// API wallet's address on the venue key) or SchemeFrostEd25519 (the engine
// records the public key the owner registers at the venue).
func RunKeygen(ctx context.Context, c *Client, venueKeyID, scheme string) (GeneratedKey, error) {
	switch scheme {
	case SchemeECDSA2p:
		key, err := runECDSAKeygen(ctx, c, venueKeyID)
		if err != nil {
			return GeneratedKey{}, err
		}
		share, err := json.Marshal(key)
		if err != nil {
			return GeneratedKey{}, err
		}
		return GeneratedKey{Scheme: scheme, Share: share, PublicKey: key.PublicKey(), Address: key.Address()}, nil
	case SchemeFrostEd25519:
		return runFrostKeygen(ctx, c, venueKeyID)
	}
	return GeneratedKey{}, fmt.Errorf("signer: key generation: scheme %q is not %s or %s", scheme, SchemeECDSA2p, SchemeFrostEd25519)
}

// runFrostKeygen is FROST's distributed key generation with the engine's
// signer: the engine opens, this signer answers with its commitment and its
// share for the engine, the engine closes with its share for this signer.
func runFrostKeygen(ctx context.Context, c *Client, venueKeyID string) (GeneratedKey, error) {
	kg, err := frost.NewKeygen(frost.AgentID)
	if err != nil {
		return GeneratedKey{}, err
	}
	defer kg.Abandon()
	r1, err := c.KeygenRound(ctx, api.KeygenRound{VenueKeyId: venueKeyID, Round: api.KeygenRoundRoundN1})
	if err != nil {
		return GeneratedKey{}, fmt.Errorf("signer: key generation round 1: %w", err)
	}
	first, err := decodeB64(r1.Message)
	if err != nil {
		return GeneratedKey{}, fmt.Errorf("signer: key generation round 1: %w", err)
	}
	reply, err := kg.Reply(first)
	if err != nil {
		return GeneratedKey{}, err
	}
	r2, err := c.KeygenRound(ctx, api.KeygenRound{VenueKeyId: venueKeyID, Round: api.KeygenRoundRoundN2, SessionId: &r1.SessionId, Message: ptr(base64.StdEncoding.EncodeToString(reply))})
	if err != nil {
		return GeneratedKey{}, fmt.Errorf("signer: key generation round 2: %w", err)
	}
	last, err := decodeB64(r2.Message)
	if err != nil {
		return GeneratedKey{}, fmt.Errorf("signer: key generation round 2: %w", err)
	}
	share, err := kg.Finish(last)
	if err != nil {
		return GeneratedKey{}, err
	}
	if r2.PublicKey != nil {
		pub, err := decodeB64(*r2.PublicKey)
		if err != nil || !bytes.Equal(pub, share.PublicKey()) {
			return GeneratedKey{}, errors.New("signer: key generation: the engine's public key is not the one this signer derived")
		}
	}
	raw, err := json.Marshal(share)
	if err != nil {
		return GeneratedKey{}, err
	}
	return GeneratedKey{Scheme: SchemeFrostEd25519, Share: raw, PublicKey: share.PublicKey()}, nil
}
