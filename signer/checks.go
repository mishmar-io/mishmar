package signer

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/proposal"
	"github.com/mishmar-io/mishmar/venues/binance"
	"github.com/mishmar-io/mishmar/venues/hyperliquid"
)

// Refusal is why the signer would not sign: a stable code (one of the
// Refuse… constants) and what was found. Its Reason goes to the engine and
// is recorded on the decision.
type Refusal struct {
	Code   string
	Detail string
}

// Reason is the one line the engine records: the code, then the detail.
func (r *Refusal) Reason() string {
	if r.Detail == "" {
		return r.Code
	}
	return r.Code + ": " + r.Detail
}

func (r *Refusal) Error() string { return "signer: refused: " + r.Reason() }

// The refusal codes, in the order the checks run.
const (
	RefuseScheme                   = "unsupported_scheme"
	RefuseUnknownKey               = "unknown_venue_key"
	RefuseKeyMismatch              = "venue_key_mismatch"
	RefuseExpired                  = "request_expired"
	RefuseReplay                   = "request_already_answered"
	RefusePurpose                  = "unsupported_purpose"
	RefuseNoProposal               = "no_proposal"
	RefuseForeignProposal          = "foreign_proposal"
	RefuseBadSignature             = "bad_proposal_signature"
	RefuseConnectedAccountMismatch = "connected_account_mismatch"
	RefuseProposalExpired          = "proposal_expired"
	RefuseMessage                  = "message_mismatch"
	RefuseVenue                    = "unsupported_venue"
	RefuseHook                     = "hook_refused"
	RefuseHookTimeout              = "hook_timeout"
	RefuseMalformed                = "malformed_request"
	RefuseNoSlot                   = "no_presigned_slot"
)

// Policy is what the signer refuses before it signs, from what it knows
// about itself: its key, so the proposal must be the agent's own, and the
// venue keys it holds a share of.
type Policy struct {
	// KeyID is the signer's agent key id; PublicKey its public half, base64.
	KeyID     string
	PublicKey string
	VenueKeys map[string]VenueKey
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Answered reports whether this process already answered the request id
	// in this session; nil means never. A request handed out again after it
	// was answered is refused as a replay rather than signed twice.
	Answered func(id string) bool
	// Rules is the coin's Hyperliquid rules on a connected account, as the
	// signer cached them (Signer.Handle fetches them first); false when
	// unknown. Nil refuses every Hyperliquid order.
	Rules func(connectedAccountID, symbol string) (hyperliquid.Rules, bool)
}

