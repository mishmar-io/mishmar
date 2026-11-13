package decision

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
)

var update = flag.Bool("update", false, "rewrite spec/vectors/decision-signing.json from this test's cases")

const vectorPath = "../spec/vectors/decision-signing.json"

var t0 = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// vectorSeed is the fixed key the vectors are signed with, 0x00..0x1f: the
// proposal vectors' key, here in the engine's role.
func vectorSeed() []byte {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	return seed
}

// order is an allowed limit order on Hyperliquid as the engine serves it,
// signed as version 3, with what is served but not signed set too (reason,
// latency, the venue's answer).
func order() api.Decision {
	return api.Decision{
		Id: "dec_1", CreatedAt: t0.Add(250 * time.Millisecond), AgentId: "agt_1", AgentName: "Trend follower",
		ConnectedAccountId: "ta_hl", Verdict: api.VerdictAllow,
		Proposal: api.Proposal{
			Kind: api.ProposalKindOrder, Venue: api.VenueHyperliquid, Symbol: "BTC", Side: api.SideBuy, Size: "0.0003",
			OrderType: api.ProposalOrderTypeLimit, Price: ptr("50000"), NotionalUsd: "15", ExpiresAt: t0.Add(time.Minute),
			ClientOrderId: "msh-a1b2c3-k3j9xq",
		},
		Evaluations: []api.RuleEvaluation{
			{RuleId: "rl_1", RuleName: "Max order size", Scope: api.Scope{Kind: api.ScopeKindAgent, Id: "agt_1"},
				Metric: api.RuleMetricOrderSize, Observed: "15", Threshold: "150", Unit: api.RuleUnitUsd, Passed: true, Outcome: api.RuleOutcomeBlock},
			{RuleId: "rl_2", RuleName: "Account gross exposure", Scope: api.Scope{Kind: api.ScopeKindConnectedAccount, Id: "ta_hl"},
				Metric: api.RuleMetricGrossExposure, Observed: "35", Threshold: "100", Unit: api.RuleUnitUsd, Passed: true, Outcome: api.RuleOutcomeBlock},
		},
		State: api.StateSnapshot{
			AsOf: t0, DailyPnlUsd: "-1.5", DrawdownPct: "0", GrossExposureUsd: "20", Leverage: "0.04", NetExposureUsd: "20",
		},
		ConfigRevision: 12, AgentSignature: "YWdlbnQ=",
		Submission:     &api.DecisionSubmission{KeyId: "key_a", Nonce: "n1", ProposedAt: t0, Price: ptr("50000")},
		SigningVersion: ptr(api.DecisionSigningVersionN3),
		Reason:         ptr("every rule passed"),
		LatencyMs:      2.1,
		VenueAck:       &api.VenueAck{OrderId: "64462809897", Status: "resting", ReceivedAt: t0.Add(time.Second)},
	}
}

// signedV3 is order()'s record as version 3 signs it, written out by hand
// so a change to the shape shows here first.
const signedV3 = `{"agentId":"agt_1","agentName":"Trend follower","agentSignature":"YWdlbnQ=","configRevision":12,"connectedAccountId":"ta_hl",` +
	`"createdAt":"2026-11-13T10:00:00.25Z",` +
	`"evaluations":[{"metric":"order_size","observed":"15","outcome":"block","passed":true,"ruleId":"rl_1","ruleName":"Max order size","scope":{"id":"agt_1","kind":"agent"},"threshold":"150","unit":"usd"},` +
	`{"metric":"gross_exposure","observed":"35","outcome":"block","passed":true,"ruleId":"rl_2","ruleName":"Account gross exposure","scope":{"id":"ta_hl","kind":"connected_account"},"threshold":"100","unit":"usd"}],` +
	`"id":"dec_1",` +
	`"proposal":{"clientOrderId":"msh-a1b2c3-k3j9xq","expiresAt":"2026-11-13T10:01:00Z","kind":"order","notionalUsd":"15","orderType":"limit","price":"50000","reduceOnly":false,"side":"buy","size":"0.0003","symbol":"BTC","venue":"hyperliquid"},` +
	`"state":{"asOf":"2026-11-13T10:00:00Z","dailyPnlUsd":"-1.5","drawdownPct":"0","grossExposureUsd":"20","leverage":"0.04","netExposureUsd":"20"},` +
	`"submission":{"keyId":"key_a","nonce":"n1","price":"50000","proposedAt":"2026-11-13T10:00:00Z"},` +
	`"verdict":"allow"}`

