package frost

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bytemare/dkg"
	"github.com/bytemare/ecc"
	bfrost "github.com/bytemare/frost"
	"github.com/bytemare/secret-sharing/keys"
)

// Two-party key generation: FROST's distributed key generation (Pedersen
// with proofs of knowledge, bytemare/dkg), so the whole key never exists on
// any machine; Deal is the older way, for an owner who makes the key on
// their own machine. The engine's signer opens (First), the agent's signer
// answers with its own commitment and its share for the engine (Reply), the
// engine's signer closes with its share for the agent and keeps its half
// (Last), and the agent's keeps its half (Finish). That is two rounds over
// the agent API, the engine relaying, the same shape as two-party ECDSA.
// The round-2 messages carry a secret share for the other party: they go
// over the authenticated channel the agent API is, never logged.

const dkgSuite = dkg.Edwards25519Sha512

// Keygen is one party's side of a key generation; one per key.
type Keygen struct {
	id   uint16
	p    *dkg.Participant
	own  *dkg.Round1Data
	peer *dkg.Round1Data
	done bool
}

// NewKeygen starts one party's side: AgentID or EngineID.
func NewKeygen(id uint16) (*Keygen, error) {
	if id != AgentID && id != EngineID {
		return nil, fmt.Errorf("frost: keygen: party %d is neither the agent (%d) nor the engine (%d)", id, AgentID, EngineID)
	}
	if !suite.Available() || !dkgSuite.Available() {
		return nil, errors.New("frost: the Ed25519 ciphersuite is not available in this build")
	}
	p, err := dkgSuite.NewParticipant(id, 2, 2)
	if err != nil {
		return nil, fmt.Errorf("frost: keygen: %w", err)
	}
	return &Keygen{id: id, p: p}, nil
}

// ID is the party this side generates for.
func (k *Keygen) ID() uint16 { return k.id }

// First is the engine's opening message: its commitment to its polynomial
// with the proof of knowledge of its secret.
func (k *Keygen) First() ([]byte, error) {
	lib.Lock()
	defer lib.Unlock()
	if k.id != EngineID {
		return nil, errors.New("frost: keygen: First is the engine's; the agent answers with Reply")
	}
	if k.own != nil {
		return nil, errors.New("frost: keygen: First was called already")
	}
	r1, err := k.p.Start()
	if err != nil {
		return nil, fmt.Errorf("frost: keygen: %w", err)
	}
	k.own = r1
	return r1.Encode(), nil
}

// keygenReply is the agent's answer on the wire: its own commitment with the
// proof, and its secret share for the engine.
type keygenReply struct {
	Round1 string `json:"round1"`
	Round2 string `json:"round2"`
}

