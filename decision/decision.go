// Package decision checks the engine's signature on a decision
// (Decision.engineSignature) from the decision alone, as the engine serves
// it, with the engine's public key (GET /ledger/head, enginePublicKey).
//
// The engine signs the record as it writes it: the canonical JSON (canon) of
// id, createdAt, agentId, agentName, connectedAccountId, verdict, proposal,
// exit (when set), evaluations, state, configRevision, agentSignature and
// submission (when set). The rest of a served decision (the reason, the
// latency, the venue's answer, the approval, the signing outcome, the
// valuation, the cancellation) arrives later or is a line for a person, and
// is not covered. Which record was signed depends on the decision's
// signingVersion:
//
//   - 3 (contract 0.21.0): the fields above, and no portfolioId. A
//     portfolioId on a version 3 decision is not signed and is ignored.
//   - 2 (contract 0.20.0): the same, with portfolioId, the decision's own.
//   - 1, or none (before contract 0.20.0): version 2's record with the
//     connected account under its old names, at every depth: the keys
//     tradingAccountId, tradingAccountIds and maxTradingAccounts, and the
//     scope kind trading_account.
//
// A version this package does not know is refused, never guessed: its bytes
// cannot be rebuilt here, and a guess would pass a record nobody checked.
package decision

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/canon"
)

// Latest is the newest signing version this package rebuilds.
const Latest = api.DecisionSigningVersionN3

// record is what the engine signs for a decision. PortfolioId is set on
// versions 1 and 2 only.
type record struct {
	Id                 string                  `json:"id"`
	CreatedAt          time.Time               `json:"createdAt"`
	AgentId            string                  `json:"agentId"`
	AgentName          string                  `json:"agentName"`
	PortfolioId        *string                 `json:"portfolioId,omitempty"`
	ConnectedAccountId string                  `json:"connectedAccountId"`
	Verdict            api.Verdict             `json:"verdict"`
	Proposal           api.Proposal            `json:"proposal"`
	Exit               *bool                   `json:"exit,omitempty"`
	Evaluations        []api.RuleEvaluation    `json:"evaluations"`
	State              api.StateSnapshot       `json:"state"`
	ConfigRevision     int                     `json:"configRevision"`
	AgentSignature     string                  `json:"agentSignature"`
	Submission         *api.DecisionSubmission `json:"submission,omitempty"`
}

// Version is the signing version of d: its signingVersion, or 1 when it
// names none (a decision from before contract 0.20.0).
func Version(d api.Decision) api.DecisionSigningVersion {
	if d.SigningVersion == nil {
		return api.DecisionSigningVersionN1
	}
	return *d.SigningVersion
}

// SigningBytes rebuilds the bytes the engine signed for d, in the shape its
// signing version names. An unknown version is an error.
func SigningBytes(d api.Decision) ([]byte, error) {
	v := Version(d)
	r := record{
		Id: d.Id, CreatedAt: d.CreatedAt.UTC(), AgentId: d.AgentId, AgentName: d.AgentName,
		ConnectedAccountId: d.ConnectedAccountId, Verdict: d.Verdict, Proposal: d.Proposal, Exit: d.Exit,
		Evaluations: d.Evaluations, State: d.State, ConfigRevision: d.ConfigRevision,
		AgentSignature: d.AgentSignature, Submission: d.Submission,
	}
	if r.Evaluations == nil {
		r.Evaluations = []api.RuleEvaluation{}
	}
	switch v {
	case api.DecisionSigningVersionN3:
	case api.DecisionSigningVersionN1, api.DecisionSigningVersionN2:
		// The record carried the agent's portfolio as a plain string, so a
		// decision that has lost it was signed with "".
		p := ""
		if d.PortfolioId != nil {
			p = *d.PortfolioId
		}
		r.PortfolioId = &p
	default:
		return nil, fmt.Errorf("decision %s: signing version %d is not one this client knows (1 to %d); a newer engine signed it: update the client to verify it", d.Id, v, Latest)
	}
	msg, err := canon.JSON(r)
	if err != nil {
		return nil, err
	}
	if v == api.DecisionSigningVersionN1 {
		return oldNames(msg)
	}
	return msg, nil
}

// Verify checks d's engineSignature against the bytes SigningBytes rebuilds,
// under the engine's public key.
func Verify(d api.Decision, engineKey ed25519.PublicKey) error {
	if len(engineKey) != ed25519.PublicKeySize {
		return errors.New("decision: the engine's public key is not an Ed25519 key (GET /ledger/head, enginePublicKey)")
	}
	if d.EngineSignature == nil || *d.EngineSignature == "" {
		return fmt.Errorf("decision %s: no engineSignature to verify", d.Id)
	}
	sig, err := base64.StdEncoding.DecodeString(*d.EngineSignature)
	if err != nil {
		return fmt.Errorf("decision %s: engineSignature is not base64: %v", d.Id, err)
	}
	msg, err := SigningBytes(d)
	if err != nil {
		return err
	}
	if !ed25519.Verify(engineKey, msg, sig) {
		return fmt.Errorf("decision %s: the engine's signature does not verify (signing version %d): the record was changed after it was signed, or another engine key signed it", d.Id, Version(d))
	}
	return nil
}

// The names contract 0.20.0 changed, today's to the old: keys by exact
// name, and the one enum value by exact match, at every depth.
var (
	oldKeys = map[string]string{
		"connectedAccountId":   "tradingAccountId",
		"connectedAccountIds":  "tradingAccountIds",
		"maxConnectedAccounts": "maxTradingAccounts",
	}
	oldValues = map[string]string{"connected_account": "trading_account"}
)

// oldNames turns canonical JSON in today's names into the canonical JSON of
// the same record as written before contract 0.20.0, keys sorted again.
// Where an object has a key under both names, the old one stays.
func oldNames(canonical []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(renamed(v))
}

func renamed(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			to, moved := oldKeys[k]
			if !moved {
				out[k] = renamed(val)
				continue
			}
			if _, both := t[to]; !both {
				out[to] = renamed(val)
			}
		}
		return out
	case []any:
		for i := range t {
			t[i] = renamed(t[i])
		}
		return t
	case string:
		if to, ok := oldValues[t]; ok {
			return to
		}
		return t
	default:
		return v
	}
}