// signedV2 is the same record as version 2 signs it: portfolioId between
// createdAt and evaluations, and nothing else changed.
var signedV2 = strings.Replace(signedV3, `"id":"dec_1",`, `"id":"dec_1","portfolioId":"pf_main",`, 1)

// signedV1 is version 2's record under the names from before contract
// 0.20.0: the account's id as tradingAccountId (sorted after submission),
// the scope kind as trading_account.
var signedV1 = strings.NewReplacer(
	`"connectedAccountId":"ta_hl",`, ``,
	`"kind":"connected_account"`, `"kind":"trading_account"`,
	`"verdict":"allow"}`, `"tradingAccountId":"ta_hl","verdict":"allow"}`,
).Replace(signedV2)

func cases() []vectorCase {
	v3 := order()
	v2 := order()
	v2.SigningVersion, v2.PortfolioId = ptr(api.DecisionSigningVersionN2), ptr("pf_main")
	v1 := v2
	v1.SigningVersion = nil
	exit := api.Decision{
		Id: "dec_2", CreatedAt: t0.Add(time.Second), AgentId: "agt_1", AgentName: "Reversion",
		ConnectedAccountId: "ta_bn", Verdict: api.VerdictAllow, Exit: ptr(true),
		Proposal: api.Proposal{
			Kind: api.ProposalKindOrder, Venue: api.VenueBinance, Symbol: "BTCUSDT", Side: api.SideSell, Size: "0.004",
			OrderType: api.ProposalOrderTypeMarket, ReduceOnly: true, NotionalUsd: "400.12", ExpiresAt: t0.Add(time.Minute),
			ClientOrderId: "msh-d4e5f6-p2q8rw",
		},
		State:          api.StateSnapshot{AsOf: t0, DailyPnlUsd: "0", DrawdownPct: "0", GrossExposureUsd: "400", Leverage: "0.2", NetExposureUsd: "400"},
		ConfigRevision: 3, AgentSignature: "YWdlbnQy",
		Submission:     &api.DecisionSubmission{KeyId: "key_b", Nonce: "n2", ProposedAt: t0},
		SigningVersion: ptr(api.DecisionSigningVersionN3),
		LatencyMs:      1.4,
	}
	return []vectorCase{
		{Name: "version 3: an allowed limit order; the record has no portfolioId", Decision: v3},
		{Name: "version 2: the same decision, signed with the portfolio the agent was in", Decision: v2},
		{Name: "version 1 (signingVersion absent): the same, signed before contract 0.20.0 under the old names (tradingAccountId, trading_account)", Decision: v1},
		{Name: "version 3: a reduce-only market exit on Binance, no rule evaluated (evaluations signed as []), exit signed", Decision: exit},
	}
}

type vectorCase struct {
	Name         string       `json:"name"`
	Decision     api.Decision `json:"decision"`
	SigningBytes string       `json:"signingBytes"`
}

type vectorFile struct {
	Description string       `json:"description"`
	Secret      string       `json:"secret"`
	PublicKey   string       `json:"publicKey"`
	Cases       []vectorCase `json:"cases"`
}

func TestSigningBytesPerVersion(t *testing.T) {
	c := cases()
	got, err := SigningBytes(c[0].Decision)
	if err != nil || string(got) != signedV3 {
		t.Fatalf("version 3 (%v)\n got %s\nwant %s", err, got, signedV3)
	}
	got, err = SigningBytes(c[1].Decision)
	if err != nil || string(got) != signedV2 {
		t.Fatalf("version 2 (%v)\n got %s\nwant %s", err, got, signedV2)
	}
	got, err = SigningBytes(c[2].Decision)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != signedV1 {
		t.Fatalf("version 1\n got %s\nwant %s", got, signedV1)
	}
	// What is served but not signed never reaches the bytes.
	for _, absent := range []string{"signingVersion", "reason", "latencyMs", "venueAck", "engineSignature"} {
		if bytes.Contains(got, []byte(absent)) {
			t.Fatalf("%s is in the signed bytes: %s", absent, got)
		}
	}
	// A version 3 record signs no portfolio, even one served beside it.
	v3 := c[0].Decision
	v3.PortfolioId = ptr("pf_main")
	if got, _ := SigningBytes(v3); string(got) != signedV3 {
		t.Fatalf("a portfolioId on a version 3 decision was signed: %s", got)
	}
	// A version 2 record without its portfolio was signed with "".
	v2 := c[1].Decision
	v2.PortfolioId = nil
	if got, _ := SigningBytes(v2); !bytes.Contains(got, []byte(`"portfolioId":""`)) {
		t.Fatalf("a version 2 decision without portfolioId: %s", got)
	}
}

