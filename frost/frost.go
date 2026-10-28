// Package frost signs for an exchange API key that no one machine holds.
//
// The key is a two-party FROST key over Ed25519 (RFC 9591, Ed25519
// ciphersuite). The agent holds share 1, the engine share 2, and a signature
// needs both: a share alone says nothing about the key and cannot sign. What
// comes out is an ordinary Ed25519 signature, which the venue verifies against
// the group's public key like any other.
//
// One signature takes one round trip. Each party makes a one-time Commitment;
// with both commitments in hand each party makes its SignatureShare of the
// message; whoever holds both shares aggregates them. The engine is the
// coordinator: it commits, sends its commitment and the message to the agent's
// signer, gets the agent's commitment and share back, adds its own and
// aggregates. Commitments and nonces are single-use, and Sign clears them.
package frost

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/bytemare/ecc"
	bfrost "github.com/bytemare/frost"
	secretsharing "github.com/bytemare/secret-sharing"
	"github.com/bytemare/secret-sharing/keys"
)

// Scheme names this signing scheme in trading account setups and share files.
const Scheme = "frost-ed25519"

// The two parties' identifiers. FROST identifiers start at 1.
const (
	AgentID  uint16 = 1
	EngineID uint16 = 2
)

const suite = bfrost.Ed25519

// Share is one party's half of a dealt key, with the public side of the group:
// the group's public key and both parties' public key shares, which a party
// needs to check the other's signature share.
type Share struct {
	id       uint16
	keyShare *keys.KeyShare
	config   *bfrost.Configuration
}

// Deal makes a fresh key and splits it in two. It runs on the owner's machine,
// which is the only place the whole key ever exists, and only for the length
// of the call: the agent share goes to the agent's signer, the engine share to
// the engine, and the public key to the venue.
func Deal() (agent, engine *Share, err error) {
	g := suite.Group()
	secret := g.NewScalar().Random()
	defer secret.Zero()
	return dealFrom(secret)
}

// dealFrom shards one secret with the given polynomial coefficient (random
// when none is given); tests and the vector generator pass fixed values.
func dealFrom(secret *ecc.Scalar, coefficients ...*ecc.Scalar) (agent, engine *Share, err error) {
	g := suite.Group()
	if !suite.Available() {
		return nil, nil, errors.New("frost: the Ed25519 ciphersuite is not available in this build")
	}
	shares, err := secretsharing.ShardAndCommit(g, secret, 2, 2, coefficients...)
	if err != nil {
		return nil, nil, fmt.Errorf("frost: deal: %w", err)
	}
	if len(shares) != 2 || shares[0].Identifier() != AgentID || shares[1].Identifier() != EngineID {
		return nil, nil, errors.New("frost: deal: unexpected share identifiers")
	}
	config := &bfrost.Configuration{
		Ciphersuite:           suite,
		Threshold:             2,
		MaxSigners:            2,
		VerificationKey:       g.Base().Multiply(secret),
		SignerPublicKeyShares: []*keys.PublicKeyShare{shares[0].PublicKeyShare(), shares[1].PublicKeyShare()},
	}
	if err := config.Init(); err != nil {
		return nil, nil, fmt.Errorf("frost: deal: %w", err)
	}
	agent = &Share{id: AgentID, keyShare: shares[0], config: config}
	engine = &Share{id: EngineID, keyShare: shares[1], config: config}
	return agent, engine, nil
}

// ID is the party this share belongs to: AgentID or EngineID.
func (s *Share) ID() uint16 { return s.id }

// PublicKey is the group's Ed25519 public key, the one registered at the venue.
func (s *Share) PublicKey() ed25519.PublicKey {
	return ed25519.PublicKey(s.config.VerificationKey.Encode())
}

// shareFile is the stored form: everything base64, the scheme named so a file
// from another scheme is refused rather than misread.
type shareFile struct {
	Scheme   string `json:"scheme"`
	ID       uint16 `json:"id"`
	KeyShare string `json:"keyShare"`
	Group    string `json:"group"`
}

