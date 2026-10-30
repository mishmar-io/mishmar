package signer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/ecdsa2p"
)

// Two-party ECDSA (Hyperliquid API wallets): the agent's key is made by key
// generation with the engine (RunKeygen), and signing is single-hop through
// presigned slots the signer takes from the engine ahead of time (two rounds,
// like a batch of FROST commitments) and spends one per order, in the
// proposal's venue message or in a channel answer. A slot signs once and is
// kept in memory only.

// runECDSAKeygen runs the agent's side of two-party key generation for a venue
// key the engine created without a share: two rounds over the agent API,
// the engine relaying to its own signer. The key comes back with its
// address, which the engine recorded on the venue key.
func runECDSAKeygen(ctx context.Context, c *Client, venueKeyID string) (*ecdsa2p.Key, error) {
	kg, err := ecdsa2p.NewKeygen(ecdsa2p.RoleAgent)
	if err != nil {
		return nil, err
	}
	defer kg.Abandon()
	r1, err := c.KeygenRound(ctx, api.KeygenRound{VenueKeyId: venueKeyID, Round: api.KeygenRoundRoundN1})
	if err != nil {
		return nil, fmt.Errorf("signer: key generation round 1: %w", err)
	}
	m1, err := decodeB64(r1.Message)
	if err != nil {
		return nil, fmt.Errorf("signer: key generation round 1: %w", err)
	}
	m2, err := kg.Reply(m1)
	if err != nil {
		return nil, err
	}
	r2, err := c.KeygenRound(ctx, api.KeygenRound{VenueKeyId: venueKeyID, Round: api.KeygenRoundRoundN2, SessionId: &r1.SessionId, Message: ptr(base64.StdEncoding.EncodeToString(m2))})
	if err != nil {
		return nil, fmt.Errorf("signer: key generation round 2: %w", err)
	}
	m3, err := decodeB64(r2.Message)
	if err != nil {
		return nil, fmt.Errorf("signer: key generation round 2: %w", err)
	}
	key, err := kg.Finish(m3)
	if err != nil {
		return nil, err
	}
	if r2.PublicKey != nil {
		pub, err := decodeB64(*r2.PublicKey)
		if err != nil || string(pub) != string(key.PublicKey()) {
			return nil, errors.New("signer: key generation: the engine's public key is not the one this signer derived")
		}
	}
	if r2.Address != nil && *r2.Address != key.Address() {
		return nil, fmt.Errorf("signer: key generation: the engine recorded address %s, this signer derived %s", *r2.Address, key.Address())
	}
	return key, nil
}

// presigBatch is one presigning run's slots, with the ids the engine gave
// them, spent in order.
type presigBatch struct {
	presig *ecdsa2p.Presig
	slots  []api.EngineCommitment
	next   int
}

// ecdsaKey parses the agent's ECDSA key of a venue key once and keeps it.
func (s *Signer) ecdsaKey(venueKeyID string) (*ecdsa2p.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.ecdsaKeys[venueKeyID]; ok {
		return k, nil
	}
	vk, ok := s.Config.VenueKeys[venueKeyID]
	if !ok {
		return nil, fmt.Errorf("no key for %s", venueKeyID)
	}
	if vk.Scheme != SchemeECDSA2p {
		return nil, fmt.Errorf("%s is a %s key", venueKeyID, vk.Scheme)
	}
	k, err := ecdsa2p.ParseKey(vk.Share)
	if err != nil {
		return nil, err
	}
	if s.ecdsaKeys == nil {
		s.ecdsaKeys = map[string]*ecdsa2p.Key{}
	}
	s.ecdsaKeys[venueKeyID] = k
	return k, nil
}

// takeSlot pops the next unused, unexpired presigned slot for the venue
// key, presigning a batch with the engine when none is left, and tops up in
// the background when the batch runs low.
func (s *Signer) takeSlot(ctx context.Context, venueKeyID string, now time.Time) (slotID string, presig *ecdsa2p.Presig, index int, err error) {
	st := s.oneShot()
	pop := func() (string, *ecdsa2p.Presig, int, int, bool) {
		st.mu.Lock()
		defer st.mu.Unlock()
		for len(st.presigs[venueKeyID]) > 0 {
			b := st.presigs[venueKeyID][0]
			for b.next < len(b.slots) {
				slot := b.slots[b.next]
				i := b.next
				b.next++
				if now.Add(commitmentMargin).Before(slot.ExpiresAt) {
					return slot.Id, b.presig, i, s.unusedLocked(st, venueKeyID), true
				}
			}
			st.presigs[venueKeyID] = st.presigs[venueKeyID][1:]
		}
		return "", nil, 0, 0, false
	}
	id, p, i, left, ok := pop()
	if !ok {
		if err := s.fetchPresigs(ctx, venueKeyID); err != nil {
			return "", nil, 0, noOneShot("presigning for %s: %v", venueKeyID, err)
		}
		if id, p, i, left, ok = pop(); !ok {
			return "", nil, 0, noOneShot("the engine has no presigned slot to spare for %s", venueKeyID)
		}
	}
	if batch := s.CommitmentBatch; batch > 1 && left < batch/2 {
		s.refillPresigs(venueKeyID)
	}
	return id, p, i, nil
}

