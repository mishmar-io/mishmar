// Package proposal builds and signs what an agent sends to POST /v1/proposals.
//
// The engine authenticates a proposal by its signature alone: the canonical
// JSON of the submitted object without `signature`, signed with the agent's
// key. The bytes are pinned, so an SDK in any language reproduces them; the
// cases in spec/vectors/proposal-signing.json are the test every SDK must pass.
//
// Rules an SDK must follow (see SigningBytes):
//   - one JSON object, no whitespace, keys sorted; optional keys (leverage,
//     price) present only when sent, never null; `signature` left out;
//   - size, price and leverage are decimal strings as the contract's Decimal
//     schema has them, signed byte for byte as written: "0.50" and "0.5" are
//     the same quantity but different signatures, and the engine never
//     reformats what it verifies, so no float may ever touch them;
//   - proposedAt and expiresAt are RFC 3339 in UTC with a Z suffix, the
//     fractional second trimmed of trailing zeros and omitted when zero:
//     2026-10-18T10:00:00Z, 2026-10-18T10:00:00.25Z;
//   - reduceOnly is a JSON boolean; everything else a JSON string;
//   - a cancel (kind "cancel") carries targetClientOrderId and none of the
//     order fields (side, size, price, orderType, leverage, reduceOnly);
//     every other kind carries side, size, orderType and reduceOnly;
//   - venueMessage, when the signer built the venue request itself, is a
//     nested object canonicalised the same way, keys sorted.
package proposal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/canon"
)

// Kind is what the agent proposes to do.
type Kind string

const (
	KindOrder         Kind = "order"
	KindTransaction   Kind = "transaction"
	KindSignedMessage Kind = "signed_message"
	// KindCancel pulls a resting order the agent placed through Mishmar,
	// named by TargetClientOrderID.
	KindCancel Kind = "cancel"
)

// Side of an order.
type Side string

const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

// OrderType: a limit order carries a price; a market order is priced at the
// engine's mark.
type OrderType string

const (
	Limit  OrderType = "limit"
	Market OrderType = "market"
)

// Submit mirrors the contract's ProposalSubmit field for field. The order
// fields are empty for a cancel and omitted from the wire and the signing
// bytes; ReduceOnly is a pointer so an order's false is sent while a
// cancel's absence is not (Bool makes one).
type Submit struct {
	KeyID               string        `json:"keyId"`
	Kind                Kind          `json:"kind"`
	Leverage            *string       `json:"leverage,omitempty"`
	Nonce               string        `json:"nonce"`
	OrderType           OrderType     `json:"orderType,omitempty"`
	Price               *string       `json:"price,omitempty"`
	ProposedAt          time.Time     `json:"proposedAt"`
	ExpiresAt           time.Time     `json:"expiresAt"`
	ReduceOnly          *bool         `json:"reduceOnly,omitempty"`
	ConnectedAccountID  string        `json:"connectedAccountId"`
	Side                Side          `json:"side,omitempty"`
	Size                string        `json:"size,omitempty"`
	Symbol              string        `json:"symbol"`
	TargetClientOrderID string        `json:"targetClientOrderId,omitempty"`
	VenueMessage        *VenueMessage `json:"venueMessage,omitempty"`
	Signature           string        `json:"signature,omitempty"`
}

// VenueMessage mirrors the contract's ProposalVenueMessage: the venue request
// the agent's signer built, with its half of the signature, for the one-shot
// path. Base64 throughout, standard alphabet with padding.
type VenueMessage struct {
	VenueKeyID         string `json:"venueKeyId"`
	Scheme             string `json:"scheme"`
	Message            string `json:"message"`
	EngineCommitmentID string `json:"engineCommitmentId"`
	Commitment         string `json:"commitment,omitempty"`
	Share              string `json:"share"`
}

// Bool returns a pointer to b, for Submit.ReduceOnly.
func Bool(b bool) *bool { return &b }

// unsigned is Submit without `signature`, so the canonical form is the
// submitted object with the signature absent, not empty.
type unsigned struct {
	KeyID               string        `json:"keyId"`
	Kind                Kind          `json:"kind"`
	Leverage            *string       `json:"leverage,omitempty"`
	Nonce               string        `json:"nonce"`
	OrderType           OrderType     `json:"orderType,omitempty"`
	Price               *string       `json:"price,omitempty"`
	ProposedAt          time.Time     `json:"proposedAt"`
	ExpiresAt           time.Time     `json:"expiresAt"`
	ReduceOnly          *bool         `json:"reduceOnly,omitempty"`
	ConnectedAccountID  string        `json:"connectedAccountId"`
	Side                Side          `json:"side,omitempty"`
	Size                string        `json:"size,omitempty"`
	Symbol              string        `json:"symbol"`
	TargetClientOrderID string        `json:"targetClientOrderId,omitempty"`
	VenueMessage        *VenueMessage `json:"venueMessage,omitempty"`
}