// Check runs the checks in order and returns the first refusal, or nil with
// the decoded message when the request may be signed, so the caller signs
// exactly the bytes that were checked:
//
//  1. the scheme is frost-ed25519 or ecdsa-2p (the others are not built yet);
//  2. the venue key is in the config, with that scheme, on the request's connected account;
//  3. the request has not expired and was not answered already;
//  4. the purpose is order, cancel or verify;
//  5. an order or a cancel carries the agent's own proposal: signed by this
//     key, verifying with it, for this connected account, not yet expired;
//  6. the message, decoded, is the order the proposal describes (Binance:
//     checkBinanceOrder), the cancel it names (checkBinanceCancel) or, for
//     verify, no order at all; a Hyperliquid digest is recomputed from the
//     action in the request's body, which must be the proposal's order or
//     cancel (checkHyperliquid).
func (p Policy) Check(req api.SignRequest) ([]byte, *Refusal) {
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	if req.Scheme != api.SigningSchemeFrostEd25519 && req.Scheme != api.SigningSchemeEcdsa2p {
		return nil, &Refusal{RefuseScheme, fmt.Sprintf("%s is not built yet; this signer signs %s and %s", req.Scheme, api.SigningSchemeFrostEd25519, api.SigningSchemeEcdsa2p)}
	}
	key, ok := p.VenueKeys[req.VenueKeyId]
	switch {
	case !ok:
		return nil, &Refusal{RefuseUnknownKey, fmt.Sprintf("this signer holds no share of %s", req.VenueKeyId)}
	case key.Scheme != string(req.Scheme):
		return nil, &Refusal{RefuseKeyMismatch, fmt.Sprintf("%s is a %s key, the request runs %s", req.VenueKeyId, key.Scheme, req.Scheme)}
	case key.ConnectedAccountID != req.ConnectedAccountId:
		return nil, &Refusal{RefuseKeyMismatch, fmt.Sprintf("%s belongs to connected account %s, the request is for %s", req.VenueKeyId, key.ConnectedAccountID, req.ConnectedAccountId)}
	case key.Venue != string(req.Venue):
		return nil, &Refusal{RefuseKeyMismatch, fmt.Sprintf("%s is a %s key, the request is for %s", req.VenueKeyId, key.Venue, req.Venue)}
	}
	if !now.Before(req.ExpiresAt) {
		return nil, &Refusal{RefuseExpired, fmt.Sprintf("expired at %s", req.ExpiresAt.UTC().Format(time.RFC3339))}
	}
	if p.Answered != nil && p.Answered(req.Id) {
		return nil, &Refusal{RefuseReplay, fmt.Sprintf("%s was answered in this session already (attempt %d)", req.Id, req.Attempt)}
	}
	switch req.Purpose {
	case api.SignRequestPurposeOrder, api.SignRequestPurposeCancel:
		if r := checkProposal(p, req, now); r != nil {
			return nil, r
		}
	case api.SignRequestPurposeVerify:
	default:
		return nil, &Refusal{RefusePurpose, fmt.Sprintf("%s is not signed by this signer", req.Purpose)}
	}
	message, err := base64.StdEncoding.DecodeString(req.Message)
	if err != nil {
		return nil, &Refusal{RefuseMalformed, "message is not base64"}
	}
	switch req.Venue {
	case api.VenueBinance:
		var r *Refusal
		switch req.Purpose {
		case api.SignRequestPurposeVerify:
			r = checkBinanceRead(req, message)
		case api.SignRequestPurposeCancel:
			r = checkBinanceCancel(req, message)
		default:
			r = checkBinanceOrder(req, message)
		}
		if r != nil {
			return nil, r
		}
	case api.VenueHyperliquid:
		if r := checkHyperliquid(p, req, key, message, now); r != nil {
			return nil, r
		}
	default:
		return nil, &Refusal{RefuseVenue, fmt.Sprintf("%s is not supported by this signer", req.Venue)}
	}
	return message, nil
}

// hlIntentOf is the order a proposal describes, as the Hyperliquid package reads it.
func hlIntentOf(p api.ProposalSubmit) hyperliquid.Intent {
	in := hyperliquid.Intent{
		Symbol: p.Symbol, Side: string(p.Side), OrderType: string(p.OrderType), Size: p.Size,
		ReduceOnly: p.ReduceOnly != nil && *p.ReduceOnly,
	}
	if p.Price != nil {
		in.Price = *p.Price
	}
	return in
}