// MarshalJSON encodes the share for a file or a keystore entry. The result
// holds the party's secret share: store it like a private key.
func (s *Share) MarshalJSON() ([]byte, error) {
	return json.Marshal(shareFile{
		Scheme:   Scheme,
		ID:       s.id,
		KeyShare: base64.StdEncoding.EncodeToString(s.keyShare.Encode()),
		Group:    base64.StdEncoding.EncodeToString(s.config.Encode()),
	})
}

// ParseShare decodes what MarshalJSON wrote and checks the share against the
// group it claims to belong to.
func ParseShare(data []byte) (*Share, error) {
	var f shareFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("frost: share: %w", err)
	}
	if f.Scheme != Scheme {
		return nil, fmt.Errorf("frost: share: scheme %q is not %s", f.Scheme, Scheme)
	}
	if f.ID != AgentID && f.ID != EngineID {
		return nil, fmt.Errorf("frost: share: party %d is neither the agent (%d) nor the engine (%d)", f.ID, AgentID, EngineID)
	}
	rawShare, err := base64.StdEncoding.DecodeString(f.KeyShare)
	if err != nil {
		return nil, fmt.Errorf("frost: share: keyShare: %w", err)
	}
	rawGroup, err := base64.StdEncoding.DecodeString(f.Group)
	if err != nil {
		return nil, fmt.Errorf("frost: share: group: %w", err)
	}
	config := &bfrost.Configuration{}
	if err := config.Decode(rawGroup); err != nil {
		return nil, fmt.Errorf("frost: share: group: %w", err)
	}
	if config.Ciphersuite != suite || config.Threshold != 2 || config.MaxSigners != 2 {
		return nil, errors.New("frost: share: the group is not a two-party Ed25519 group")
	}
	if err := config.Init(); err != nil {
		return nil, fmt.Errorf("frost: share: group: %w", err)
	}
	keyShare := keys.NewKeyShareReceiver(suite.Group())
	if err := keyShare.Decode(rawShare); err != nil {
		return nil, fmt.Errorf("frost: share: keyShare: %w", err)
	}
	if keyShare.Identifier() != f.ID {
		return nil, errors.New("frost: share: the key share belongs to another party")
	}
	if err := config.ValidateKeyShare(keyShare); err != nil {
		return nil, fmt.Errorf("frost: share: %w", err)
	}
	return &Share{id: f.ID, keyShare: keyShare, config: config}, nil
}

// Commitment is a party's one-time nonce commitment, made before the message
// is known and sent to the other party in the clear.
type Commitment struct{ c *bfrost.Commitment }

// Bytes is the wire form.
func (c Commitment) Bytes() []byte { return c.c.Encode() }

// Party is the commitment's maker: AgentID or EngineID.
func (c Commitment) Party() uint16 { return c.c.SignerID }

// ParseCommitment decodes a commitment from the wire.
func ParseCommitment(b []byte) (Commitment, error) {
	c := &bfrost.Commitment{}
	if err := c.Decode(b); err != nil {
		return Commitment{}, fmt.Errorf("frost: commitment: %w", err)
	}
	if c.Group != suite.Group() {
		return Commitment{}, errors.New("frost: commitment: not an Ed25519 commitment")
	}
	return Commitment{c: c}, nil
}

// SignatureShare is one party's share of a signature over one message.
type SignatureShare struct{ s *bfrost.SignatureShare }

// Bytes is the wire form.
func (s SignatureShare) Bytes() []byte { return s.s.Encode() }

// Party is the share's maker: AgentID or EngineID.
func (s SignatureShare) Party() uint16 { return s.s.SignerIdentifier }

// ParseSignatureShare decodes a signature share from the wire.
func ParseSignatureShare(b []byte) (SignatureShare, error) {
	s := &bfrost.SignatureShare{}
	if err := s.Decode(b); err != nil {
		return SignatureShare{}, fmt.Errorf("frost: signature share: %w", err)
	}
	if s.Group != suite.Group() {
		return SignatureShare{}, errors.New("frost: signature share: not an Ed25519 share")
	}
	return SignatureShare{s: s}, nil
}

