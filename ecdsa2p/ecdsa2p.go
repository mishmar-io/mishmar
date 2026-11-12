// Package ecdsa2p is the two-party ECDSA key over secp256k1 behind a
// Hyperliquid API wallet, or any other EVM-style signer: Lindell's 2017
// protocol as Coinbase's cb-mpc implements it (ECDSA-2PC), through the
// mishmar-io fork of cb-mpc-go.
//
// The key is made by the protocol's own key generation between the agent's
// signer and the engine's, so no machine ever holds both shares, and a
// signature needs both. The engine is party 1: it holds the Paillier key,
// verifies the agent's last message, decrypts and outputs the signature. The
// agent is party 2: it proves its last message, so a signature that fails
// never leaks anything about the key.
//
// Signing is single-hop. Presigning runs the protocol's three messages that
// do not depend on the message ahead of time, for a batch of nonces; an
// order then carries only the agent's last message (Complete), which the
// engine verifies and finishes alone (Finish). A nonce signs once: Complete
// and Finish burn their slot before they use it, whatever the outcome, and
// a batch is kept in memory only.
//
// Key generation and presigning are sessions driven one message at a time
// (First, Reply, Last, Finish), with no transport of their own, so the
// engine can carry the messages between the two signers over its own API.
package ecdsa2p

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/mishmar-io/cb-mpc-go/pkg/mpc"
	"golang.org/x/crypto/sha3"
)

// Scheme names this key type in key files and connected account setups.
const Scheme = "ecdsa-2p-secp256k1"

// Role is which party a key belongs to.
type Role string

const (
	// RoleEngine is party 1: it holds the Paillier key and finishes signatures.
	RoleEngine Role = "engine"
	// RoleAgent is party 2: it proves its last message.
	RoleAgent Role = "agent"
)

func (r Role) self() (mpc.TwoPartySelf, error) {
	switch r {
	case RoleEngine:
		return mpc.TwoPartyP1, nil
	case RoleAgent:
		return mpc.TwoPartyP2, nil
	}
	return 0, fmt.Errorf("ecdsa2p: role %q is neither %s nor %s", r, RoleEngine, RoleAgent)
}

// Limits of a session.
const (
	// SessionTimeout bounds a key generation or presigning run from its start.
	SessionTimeout = time.Minute
	// MaxPresign is the most nonces one presigning run makes.
	MaxPresign = 1024
	// SIDHalfSize is the size of each party's half of a presigning session id.
	SIDHalfSize = 16
)

// Key is one party's key: the opaque key blob the library made, with the
// role and the joint public key. It is a secret: store it like a private key.
type Key struct {
	role Role
	blob []byte
	pub  *secp256k1.PublicKey
}

// Role is the party this key belongs to.
func (k *Key) Role() Role { return k.role }

// PublicKey is the joint public key, compressed (33 bytes).
func (k *Key) PublicKey() []byte { return k.pub.SerializeCompressed() }

// Address is the joint key's EVM address: 0x and 40 lower-case hex characters.
func (k *Key) Address() string { return AddressOf(k.pub) }

// AddressOf is the EVM address of a public key: the last 20 bytes of the
// Keccak-256 of the uncompressed key without its 0x04 prefix.
func AddressOf(pub *secp256k1.PublicKey) string {
	raw := pub.SerializeUncompressed()[1:]
	h := sha3.NewLegacyKeccak256()
	h.Write(raw)
	sum := h.Sum(nil)
	return "0x" + hex.EncodeToString(sum[12:])
}

type keyFile struct {
	Scheme    string `json:"scheme"`
	Role      Role   `json:"role"`
	Key       string `json:"key"`
	PublicKey string `json:"publicKey"`
}

// MarshalJSON encodes the key for a file or a keystore entry.
func (k *Key) MarshalJSON() ([]byte, error) {
	return json.Marshal(keyFile{
		Scheme: Scheme, Role: k.role,
		Key:       base64.StdEncoding.EncodeToString(k.blob),
		PublicKey: base64.StdEncoding.EncodeToString(k.PublicKey()),
	})
}

