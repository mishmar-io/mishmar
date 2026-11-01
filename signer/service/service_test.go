package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/ecdsa2p"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/signerapi"
	"github.com/mishmar-io/mishmar/venues/binance"
	"github.com/mishmar-io/mishmar/venues/hyperliquid"
)

type harness struct {
	t      *testing.T
	svc    *Service
	client *signerapi.ClientWithResponses
	engine ed25519.PrivateKey
	now    time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	backend, err := OpenFile(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(backend, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	svc, err := New(Options{Store: store, EnginePublicKey: pub, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	client, err := signerapi.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, svc: svc, client: client, engine: priv, now: now}
}

// decision is an allow decision for the proposal, signed by the engine's key.
func (h *harness) decision(p api.Proposal) *signerapi.Decision {
	return h.signedDecision(api.VerdictAllow, p)
}

func (h *harness) signedDecision(v api.Verdict, p api.Proposal) *signerapi.Decision {
	record, err := json.Marshal(map[string]any{"id": "dec_1", "verdict": v, "proposal": p})
	if err != nil {
		h.t.Fatal(err)
	}
	return &signerapi.Decision{Record: b64(record), Signature: b64(ed25519.Sign(h.engine, record))}
}

// approval is the engine's signed statement about a decision, in the shape
// the engine writes it.
func (h *harness) approval(decisionID, status string, key ed25519.PrivateKey) *signerapi.ApprovalProof {
	record, err := json.Marshal(map[string]any{"kind": "approval", "decisionId": decisionID, "approvalId": "apr_1", "status": status, "decidedBy": "ishai", "decidedAt": h.now})
	if err != nil {
		h.t.Fatal(err)
	}
	return &signerapi.ApprovalProof{Record: b64(record), Signature: b64(ed25519.Sign(key, record))}
}

// A decision that waited for a person keeps its verdict, so what lets it
// through is the engine's signed statement that this decision was approved.
func TestDecisionsThatWaitedForAPerson(t *testing.T) {
	h := newHarness(t)
	p := api.Proposal{Kind: api.ProposalKindOrder, Venue: api.VenueHyperliquid, Symbol: "BTC", Size: "0.001", ClientOrderId: "msh-a"}
	_, stranger, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	notApproval := h.approval("dec_1", "approved", h.engine)
	raw, _ := json.Marshal(map[string]any{"kind": "decision", "decisionId": "dec_1", "status": "approved"})
	notApproval.Record, notApproval.Signature = b64(raw), b64(ed25519.Sign(h.engine, raw))

	for _, c := range []struct {
		name  string
		proof *signerapi.ApprovalProof
		want  string // the refusal, "" when it passes
	}{
		{"no proof", nil, "carries none"},
		{"rejected", h.approval("dec_1", "rejected", h.engine), "not an approval"},
		{"another decision", h.approval("dec_2", "approved", h.engine), "another decision"},
		{"not the engine's", h.approval("dec_1", "approved", stranger), "does not verify"},
		{"some other record the engine signed", notApproval, "not an approval"},
		{"approved", h.approval("dec_1", "approved", h.engine), ""},
	} {
		d := h.signedDecision(api.VerdictRequireApproval, p)
		d.Approval = c.proof
		rec, err := h.svc.decision(d)
		if c.want == "" {
			if err != nil || rec == nil || rec.Proposal.ClientOrderId != "msh-a" {
				t.Fatalf("%s: %v %+v", c.name, err, rec)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: got %v, want a refusal with %q", c.name, err, c.want)
		}
	}

	// A proof does nothing for a decision that was blocked, and an allowed one needs none.
	blocked := h.signedDecision(api.VerdictBlock, p)
	blocked.Approval = h.approval("dec_1", "approved", h.engine)
	if _, err := h.svc.decision(blocked); err == nil {
		t.Fatal("a blocked decision passed on an approval")
	}
	if _, err := h.svc.decision(h.decision(p)); err != nil {
		t.Fatalf("an allowed decision: %v", err)
	}
}

func decode(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// keygen runs key generation against the service as the agent's signer would.
func (h *harness) keygen(keyID, session string) *ecdsa2p.Key {
	h.t.Helper()
	ctx := context.Background()
	agent, err := ecdsa2p.NewKeygen(ecdsa2p.RoleAgent)
	if err != nil {
		h.t.Fatal(err)
	}
	r1, err := h.client.KeygenStepWithResponse(ctx, keyID, signerapi.KeygenStep{Step: signerapi.KeygenStepStepN1, SessionId: session})
	if err != nil || r1.JSON200 == nil {
		h.t.Fatalf("keygen step 1: %v %s", err, r1.Body)
	}
	m2, err := agent.Reply(decode(h.t, r1.JSON200.Message))
	if err != nil {
		h.t.Fatal(err)
	}
	r2, err := h.client.KeygenStepWithResponse(ctx, keyID, signerapi.KeygenStep{Step: signerapi.KeygenStepStepN2, SessionId: session, AgentMessage: ptr(b64(m2))})
	if err != nil || r2.JSON200 == nil {
		h.t.Fatalf("keygen step 2: %v %s", err, r2.Body)
	}
	key, err := agent.Finish(decode(h.t, r2.JSON200.Message))
	if err != nil {
		h.t.Fatal(err)
	}
	if r2.JSON200.Address == nil || *r2.JSON200.Address != key.Address() || r2.JSON200.PublicKey == nil || *r2.JSON200.PublicKey != b64(key.PublicKey()) {
		h.t.Fatalf("the service's key is not the agent's: %+v vs %s", r2.JSON200, key.Address())
	}
	return key
}

// presign runs presigning for n nonces and returns the agent's presig and
// the service's slot ids.
func (h *harness) presign(keyID, session string, key *ecdsa2p.Key, n int) (*ecdsa2p.Presig, []signerapi.Slot) {
	h.t.Helper()
	ctx := context.Background()
	ah, _ := ecdsa2p.NewSIDHalf()
	eh, _ := ecdsa2p.NewSIDHalf()
	sid, _ := ecdsa2p.JointSID(ah, eh)
	r1, err := h.client.PresignStepWithResponse(ctx, keyID, signerapi.PresignStep{Step: signerapi.PresignStepStepN1, SessionId: session, Count: &n, Sid: ptr(b64(sid))})
	if err != nil || r1.JSON200 == nil {
		h.t.Fatalf("presign step 1: %v %s", err, r1.Body)
	}
	run, err := key.NewPresign(n, sid)
	if err != nil {
		h.t.Fatal(err)
	}
	m2, err := run.Reply(decode(h.t, r1.JSON200.Message))
	if err != nil {
		h.t.Fatal(err)
	}
	r2, err := h.client.PresignStepWithResponse(ctx, keyID, signerapi.PresignStep{Step: signerapi.PresignStepStepN2, SessionId: session, AgentMessage: ptr(b64(m2))})
	if err != nil || r2.JSON200 == nil {
		h.t.Fatalf("presign step 2: %v %s", err, r2.Body)
	}
	presig, err := run.Finish(decode(h.t, r2.JSON200.Message))
	if err != nil {
		h.t.Fatal(err)
	}
	if r2.JSON200.Slots == nil || len(*r2.JSON200.Slots) != n {
		h.t.Fatalf("slots: %+v", r2.JSON200.Slots)
	}
	return presig, *r2.JSON200.Slots
}

var btc = hyperliquid.Rules{AssetIndex: 0, SizeDecimals: 5}

// hlOrder builds a limit order message as the agent's signer would, with the
// proposal the engine's decision would carry for it.
func (h *harness) hlOrder(clientOrderID string) (message []byte, digest []byte, proposal api.Proposal) {
	h.t.Helper()
	in := hyperliquid.Intent{Symbol: "BTC", Side: "buy", OrderType: "limit", Size: "0.0015", Price: "65000.5"}
	nonce := hyperliquid.Nonce(h.now)
	msg, err := hyperliquid.BuildOrder(in, btc, clientOrderID, nonce)
	if err != nil {
		h.t.Fatal(err)
	}
	_, action, err := hyperliquid.Decode(msg)
	if err != nil {
		h.t.Fatal(err)
	}
	d, err := hyperliquid.Digest(action, nonce, hyperliquid.Testnet)
	if err != nil {
		h.t.Fatal(err)
	}
	price := "65000.5"
	return msg, d, api.Proposal{
		Kind: api.ProposalKindOrder, Venue: api.VenueHyperliquid, Symbol: "BTC", Side: api.SideBuy, OrderType: api.ProposalOrderTypeLimit,
		Size: "0.0015", Price: &price, ClientOrderId: clientOrderID, NotionalUsd: "97.5", ExpiresAt: h.now.Add(time.Minute),
	}
}

func TestKeygenPresignFinish(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	key := h.keygen("vk_hl", "ks_1")

	// The service knows the key, and will not make it twice.
	got, err := h.client.GetKeyWithResponse(ctx, "vk_hl")
	if err != nil || got.JSON200 == nil || got.JSON200.Scheme != signerapi.SchemeEcdsa2p || got.JSON200.Address == nil || *got.JSON200.Address != key.Address() {
		t.Fatalf("get key: %v %s", err, got.Body)
	}
	dup, _ := h.client.KeygenStepWithResponse(ctx, "vk_hl", signerapi.KeygenStep{Step: signerapi.KeygenStepStepN1, SessionId: "ks_2"})
	if dup.StatusCode() != http.StatusConflict {
		t.Fatalf("a second key generation answered %d", dup.StatusCode())
	}

	presig, slots := h.presign("vk_hl", "ps_1", key, 4)
	if k, _ := h.client.GetKeyWithResponse(ctx, "vk_hl"); k.JSON200.UnusedNonces == nil || *k.JSON200.UnusedNonces != 4 {
		t.Fatalf("unused nonces %+v", k.JSON200.UnusedNonces)
	}

	// An order, signed single-hop: the agent's last message and the
	// engine's decision, and the service finishes.
	id := hyperliquid.ClientOrderID("agt_test")
	msg, digest, proposal := h.hlOrder(id)
	m4, err := key.Complete(presig, 0, digest)
	if err != nil {
		t.Fatal(err)
	}
	network := "testnet"
	fin := func(slot int, m4 []byte, d *signerapi.Decision) *signerapi.FinishResponse {
		r, err := h.client.FinishWithResponse(ctx, "vk_hl", signerapi.FinishRequest{
			SlotId: slots[slot].Id, AgentMessage: b64(m4), Decision: d,
			VenueAction: signerapi.VenueAction{Venue: signerapi.VenueActionVenueHyperliquid, Network: &network, Action: b64(msg)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := fin(0, m4, h.decision(proposal))
	if r.JSON200 == nil {
		t.Fatalf("finish: %s", r.Body)
	}
	sig := hyperliquid.Signature{R: r.JSON200.Signature.R, S: r.JSON200.Signature.S, V: uint8(r.JSON200.Signature.V)}
	addr, err := hyperliquid.RecoverAddress(digest, sig)
	if err != nil || addr != key.Address() {
		t.Fatalf("the signature recovers %s, want %s (%v)", addr, key.Address(), err)
	}
	// The slot is spent.
	if again := fin(0, m4, h.decision(proposal)); again.JSONDefault == nil || again.JSONDefault.Code != signerapi.ErrorCodeConflict {
		t.Fatalf("a spent slot answered %s", again.Body)
	}
	// The decision must be the engine's, allow, and for this order.
	m4b, _ := key.Complete(presig, 1, digest)
	other := proposal
	other.ClientOrderId = hyperliquid.ClientOrderID("agt_test")
	if r := fin(1, m4b, h.decision(other)); r.JSONDefault == nil || r.JSONDefault.Code != signerapi.ErrorCodeRefused {
		t.Fatalf("another order's decision passed: %s", r.Body)
	}
	m4c, _ := key.Complete(presig, 2, digest)
	if r := fin(2, m4c, h.signedDecision(api.VerdictBlock, proposal)); r.JSONDefault == nil || r.JSONDefault.Code != signerapi.ErrorCodeRefused {
		t.Fatalf("a blocked decision passed: %s", r.Body)
	}
	m4d, _ := key.Complete(presig, 3, digest)
	forged := h.decision(proposal)
	forged.Signature = b64(make([]byte, ed25519.SignatureSize))
	if r := fin(3, m4d, forged); r.JSONDefault == nil || r.JSONDefault.Code != signerapi.ErrorCodeRefused {
		t.Fatalf("a forged decision passed: %s", r.Body)
	}
	if k, _ := h.client.GetKeyWithResponse(ctx, "vk_hl"); *k.JSON200.UnusedNonces != 0 {
		t.Fatalf("%d nonces left; every attempt burns its slot", *k.JSON200.UnusedNonces)
	}

	// Forgotten: gone from the store and the API.
	if r, _ := h.client.ForgetKeyWithResponse(ctx, "vk_hl"); r.StatusCode() != http.StatusNoContent {
		t.Fatalf("forget: %d", r.StatusCode())
	}
	if r, _ := h.client.GetKeyWithResponse(ctx, "vk_hl"); r.StatusCode() != http.StatusNotFound {
		t.Fatalf("a forgotten key answered %d", r.StatusCode())
	}
}

func TestFrost(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	agentShare, engineShare, err := frost.Deal()
	if err != nil {
		t.Fatal(err)
	}
	engineJSON, _ := json.Marshal(engineShare)
	agentJSON, _ := json.Marshal(agentShare)
	imp, err := h.client.ImportKeyWithResponse(ctx, "vk_bn", signerapi.KeyImport{Scheme: signerapi.SchemeFrostEd25519, Share: string(engineJSON)})
	if err != nil || imp.JSON201 == nil || imp.JSON201.PublicKey != b64(engineShare.PublicKey()) {
		t.Fatalf("import: %v %s", err, imp.Body)
	}
	if r, _ := h.client.ImportKeyWithResponse(ctx, "vk_bn", signerapi.KeyImport{Scheme: signerapi.SchemeFrostEd25519, Share: string(engineJSON)}); r.StatusCode() != http.StatusConflict {
		t.Fatalf("a second import answered %d", r.StatusCode())
	}
	if r, _ := h.client.ImportKeyWithResponse(ctx, "vk_bad", signerapi.KeyImport{Scheme: signerapi.SchemeFrostEd25519, Share: string(agentJSON)}); r.StatusCode() != http.StatusUnprocessableEntity {
		t.Fatalf("the agent's share was imported: %d", r.StatusCode())
	}
	cs, err := h.client.IssueCommitmentsWithResponse(ctx, "vk_bn", signerapi.IssueCommitmentsJSONRequestBody{Count: 2})
	if err != nil || cs.JSON200 == nil || len(*cs.JSON200) != 2 {
		t.Fatalf("commitments: %v %s", err, cs.Body)
	}
	commitments := *cs.JSON200

	// An order the agent built and half-signed.
	id := binance.ClientOrderID("agt_test")
	message, err := binance.BuildOrder(binance.Intent{Symbol: "BTCUSDT", Side: "buy", OrderType: "market", Size: "0.001"}, binance.Rules{Step: "0.001"}, id, h.now)
	if err != nil {
		t.Fatal(err)
	}
	party, _ := agentShare.Party()
	ac := party.Commit()
	ec, err := frost.ParseCommitment(decode(t, commitments[0].Commitment))
	if err != nil {
		t.Fatal(err)
	}
	as, err := party.Sign(message, ec, ac)
	if err != nil {
		t.Fatal(err)
	}
	proposal := api.Proposal{Kind: api.ProposalKindOrder, Venue: api.VenueBinance, Symbol: "BTCUSDT", Side: api.SideBuy, OrderType: api.ProposalOrderTypeMarket, Size: "0.001", ClientOrderId: id, NotionalUsd: "80", ExpiresAt: h.now.Add(time.Minute)}
	r, err := h.client.FrostSignWithResponse(ctx, "vk_bn", signerapi.FrostSignRequest{
		CommitmentId: commitments[0].Id, AgentCommitment: b64(ac.Bytes()), AgentShare: b64(as.Bytes()), Decision: h.decision(proposal),
		VenueAction: signerapi.VenueAction{Venue: signerapi.VenueActionVenueBinance, Action: b64(message)},
	})
	if err != nil || r.JSON200 == nil {
		t.Fatalf("frost sign: %v %s", err, r.Body)
	}
	if !ed25519.Verify(engineShare.PublicKey(), message, decode(t, r.JSON200.Signature)) {
		t.Fatal("the signature does not verify")
	}
	// A read for verify needs no decision; an order without one is refused.
	read := []byte("recvWindow=5000&timestamp=1761825600000")
	party2, _ := agentShare.Party()
	ac2 := party2.Commit()
	ec2, _ := frost.ParseCommitment(decode(t, commitments[1].Commitment))
	as2, _ := party2.Sign(read, ec2, ac2)
	verify := signerapi.FrostSignRequestPurposeVerify
	r2, err := h.client.FrostSignWithResponse(ctx, "vk_bn", signerapi.FrostSignRequest{
		CommitmentId: commitments[1].Id, AgentCommitment: b64(ac2.Bytes()), AgentShare: b64(as2.Bytes()), Purpose: &verify,
		VenueAction: signerapi.VenueAction{Venue: signerapi.VenueActionVenueBinance, Action: b64(read)},
	})
	if err != nil || r2.JSON200 == nil {
		t.Fatalf("verify sign: %v %s", err, r2.Body)
	}
	cs2, _ := h.client.IssueCommitmentsWithResponse(ctx, "vk_bn", signerapi.IssueCommitmentsJSONRequestBody{Count: 1})
	r3, _ := h.client.FrostSignWithResponse(ctx, "vk_bn", signerapi.FrostSignRequest{
		CommitmentId: (*cs2.JSON200)[0].Id, AgentCommitment: b64(ac.Bytes()), AgentShare: b64(as.Bytes()),
		VenueAction: signerapi.VenueAction{Venue: signerapi.VenueActionVenueBinance, Action: b64(message)},
	})
	if r3.JSONDefault == nil || r3.JSONDefault.Code != signerapi.ErrorCodeRefused {
		t.Fatalf("an order without a decision passed: %s", r3.Body)
	}
	hl, _ := h.client.HealthWithResponse(ctx)
	if hl.JSON200 == nil || hl.JSON200.Keys != 1 || hl.JSON200.Instance != h.svc.Instance() {
		t.Fatalf("health %s", hl.Body)
	}
}