func TestUnknownVersionIsRefused(t *testing.T) {
	key := agentkey.FromSeed(vectorSeed())
	d := order()
	msg, _ := SigningBytes(d)
	d.EngineSignature = ptr(key.Sign(msg))
	for _, v := range []api.DecisionSigningVersion{0, 4} {
		d.SigningVersion = ptr(v)
		if _, err := SigningBytes(d); err == nil || !strings.Contains(err.Error(), "not one this client knows") {
			t.Fatalf("version %d: %v", v, err)
		}
		if err := Verify(d, key.PublicKey()); err == nil {
			t.Fatalf("version %d verified", v)
		}
	}
}

func TestVerify(t *testing.T) {
	key := agentkey.FromSeed(vectorSeed())
	for _, c := range cases() {
		d := c.Decision
		msg, err := SigningBytes(d)
		if err != nil {
			t.Fatal(err)
		}
		d.EngineSignature = ptr(key.Sign(msg))
		if err := Verify(d, key.PublicKey()); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		changed := d
		changed.AgentName = "someone else"
		if Verify(changed, key.PublicKey()) == nil {
			t.Fatalf("%s: verifies with another agent name", c.Name)
		}
		// Served but not signed: changing it changes nothing.
		served := d
		served.LatencyMs, served.Reason = 99, ptr("rewritten")
		if err := Verify(served, key.PublicKey()); err != nil {
			t.Fatalf("%s: an unsigned field broke the signature: %v", c.Name, err)
		}
	}
	// A version 2 decision read as version 3, or the other way round, does not verify.
	v2 := cases()[1].Decision
	msg, _ := SigningBytes(v2)
	v2.EngineSignature = ptr(key.Sign(msg))
	v2.SigningVersion = ptr(api.DecisionSigningVersionN3)
	if Verify(v2, key.PublicKey()) == nil {
		t.Fatal("a version 2 signature verified as version 3")
	}
	if err := Verify(cases()[0].Decision, key.PublicKey()); err == nil || !strings.Contains(err.Error(), "no engineSignature") {
		t.Fatalf("no signature: %v", err)
	}
}

// TestVectors keeps spec/vectors/decision-signing.json equal to the cases
// above, signed with the fixed key. `go test ./decision -update` rewrites it.
func TestVectors(t *testing.T) {
	key := agentkey.FromSeed(vectorSeed())
	file := vectorFile{
		Description: "Ed25519 over the record the engine signs for a decision (Decision.engineSignature). For each case: rebuild the record from `decision` as its signingVersion says (3: id, createdAt, agentId, agentName, connectedAccountId, verdict, proposal, exit, evaluations, state, configRevision, agentSignature, submission; 2: the same with portfolioId; 1 or absent: version 2's with connectedAccountId, connectedAccountIds and maxConnectedAccounts under their old names tradingAccountId, tradingAccountIds and maxTradingAccounts and the value connected_account as trading_account, at every depth), canonicalise it (times to UTC with Z, keys sorted, no whitespace, nulls dropped, evaluations [] when there are none) and it must equal `signingBytes`; `decision.engineSignature` must verify over it under `publicKey`, and signing it with `secret` gives exactly that signature. Any other signingVersion must be refused.",
		Secret:      agentkey.Secret("msk_vector", vectorSeed()),
		PublicKey:   key.PublicKeyB64(),
	}
	for _, c := range cases() {
		msg, err := SigningBytes(c.Decision)
		if err != nil {
			t.Fatal(err)
		}
		c.SigningBytes = string(msg)
		c.Decision.EngineSignature = ptr(key.Sign(msg))
		file.Cases = append(file.Cases, c)
	}
	want, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if *update {
		if err := os.MkdirAll(filepath.Dir(vectorPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vectorPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("spec/vectors/decision-signing.json is out of date: run `go test ./decision -update`")
	}
	// And the file verifies as another reader would take it.
	var back vectorFile
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	pub, err := agentkey.ParsePublicKey(back.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range back.Cases {
		b, err := SigningBytes(c.Decision)
		if err != nil || string(b) != c.SigningBytes {
			t.Errorf("%s: the file's bytes do not round-trip (%v)", c.Name, err)
		}
		if err := Verify(c.Decision, pub); err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
	}
}