// ParseKey decodes what MarshalJSON wrote and checks the blob against the
// public key it claims.
func ParseKey(data []byte) (*Key, error) {
	var f keyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("ecdsa2p: key: %w", err)
	}
	if f.Scheme != Scheme {
		return nil, fmt.Errorf("ecdsa2p: key: scheme %q is not %s", f.Scheme, Scheme)
	}
	if _, err := f.Role.self(); err != nil {
		return nil, err
	}
	blob, err := base64.StdEncoding.DecodeString(f.Key)
	if err != nil || len(blob) == 0 {
		return nil, errors.New("ecdsa2p: key: the key is not base64")
	}
	k, err := keyFromBlob(f.Role, blob)
	if err != nil {
		return nil, err
	}
	claimed, err := base64.StdEncoding.DecodeString(f.PublicKey)
	if err != nil || hex.EncodeToString(claimed) != hex.EncodeToString(k.PublicKey()) {
		return nil, errors.New("ecdsa2p: key: the public key does not match the key")
	}
	return k, nil
}

// keyFromBlob wraps a library key blob, reading the joint public key from it.
func keyFromBlob(role Role, blob []byte) (*Key, error) {
	compressed, err := mpc.ECDSA2PGetPublicKeyCompressed(blob)
	if err != nil {
		return nil, fmt.Errorf("ecdsa2p: key: %w", err)
	}
	pub, err := secp256k1.ParsePubKey(compressed)
	if err != nil {
		return nil, fmt.Errorf("ecdsa2p: key: %w", err)
	}
	return &Key{role: role, blob: blob, pub: pub}, nil
}

// ---- sessions: a protocol run driven one message at a time ----

// session runs one library call in a goroutine over a transport whose
// messages are handed in and out by the caller.
type session struct {
	ctx    context.Context
	cancel context.CancelFunc
	in     chan []byte
	out    chan []byte
	done   chan error
	ended  bool
	err    error
	blob   []byte // what the call returned: a key blob or a presig blob
}

type transport struct{ s *session }