// lib serializes every call into the FROST library: it hashes through
// package-level state (one SHA-512 per ciphersuite), so two signatures at once
// corrupt each other whichever signers they belong to. At half a millisecond
// a signature the lock is no constraint.
var lib sync.Mutex

// Party signs with a share. It holds the nonces between Commit and Sign, so
// the same Party must do both; it is safe for concurrent use.
type Party struct {
	share  *Share
	signer *bfrost.Signer
}

// Party makes a signing party for the share.
func (s *Share) Party() (*Party, error) {
	signer, err := s.config.Signer(s.keyShare)
	if err != nil {
		return nil, fmt.Errorf("frost: %w", err)
	}
	return &Party{share: s, signer: signer}, nil
}

// Commit makes a one-time commitment for one signature. The nonces behind it
// stay inside the Party until Sign uses them.
func (p *Party) Commit() Commitment {
	lib.Lock()
	defer lib.Unlock()
	return Commitment{c: p.signer.Commit()}
}

// Sign makes this party's share of the signature over message, given both
// commitments (its own, from Commit, and the other party's). The commitment
// used is cleared: a second Sign with it fails.
func (p *Party) Sign(message []byte, commitments ...Commitment) (SignatureShare, error) {
	list, err := p.share.list(commitments)
	if err != nil {
		return SignatureShare{}, err
	}
	lib.Lock()
	defer lib.Unlock()
	share, err := p.signer.Sign(message, list)
	if err != nil {
		return SignatureShare{}, fmt.Errorf("frost: sign: %w", err)
	}
	return SignatureShare{s: share}, nil
}

// list checks that the commitments are one from each party and sorts them as
// the protocol requires.
func (s *Share) list(commitments []Commitment) (bfrost.CommitmentList, error) {
	if len(commitments) != 2 {
		return nil, fmt.Errorf("frost: a signature needs both parties' commitments, got %d", len(commitments))
	}
	list := make(bfrost.CommitmentList, 0, 2)
	for _, c := range commitments {
		if c.c == nil {
			return nil, errors.New("frost: empty commitment")
		}
		list = append(list, c.c)
	}
	list.Sort()
	if err := s.config.ValidateCommitmentList(list); err != nil {
		return nil, fmt.Errorf("frost: commitments: %w", err)
	}
	return list, nil
}

// VerifyShare checks the other party's signature share before it is used, so
// a bad share is named rather than discovered as an invalid signature.
func (s *Share) VerifyShare(message []byte, share SignatureShare, commitments ...Commitment) error {
	list, err := s.list(commitments)
	if err != nil {
		return err
	}
	lib.Lock()
	defer lib.Unlock()
	if err := s.config.VerifySignatureShare(share.s, message, list); err != nil {
		return fmt.Errorf("frost: party %d's share does not verify: %w", share.Party(), err)
	}
	return nil
}

// Aggregate combines both parties' shares into the Ed25519 signature over
// message (64 bytes, R then S, as RFC 8032 lays it out) and verifies it
// against the group's public key before returning it.
func (s *Share) Aggregate(message []byte, commitments []Commitment, shares []SignatureShare) ([]byte, error) {
	list, err := s.list(commitments)
	if err != nil {
		return nil, err
	}
	if len(shares) != 2 {
		return nil, fmt.Errorf("frost: a signature needs both parties' shares, got %d", len(shares))
	}
	raw := make([]*bfrost.SignatureShare, 0, 2)
	for _, sh := range shares {
		if sh.s == nil {
			return nil, errors.New("frost: empty signature share")
		}
		raw = append(raw, sh.s)
	}
	lib.Lock()
	sig, err := s.config.AggregateSignatures(message, raw, list, true)
	lib.Unlock()
	if err != nil {
		return nil, fmt.Errorf("frost: aggregate: %w", err)
	}
	out := make([]byte, 0, ed25519.SignatureSize)
	out = append(out, sig.R.Encode()...)
	out = append(out, sig.Z.Encode()...)
	if len(out) != ed25519.SignatureSize || !ed25519.Verify(s.PublicKey(), message, out) {
		return nil, errors.New("frost: the aggregated signature does not verify as Ed25519")
	}
	return out, nil
}