// unusedLocked counts the slots still to spend for the key; st.mu is held.
func (s *Signer) unusedLocked(st *oneShotState, venueKeyID string) int {
	n := 0
	for _, b := range st.presigs[venueKeyID] {
		n += len(b.slots) - b.next
	}
	return n
}

// fetchPresigs runs one presigning with the engine for the signer's batch
// (one slot when it keeps none) and adds the slots to the key's pool. A run
// from another engine instance than the cached ones means the engine or its
// signer restarted: what was cached is dropped.
func (s *Signer) fetchPresigs(ctx context.Context, venueKeyID string) error {
	key, err := s.ecdsaKey(venueKeyID)
	if err != nil {
		return err
	}
	count := max(s.CommitmentBatch, 1)
	half, err := ecdsa2p.NewSIDHalf()
	if err != nil {
		return err
	}
	r1, err := s.Client.PresignRound(ctx, api.PresignRound{VenueKeyId: venueKeyID, Round: api.PresignRoundRoundN1, Count: &count, SignerSid: ptr(base64.StdEncoding.EncodeToString(half))})
	if err != nil {
		return err
	}
	if r1.EngineSid == nil || r1.Count == nil || *r1.Count < 1 {
		return errors.New("round 1 answered without the engine's session id half or a count")
	}
	engineHalf, err := decodeB64(*r1.EngineSid)
	if err != nil {
		return err
	}
	sid, err := ecdsa2p.JointSID(half, engineHalf)
	if err != nil {
		return err
	}
	m1, err := decodeB64(r1.Message)
	if err != nil {
		return err
	}
	run, err := key.NewPresign(*r1.Count, sid)
	if err != nil {
		return err
	}
	defer run.Abandon()
	m2, err := run.Reply(m1)
	if err != nil {
		return err
	}
	r2, err := s.Client.PresignRound(ctx, api.PresignRound{VenueKeyId: venueKeyID, Round: api.PresignRoundRoundN2, SessionId: &r1.SessionId, Message: ptr(base64.StdEncoding.EncodeToString(m2))})
	if err != nil {
		return err
	}
	m3, err := decodeB64(r2.Message)
	if err != nil {
		return err
	}
	presig, err := run.Finish(m3)
	if err != nil {
		return err
	}
	if r2.Slots == nil || len(*r2.Slots) != presig.Len() {
		return fmt.Errorf("round 2 named %d slots for %d nonces", lenOf(r2.Slots), presig.Len())
	}
	st := s.oneShot()
	st.mu.Lock()
	if r2.EngineInstance != st.instance {
		if st.instance != "" {
			clear(st.commitments)
			clear(st.presigs)
		}
		st.instance = r2.EngineInstance
	}
	st.presigs[venueKeyID] = append(st.presigs[venueKeyID], &presigBatch{presig: presig, slots: *r2.Slots})
	st.mu.Unlock()
	return nil
}

// refillPresigs presigns another batch in the background, one at a time per key.
func (s *Signer) refillPresigs(venueKeyID string) {
	st := s.oneShot()
	st.mu.Lock()
	if st.refilling[venueKeyID] {
		st.mu.Unlock()
		return
	}
	st.refilling[venueKeyID] = true
	st.mu.Unlock()
	go func() {
		defer func() {
			st.mu.Lock()
			delete(st.refilling, venueKeyID)
			st.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), s.Client.timeout())
		defer cancel()
		if err := s.fetchPresigs(ctx, venueKeyID); err != nil {
			s.Logger.Warn("signer: presigning", "venueKey", venueKeyID, "err", err)
		}
	}()
}

func lenOf[T any](p *[]T) int {
	if p == nil {
		return 0
	}
	return len(*p)
}

func decodeB64(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("not base64")
	}
	return raw, nil
}