func (t transport) Send(ctx context.Context, _ int, msg []byte) error {
	select {
	case t.s.out <- append([]byte(nil), msg...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t transport) Receive(ctx context.Context, _ int) ([]byte, error) {
	select {
	case m := <-t.s.in:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t transport) ReceiveAll(ctx context.Context, from []int) ([][]byte, error) {
	out := make([][]byte, len(from))
	for i, f := range from {
		m, err := t.Receive(ctx, f)
		if err != nil {
			return nil, err
		}
		out[i] = m
	}
	return out, nil
}

func (t transport) Close() error { return nil }

func newSession(role Role, run func(ctx context.Context, job mpc.Job2P) ([]byte, error)) (*session, error) {
	self, err := role.self()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), SessionTimeout)
	s := &session{ctx: ctx, cancel: cancel, in: make(chan []byte, 1), out: make(chan []byte, 1), done: make(chan error, 1)}
	job := mpc.Job2P{Self: self, P1Name: string(RoleEngine), P2Name: string(RoleAgent), Transport: transport{s}}
	go func() {
		blob, err := run(ctx, job)
		s.blob = blob
		s.done <- err
	}()
	return s, nil
}

// deliver hands the peer's message to the protocol.
func (s *session) deliver(m []byte) error {
	if s.ended {
		return s.endedErr()
	}
	if len(m) == 0 {
		s.abandon()
		return errors.New("ecdsa2p: an empty message")
	}
	select {
	case s.in <- append([]byte(nil), m...):
		return nil
	case err := <-s.done:
		s.ended, s.err = true, err
		return s.endedErr()
	}
}

// next waits for the protocol's next message to the peer.
func (s *session) next() ([]byte, error) {
	if s.ended {
		return nil, s.endedErr()
	}
	select {
	case m := <-s.out:
		return m, nil
	case err := <-s.done:
		s.ended, s.err = true, err
		// The protocol may have sent its last message just before it ended.
		select {
		case m := <-s.out:
			return m, nil
		default:
		}
		return nil, s.endedErr()
	}
}

// wait blocks until the protocol returns.
func (s *session) wait() ([]byte, error) {
	if !s.ended {
		s.err = <-s.done
		s.ended = true
	}
	s.cancel()
	if s.err != nil {
		return nil, fmt.Errorf("ecdsa2p: %w", s.err)
	}
	return s.blob, nil
}

func (s *session) endedErr() error {
	s.cancel()
	if s.err != nil {
		return fmt.Errorf("ecdsa2p: the protocol failed: %w", s.err)
	}
	return errors.New("ecdsa2p: the protocol has ended")
}

// abandon stops the run; the goroutine sees the cancelled context.
func (s *session) abandon() {
	s.cancel()
	if !s.ended {
		go func() { <-s.done }()
		s.ended, s.err = true, errors.New("abandoned")
	}
}

// ---- key generation: three messages, the engine first ----

// Keygen is one run of two-party key generation. The engine calls First,
// then Last with the agent's reply; the agent calls Reply with the engine's
// first message, then Finish with its last. Neither side needs anything
// from the other beyond those messages, and the protocol agrees its own
// session id.
type Keygen struct {
	s    *session
	role Role
}

// NewKeygen starts a run as the role.
func NewKeygen(role Role) (*Keygen, error) {
	s, err := newSession(role, func(ctx context.Context, job mpc.Job2P) ([]byte, error) {
		return mpc.ECDSA2PDKG(ctx, job, mpc.CurveSecp256k1)
	})
	if err != nil {
		return nil, err
	}
	return &Keygen{s: s, role: role}, nil
}

// First is the engine's first message.
func (k *Keygen) First() ([]byte, error) {
	if k.role != RoleEngine {
		return nil, errors.New("ecdsa2p: First is the engine's step")
	}
	return k.s.next()
}

// Last takes the agent's reply and returns the engine's last message and
// the engine's key.
func (k *Keygen) Last(agentMessage []byte) ([]byte, *Key, error) {
	if k.role != RoleEngine {
		return nil, nil, errors.New("ecdsa2p: Last is the engine's step")
	}
	if err := k.s.deliver(agentMessage); err != nil {
		return nil, nil, err
	}
	m, err := k.s.next()
	if err != nil {
		return nil, nil, err
	}
	blob, err := k.s.wait()
	if err != nil {
		return nil, nil, err
	}
	key, err := keyFromBlob(k.role, blob)
	if err != nil {
		return nil, nil, err
	}
	return m, key, nil
}

// Reply takes the engine's first message and returns the agent's reply.
func (k *Keygen) Reply(engineMessage []byte) ([]byte, error) {
	if k.role != RoleAgent {
		return nil, errors.New("ecdsa2p: Reply is the agent's step")
	}
	if err := k.s.deliver(engineMessage); err != nil {
		return nil, err
	}
	return k.s.next()
}

// Finish takes the engine's last message and returns the agent's key.
func (k *Keygen) Finish(engineMessage []byte) (*Key, error) {
	if k.role != RoleAgent {
		return nil, errors.New("ecdsa2p: Finish is the agent's step")
	}
	if err := k.s.deliver(engineMessage); err != nil {
		return nil, err
	}
	blob, err := k.s.wait()
	if err != nil {
		return nil, err
	}
	return keyFromBlob(k.role, blob)
}

// Abandon stops a run that will not complete.
func (k *Keygen) Abandon() { k.s.abandon() }

// ---- presigning: three messages, the engine first ----

// NewSIDHalf is one party's half of a presigning session id: 16 random
// bytes. The agent sends its half with its request, the engine returns its
// own with its first message, and both run under JointSID of the two.
func NewSIDHalf() ([]byte, error) {
	b := make([]byte, SIDHalfSize)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("ecdsa2p: %w", err)
	}
	return b, nil
}