// SigningBytes is what the agent signs and the engine verifies: the canonical
// JSON of the proposal with `signature` left out and both times in UTC.
// Changing this shape invalidates every agent SDK; it is part of the contract.
func SigningBytes(s Submit) ([]byte, error) {
	return canon.JSON(unsigned{
		KeyID: s.KeyID, Kind: s.Kind, Leverage: s.Leverage, Nonce: s.Nonce, OrderType: s.OrderType, Price: s.Price,
		ProposedAt: s.ProposedAt.UTC(), ExpiresAt: s.ExpiresAt.UTC(), ReduceOnly: s.ReduceOnly, ConnectedAccountID: s.ConnectedAccountID,
		Side: s.Side, Size: s.Size, Symbol: s.Symbol, TargetClientOrderID: s.TargetClientOrderID, VenueMessage: s.VenueMessage,
	})
}

// Sign checks the proposal's shape, then sets its signature. An order
// without ReduceOnly is signed as reduceOnly false, the default the other
// SDKs use; a cancel is left without it.
func (s *Submit) Sign(key *agentkey.Key) error {
	if s.ReduceOnly == nil && s.Kind != KindCancel {
		s.ReduceOnly = Bool(false)
	}
	if err := s.Validate(); err != nil {
		return err
	}
	msg, err := SigningBytes(*s)
	if err != nil {
		return err
	}
	s.Signature = key.Sign(msg)
	return nil
}

// Verify checks the proposal's signature against the agent's public key as
// the API returns it.
func Verify(publicKeyB64 string, s Submit) bool {
	msg, err := SigningBytes(s)
	if err != nil {
		return false
	}
	return agentkey.Verify(publicKeyB64, msg, s.Signature)
}

// decimal is the contract's Decimal: digits, one optional point, a leading
// minus allowed, no exponent, no sign otherwise.
var decimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// IsDecimal reports whether s is a decimal string the contract accepts.
func IsDecimal(s string) bool { return decimal.MatchString(s) }

// Validate checks the fields the engine would refuse, so the mistake is named
// before anything is signed.
func (s Submit) Validate() error {
	switch {
	case s.KeyID == "":
		return errors.New("proposal: keyId is required")
	case s.ConnectedAccountID == "":
		return errors.New("proposal: connectedAccountId is required")
	case s.Nonce == "":
		return errors.New("proposal: nonce is required")
	case s.Symbol == "":
		return errors.New("proposal: symbol is required")
	case s.ProposedAt.IsZero() || s.ExpiresAt.IsZero():
		return errors.New("proposal: proposedAt and expiresAt are required")
	case !s.ExpiresAt.After(s.ProposedAt):
		return errors.New("proposal: expiresAt must be after proposedAt")
	}
	if err := s.VenueMessage.validate(); err != nil {
		return err
	}
	if s.Kind == KindCancel {
		switch {
		case s.TargetClientOrderID == "":
			return errors.New("proposal: a cancel needs targetClientOrderId, the clientOrderId of the order to pull")
		case s.Side != "" || s.Size != "" || s.Price != nil || s.OrderType != "" || s.Leverage != nil || s.ReduceOnly != nil:
			return errors.New("proposal: a cancel carries no order fields (side, size, price, orderType, leverage, reduceOnly)")
		}
		return nil
	}
	switch {
	case s.Kind != KindOrder && s.Kind != KindTransaction && s.Kind != KindSignedMessage:
		return errors.New("proposal: kind must be order, transaction, signed_message or cancel")
	case s.TargetClientOrderID != "":
		return errors.New("proposal: targetClientOrderId is for cancels only")
	case s.Side != Buy && s.Side != Sell:
		return errors.New("proposal: side must be buy or sell")
	case !IsDecimal(s.Size):
		return errors.New("proposal: size must be a decimal string such as \"0.5\"")
	case s.OrderType == Limit && (s.Price == nil || !IsDecimal(*s.Price)):
		return errors.New("proposal: a limit order needs a decimal price")
	case s.OrderType == Market && s.Price != nil:
		return errors.New("proposal: a market order carries no price")
	case s.OrderType != Limit && s.OrderType != Market:
		return errors.New("proposal: orderType must be limit or market")
	case s.Leverage != nil && !IsDecimal(*s.Leverage):
		return errors.New("proposal: leverage must be a decimal string")
	case s.ReduceOnly == nil:
		return errors.New("proposal: reduceOnly is required on an order (true or false)")
	}
	return nil
}

// validate checks a one-shot venue message's shape; nil is valid (no message).
func (m *VenueMessage) validate() error {
	switch {
	case m == nil:
		return nil
	case m.VenueKeyID == "" || m.Scheme == "" || m.Message == "" || m.EngineCommitmentID == "" || m.Share == "":
		return errors.New("proposal: venueMessage needs venueKeyId, scheme, message, engineCommitmentId and share")
	case m.Scheme != "frost-ed25519" && m.Scheme != "ecdsa-2p":
		return errors.New("proposal: venueMessage.scheme must be frost-ed25519 or ecdsa-2p")
	case m.Scheme == "frost-ed25519" && m.Commitment == "":
		return errors.New("proposal: a FROST venueMessage needs the signer's commitment")
	}
	return nil
}

// NewNonce makes a fresh nonce: 16 random bytes, hex. The engine refuses a
// nonce it has seen from the same key within its replay window.
func NewNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