// checkHyperliquid checks a two-party ECDSA request: the message is a
// 32-byte digest that cannot be read, so the request's body carries the
// action and nonce it came from; the signer decodes them, recomputes the
// digest for the key's network and compares, checks the nonce is fresh,
// and checks the action is the proposal's order (hyperliquid.CheckOrder,
// under the coin's rules) or the cancel of its target. A verify request has
// nothing to sign: a Hyperliquid key is verified by its approval.
func checkHyperliquid(p Policy, req api.SignRequest, key VenueKey, digest []byte, now time.Time) *Refusal {
	if req.Purpose == api.SignRequestPurposeVerify {
		return &Refusal{RefusePurpose, "a Hyperliquid key is verified by the master wallet's approval; there is nothing to sign"}
	}
	if req.Scheme != api.SigningSchemeEcdsa2p {
		return &Refusal{RefuseScheme, fmt.Sprintf("a Hyperliquid key signs %s, not %s", api.SigningSchemeEcdsa2p, req.Scheme)}
	}
	network, err := hyperliquid.ParseNetwork(key.Network)
	if err != nil {
		return &Refusal{RefuseMalformed, fmt.Sprintf("the config names no network for %s", req.VenueKeyId)}
	}
	if req.VenueRequest == nil || req.VenueRequest.Body == nil {
		return &Refusal{RefuseMalformed, "the request carries no venue request body with the action"}
	}
	body, err := base64.StdEncoding.DecodeString(*req.VenueRequest.Body)
	if err != nil {
		return &Refusal{RefuseMalformed, "the venue request body is not base64"}
	}
	m, action, err := hyperliquid.Decode(body)
	if err != nil {
		return refusalOf(err)
	}
	want, err := hyperliquid.Digest(action, m.Nonce, network)
	if err != nil {
		return &Refusal{RefuseMalformed, err.Error()}
	}
	if !bytes.Equal(want, digest) {
		return &Refusal{RefuseMessage, "the message is not the digest of the action in the request"}
	}
	if err := hyperliquid.CheckFresh(m.Nonce, now); err != nil {
		return refusalOf(err)
	}
	prop := req.Proposal
	if prop == nil {
		return &Refusal{RefuseNoProposal, "an order request must carry the agent's signed proposal"}
	}
	if p.Rules == nil {
		return &Refusal{RefuseMalformed, "this signer has no venue rules to check Hyperliquid orders against"}
	}
	rules, ok := p.Rules(req.ConnectedAccountId, prop.Symbol)
	if !ok {
		return &Refusal{RefuseMalformed, fmt.Sprintf("no venue rules for %s on %s", prop.Symbol, req.ConnectedAccountId)}
	}
	switch req.Purpose {
	case api.SignRequestPurposeCancel:
		if prop.Kind != api.ProposalKindCancel || prop.TargetClientOrderId == nil {
			return &Refusal{RefuseMessage, fmt.Sprintf("a cancel request carries a proposal of kind %s, not a cancel", prop.Kind)}
		}
		c, err := hyperliquid.DecodeCancel(action)
		if err != nil {
			return refusalOf(err)
		}
		return refusalOf(hyperliquid.CheckCancel(c, *prop.TargetClientOrderId, rules.AssetIndex))
	default:
		if req.ClientOrderId == nil || *req.ClientOrderId == "" {
			return &Refusal{RefuseMalformed, "the request names no clientOrderId"}
		}
		if m.ClientOrderID != *req.ClientOrderId {
			return &Refusal{RefuseMessage, fmt.Sprintf("the action is for client order id %q, the request names %q", m.ClientOrderID, *req.ClientOrderId)}
		}
		o, err := hyperliquid.DecodeOrder(action)
		if err != nil {
			return refusalOf(err)
		}
		return refusalOf(hyperliquid.CheckOrder(o, hlIntentOf(*prop), rules, m.ClientOrderID))
	}
}

// checkProposal checks that an order request carries the agent's own
// proposal: this key's, verifying, for the request's connected account, not expired.
func checkProposal(p Policy, req api.SignRequest, now time.Time) *Refusal {
	prop := req.Proposal
	switch {
	case prop == nil:
		return &Refusal{RefuseNoProposal, "an order request must carry the agent's signed proposal"}
	case prop.KeyId != p.KeyID:
		return &Refusal{RefuseForeignProposal, fmt.Sprintf("the proposal was signed by %s, this signer is %s", prop.KeyId, p.KeyID)}
	case !proposal.Verify(p.PublicKey, submitOf(*prop)):
		return &Refusal{RefuseBadSignature, "the proposal's signature does not verify with this signer's key"}
	case prop.ConnectedAccountId != req.ConnectedAccountId:
		return &Refusal{RefuseConnectedAccountMismatch, fmt.Sprintf("the proposal is for connected account %s, the request for %s", prop.ConnectedAccountId, req.ConnectedAccountId)}
	case !now.Before(prop.ExpiresAt):
		return &Refusal{RefuseProposalExpired, fmt.Sprintf("the proposal expired at %s", prop.ExpiresAt.UTC().Format(time.RFC3339))}
	}
	return nil
}

// intentOf is the order a proposal describes, as the venues package reads it.
func intentOf(p api.ProposalSubmit) binance.Intent {
	in := binance.Intent{
		Symbol: p.Symbol, Side: string(p.Side), OrderType: string(p.OrderType), Size: p.Size,
		ReduceOnly: p.ReduceOnly != nil && *p.ReduceOnly,
	}
	if p.Price != nil {
		in.Price = *p.Price
	}
	return in
}