// JointSID is the session id both parties derive: SHA-256 of the agent's
// half then the engine's.
func JointSID(agentHalf, engineHalf []byte) ([]byte, error) {
	if len(agentHalf) != SIDHalfSize || len(engineHalf) != SIDHalfSize {
		return nil, fmt.Errorf("ecdsa2p: each half of a session id is %d bytes", SIDHalfSize)
	}
	sum := sha256.Sum256(append(append([]byte(nil), agentHalf...), engineHalf...))
	return sum[:], nil
}

// Presign is one presigning run for a batch of nonces under a joint session
// id, stepped like Keygen. It ends in a Presig on each side.
type Presign struct {
	s    *session
	role Role
	n    int
}

// NewPresign starts a run for n nonces (1 to MaxPresign) under the session id.
func (k *Key) NewPresign(n int, sid []byte) (*Presign, error) {
	if n < 1 || n > MaxPresign {
		return nil, fmt.Errorf("ecdsa2p: a presigning run makes 1 to %d nonces, not %d", MaxPresign, n)
	}
	if len(sid) != sha256.Size {
		return nil, errors.New("ecdsa2p: the session id is JointSID's 32 bytes")
	}
	blob, sid := k.blob, append([]byte(nil), sid...)
	s, err := newSession(k.role, func(ctx context.Context, job mpc.Job2P) ([]byte, error) {
		_, presig, err := mpc.ECDSA2PPresign(ctx, job, blob, n, sid)
		return presig, err
	})
	if err != nil {
		return nil, err
	}
	return &Presign{s: s, role: k.role, n: n}, nil
}

// First is the engine's first message.
func (p *Presign) First() ([]byte, error) {
	if p.role != RoleEngine {
		return nil, errors.New("ecdsa2p: First is the engine's step")
	}
	return p.s.next()
}

// Last takes the agent's reply and returns the engine's last message and
// the engine's presig.
func (p *Presign) Last(agentMessage []byte) ([]byte, *Presig, error) {
	if p.role != RoleEngine {
		return nil, nil, errors.New("ecdsa2p: Last is the engine's step")
	}
	if err := p.s.deliver(agentMessage); err != nil {
		return nil, nil, err
	}
	m, err := p.s.next()
	if err != nil {
		return nil, nil, err
	}
	blob, err := p.s.wait()
	if err != nil {
		return nil, nil, err
	}
	return m, newPresig(blob, p.n), nil
}

// Reply takes the engine's first message and returns the agent's reply.
func (p *Presign) Reply(engineMessage []byte) ([]byte, error) {
	if p.role != RoleAgent {
		return nil, errors.New("ecdsa2p: Reply is the agent's step")
	}
	if err := p.s.deliver(engineMessage); err != nil {
		return nil, err
	}
	return p.s.next()
}

// Finish takes the engine's last message and returns the agent's presig.
func (p *Presign) Finish(engineMessage []byte) (*Presig, error) {
	if p.role != RoleAgent {
		return nil, errors.New("ecdsa2p: Finish is the agent's step")
	}
	if err := p.s.deliver(engineMessage); err != nil {
		return nil, err
	}
	blob, err := p.s.wait()
	if err != nil {
		return nil, err
	}
	return newPresig(blob, p.n), nil
}

// Abandon stops a run that will not complete.
func (p *Presign) Abandon() { p.s.abandon() }

// Presig is one party's half of a batch of presigned nonces: slots 0 to
// Len-1, each usable once. Keep it in memory only; a copy that outlived a
// use could sign twice with one nonce and give the key away.
type Presig struct {
	mu   sync.Mutex
	blob []byte
	used []bool
	left int
}

func newPresig(blob []byte, n int) *Presig {
	return &Presig{blob: blob, used: make([]bool, n), left: n}
}

// Len is the number of slots the batch was made with.
func (p *Presig) Len() int { return len(p.used) }