// Reply is the agent's answer to the engine's First: the agent's own
// commitment with its proof of knowledge, and its secret share for the
// engine. It checks the engine's proof first.
func (k *Keygen) Reply(first []byte) ([]byte, error) {
	lib.Lock()
	defer lib.Unlock()
	if k.id != AgentID {
		return nil, errors.New("frost: keygen: Reply is the agent's; the engine opens with First")
	}
	if k.own != nil {
		return nil, errors.New("frost: keygen: Reply was called already")
	}
	peer, err := decodeRound1(first, EngineID)
	if err != nil {
		return nil, err
	}
	r1, err := k.p.Start()
	if err != nil {
		return nil, fmt.Errorf("frost: keygen: %w", err)
	}
	r2, err := k.p.Continue([]*dkg.Round1Data{peer})
	if err != nil {
		return nil, fmt.Errorf("frost: keygen: the engine's opening message does not verify: %w", err)
	}
	forEngine, ok := r2[EngineID]
	if !ok || forEngine == nil {
		return nil, errors.New("frost: keygen: no share for the engine was made")
	}
	k.own, k.peer = r1, peer
	out, err := json.Marshal(keygenReply{
		Round1: base64.StdEncoding.EncodeToString(r1.Encode()),
		Round2: base64.StdEncoding.EncodeToString(forEngine.Encode()),
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Last is the engine's closing message, its secret share for the agent,
// and the engine's share of the key. It checks the agent's proof and the
// share the agent sent against the agent's commitment.
func (k *Keygen) Last(reply []byte) ([]byte, *Share, error) {
	lib.Lock()
	defer lib.Unlock()
	if k.id != EngineID {
		return nil, nil, errors.New("frost: keygen: Last is the engine's; the agent ends with Finish")
	}
	if k.own == nil || k.done {
		return nil, nil, errors.New("frost: keygen: Last needs First before it, once")
	}
	var r keygenReply
	if err := json.Unmarshal(reply, &r); err != nil {
		return nil, nil, fmt.Errorf("frost: keygen: the agent's answer is not the expected JSON: %w", err)
	}
	raw1, err := base64.StdEncoding.DecodeString(r.Round1)
	if err != nil {
		return nil, nil, errors.New("frost: keygen: the agent's answer: round1 is not base64")
	}
	raw2, err := base64.StdEncoding.DecodeString(r.Round2)
	if err != nil {
		return nil, nil, errors.New("frost: keygen: the agent's answer: round2 is not base64")
	}
	peer, err := decodeRound1(raw1, AgentID)
	if err != nil {
		return nil, nil, err
	}
	peerShare, err := decodeRound2(raw2, AgentID, EngineID)
	if err != nil {
		return nil, nil, err
	}
	r2, err := k.p.Continue([]*dkg.Round1Data{peer})
	if err != nil {
		return nil, nil, fmt.Errorf("frost: keygen: the agent's commitment does not verify: %w", err)
	}
	forAgent, ok := r2[AgentID]
	if !ok || forAgent == nil {
		return nil, nil, errors.New("frost: keygen: no share for the agent was made")
	}
	ks, err := k.p.Finalize([]*dkg.Round1Data{peer}, []*dkg.Round2Data{peerShare})
	if err != nil {
		return nil, nil, fmt.Errorf("frost: keygen: the agent's share does not match its commitment: %w", err)
	}
	k.peer = peer
	share, err := shareFromKeygen(k.id, ks, k.own, peer)
	if err != nil {
		return nil, nil, err
	}
	k.done = true
	return forAgent.Encode(), share, nil
}

// Finish is the agent's end: it checks the engine's share against the
// engine's commitment and keeps the agent's share of the key.
func (k *Keygen) Finish(last []byte) (*Share, error) {
	lib.Lock()
	defer lib.Unlock()
	if k.id != AgentID {
		return nil, errors.New("frost: keygen: Finish is the agent's; the engine ends with Last")
	}
	if k.own == nil || k.peer == nil || k.done {
		return nil, errors.New("frost: keygen: Finish needs Reply before it, once")
	}
	peerShare, err := decodeRound2(last, EngineID, AgentID)
	if err != nil {
		return nil, err
	}
	ks, err := k.p.Finalize([]*dkg.Round1Data{k.peer}, []*dkg.Round2Data{peerShare})
	if err != nil {
		return nil, fmt.Errorf("frost: keygen: the engine's share does not match its commitment: %w", err)
	}
	share, err := shareFromKeygen(k.id, ks, k.own, k.peer)
	if err != nil {
		return nil, err
	}
	k.done = true
	return share, nil
}

func decodeRound1(b []byte, from uint16) (*dkg.Round1Data, error) {
	d := &dkg.Round1Data{}
	if err := d.Decode(b); err != nil {
		return nil, fmt.Errorf("frost: keygen: the other party's commitment message: %w", err)
	}
	if d.SenderIdentifier != from {
		return nil, fmt.Errorf("frost: keygen: the commitment message is from party %d, not %d", d.SenderIdentifier, from)
	}
	return d, nil
}

func decodeRound2(b []byte, from, to uint16) (*dkg.Round2Data, error) {
	d := &dkg.Round2Data{}
	if err := d.Decode(b); err != nil {
		return nil, fmt.Errorf("frost: keygen: the other party's share message: %w", err)
	}
	if d.SenderIdentifier != from || d.RecipientIdentifier != to {
		return nil, fmt.Errorf("frost: keygen: the share message is from party %d to %d, not from %d to %d", d.SenderIdentifier, d.RecipientIdentifier, from, to)
	}
	return d, nil
}

// shareFromKeygen builds the share with the public side of the group both
// parties need to check each other's signature shares: the group's key and
// each party's public key share, all derived from the two commitments.
func shareFromKeygen(id uint16, ks *keys.KeyShare, own, peer *dkg.Round1Data) (*Share, error) {
	g := suite.Group()
	r1 := []*dkg.Round1Data{own, peer}
	vk, err := dkg.VerificationKeyFromRound1(dkgSuite, r1)
	if err != nil {
		return nil, fmt.Errorf("frost: keygen: %w", err)
	}
	if len(own.Commitment) != len(peer.Commitment) {
		return nil, errors.New("frost: keygen: the two commitments differ in length")
	}
	aggregate := make([]*ecc.Element, len(own.Commitment))
	for i := range own.Commitment {
		aggregate[i] = g.NewElement().Add(own.Commitment[i]).Add(peer.Commitment[i])
	}
	commitments := [][]*ecc.Element{own.Commitment, peer.Commitment}
	pks := make([]*keys.PublicKeyShare, 0, 2)
	for _, pid := range []uint16{AgentID, EngineID} {
		pub, err := dkg.ComputeParticipantPublicKey(dkgSuite, pid, commitments)
		if err != nil {
			return nil, fmt.Errorf("frost: keygen: party %d's public key: %w", pid, err)
		}
		pk, err := keys.NewPublicKeyShare(g, pid, pub, aggregate)
		if err != nil {
			return nil, fmt.Errorf("frost: keygen: party %d's public key share: %w", pid, err)
		}
		pks = append(pks, pk)
	}
	config := &bfrost.Configuration{
		Ciphersuite:           suite,
		Threshold:             2,
		MaxSigners:            2,
		VerificationKey:       vk,
		SignerPublicKeyShares: pks,
	}
	if err := config.Init(); err != nil {
		return nil, fmt.Errorf("frost: keygen: %w", err)
	}
	if err := config.ValidateKeyShare(ks); err != nil {
		return nil, fmt.Errorf("frost: keygen: the share does not belong to the group: %w", err)
	}
	return &Share{id: id, keyShare: ks, config: config}, nil
}

// Abandon drops a key generation that will not finish.
func (k *Keygen) Abandon() {
	k.own, k.peer, k.done = nil, nil, true
}