// checkBinanceOrder checks a Binance order's parameter string against the
// request and the agent's proposal (binance.CheckOrder): the client order id
// is the request's, the symbol the request's and the proposal's, the side,
// the type, the quantity at most the proposed size, a limit price within 1 %
// on the passive side, reduceOnly the proposal's, and no parameter outside
// the known set.
func checkBinanceOrder(req api.SignRequest, message []byte) *Refusal {
	prop := req.Proposal
	if prop == nil {
		return &Refusal{RefuseNoProposal, "an order request must carry the agent's signed proposal"}
	}
	o, err := binance.DecodeOrder(message)
	if err != nil {
		return refusalOf(err)
	}
	if req.ClientOrderId == nil || *req.ClientOrderId == "" {
		return &Refusal{RefuseMalformed, "the request names no clientOrderId"}
	}
	if req.Order == nil {
		return &Refusal{RefuseMalformed, "the request carries no order"}
	}
	return refusalOf(binance.CheckOrder(o, intentOf(*prop), req.Order.Symbol, *req.ClientOrderId))
}

// checkBinanceCancel checks a Binance cancel against the agent's cancel
// proposal: it pulls the order the proposal names, on the proposal's symbol,
// by client order id and nothing else, with DELETE.
func checkBinanceCancel(req api.SignRequest, message []byte) *Refusal {
	prop := req.Proposal
	if prop == nil {
		return &Refusal{RefuseNoProposal, "a cancel request must carry the agent's signed proposal"}
	}
	if prop.Kind != api.ProposalKindCancel || prop.TargetClientOrderId == nil {
		return &Refusal{RefuseMessage, fmt.Sprintf("a cancel request carries a proposal of kind %s, not a cancel", prop.Kind)}
	}
	c, err := binance.DecodeCancel(message)
	if err != nil {
		return refusalOf(err)
	}
	switch {
	case c.ClientOrderID != *prop.TargetClientOrderId:
		return &Refusal{RefuseMessage, fmt.Sprintf("origClientOrderId %q is not the proposed %q", c.ClientOrderID, *prop.TargetClientOrderId)}
	case !strings.HasPrefix(c.Symbol, strings.ToUpper(prop.Symbol)):
		return &Refusal{RefuseMessage, fmt.Sprintf("symbol %q is not the proposed %s", c.Symbol, strings.ToUpper(prop.Symbol))}
	case req.VenueRequest != nil && !strings.EqualFold(req.VenueRequest.Method, http.MethodDelete):
		return &Refusal{RefuseMessage, fmt.Sprintf("a cancel is a DELETE, not %s %s", req.VenueRequest.Method, req.VenueRequest.Url)}
	}
	return nil
}

// checkBinanceRead checks that what a verify request asks to sign is a read
// and not an order: a parameter string without symbol, side, type,
// quantity, price or a client order id, for a GET.
func checkBinanceRead(req api.SignRequest, message []byte) *Refusal {
	if err := binance.CheckRead(message); err != nil {
		return refusalOf(err)
	}
	if req.VenueRequest != nil && !strings.EqualFold(req.VenueRequest.Method, http.MethodGet) {
		return &Refusal{RefuseMessage, fmt.Sprintf("a verify request is a read, not %s %s", req.VenueRequest.Method, req.VenueRequest.Url)}
	}
	return nil
}

// refusalOf turns the venues package's verdict into a refusal: a message
// that is not what it should be is message_mismatch, anything else (a bad
// proposal, a request without what it needs) malformed_request.
func trimVenue(err error) string {
	return strings.TrimPrefix(strings.TrimPrefix(err.Error(), "binance: "), "hyperliquid: ")
}

func refusalOf(err error) *Refusal {
	switch {
	case err == nil:
		return nil
	case binance.IsMismatch(err), hyperliquid.IsMismatch(err):
		return &Refusal{RefuseMessage, trimVenue(err)}
	default:
		return &Refusal{RefuseMalformed, trimVenue(err)}
	}
}