// Unused is how many slots are still usable.
func (p *Presig) Unused() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.left
}

// take burns a slot: the call that follows is its only use, whatever happens.
func (p *Presig) take(slot int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slot < 0 || slot >= len(p.used) {
		return fmt.Errorf("ecdsa2p: slot %d is not in 0 to %d", slot, len(p.used)-1)
	}
	if p.used[slot] {
		return fmt.Errorf("ecdsa2p: slot %d was used already", slot)
	}
	p.used[slot] = true
	p.left--
	return nil
}

// Complete is the agent's last message for one slot and one 32-byte
// digest: what travels in the proposal. The slot is burned first.
func (k *Key) Complete(p *Presig, slot int, digest []byte) ([]byte, error) {
	if k.role != RoleAgent {
		return nil, errors.New("ecdsa2p: Complete is the agent's step")
	}
	if len(digest) != sha256.Size {
		return nil, errors.New("ecdsa2p: a digest is 32 bytes")
	}
	if err := p.take(slot); err != nil {
		return nil, err
	}
	m, err := mpc.ECDSA2PPresignComplete(k.blob, p.blob, slot, digest)
	if err != nil {
		return nil, fmt.Errorf("ecdsa2p: complete: %w", err)
	}
	return m, nil
}

// Signature is a finished signature: r and s, and the recovery id (0 or 1)
// that recovers the joint key, as EVM venues want it.
type Signature struct {
	R, S       [32]byte
	RecoveryID uint8
}

// Finish is the engine's output step for one slot: it verifies the agent's
// message against the slot and the digest, decrypts, checks the signature
// against the joint key and returns it with its recovery id. The slot is
// burned first; a failure leaves no signature.
func (k *Key) Finish(p *Presig, slot int, digest, agentMessage []byte) (Signature, error) {
	if k.role != RoleEngine {
		return Signature{}, errors.New("ecdsa2p: Finish is the engine's step")
	}
	if len(digest) != sha256.Size {
		return Signature{}, errors.New("ecdsa2p: a digest is 32 bytes")
	}
	if err := p.take(slot); err != nil {
		return Signature{}, err
	}
	der, err := mpc.ECDSA2PPresignFinish(k.blob, p.blob, slot, digest, agentMessage)
	if err != nil {
		return Signature{}, fmt.Errorf("ecdsa2p: finish: %w", err)
	}
	return k.fromDER(der, digest)
}

// fromDER reads the library's DER signature and finds the recovery id by
// recovering the joint key with it; the library outputs low s already.
func (k *Key) fromDER(der, digest []byte) (Signature, error) {
	sig, err := ecdsa.ParseDERSignature(der)
	if err != nil {
		return Signature{}, fmt.Errorf("ecdsa2p: signature: %w", err)
	}
	r, s := sig.R(), sig.S()
	if s.IsOverHalfOrder() {
		return Signature{}, errors.New("ecdsa2p: the signature has a high s")
	}
	out := Signature{R: r.Bytes(), S: s.Bytes()}
	want := k.PublicKey()
	for v := uint8(0); v < 2; v++ {
		compact := make([]byte, 0, 65)
		compact = append(compact, 27+v)
		compact = append(compact, out.R[:]...)
		compact = append(compact, out.S[:]...)
		pub, _, err := ecdsa.RecoverCompact(compact, digest)
		if err == nil && hex.EncodeToString(pub.SerializeCompressed()) == hex.EncodeToString(want) {
			out.RecoveryID = v
			return out, nil
		}
	}
	return Signature{}, errors.New("ecdsa2p: the signature does not recover the key")
}

// Verify checks a signature over the digest against the joint key.
func (k *Key) Verify(digest []byte, sig Signature) bool {
	var r, s secp256k1.ModNScalar
	if r.SetBytes(&sig.R) != 0 || s.SetBytes(&sig.S) != 0 {
		return false
	}
	return ecdsa.NewSignature(&r, &s).Verify(digest, k.pub)
}
