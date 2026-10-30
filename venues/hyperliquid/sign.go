package hyperliquid

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/vmihailenco/msgpack/v5"
	"golang.org/x/crypto/sha3"
)

// What an API wallet signs, byte for byte as hyperliquid/utils/signing.py:
//
//	connectionId = keccak256(msgpack(action) ‖ nonce as 8 bytes big-endian
//	                         ‖ 0x00                       when no vault
//	                         ‖ 0x01 ‖ vault (20 bytes)    when trading for a vault/subaccount
//	                         ‖ 0x00 ‖ expiresAfter (8 BE) only when expiresAfter is set)
//	digest = EIP-712(domain {Exchange, 1, chainId 1337, 0x0}, Agent{source, connectionId})
//	source = "a" on mainnet, "b" on testnet
//
// The msgpack bytes follow Python's msgpack.packb of a dict in insertion
// order, so an action's field order is part of its signature: every action
// spells its order out with ordered(), nothing goes through reflection.

// Network is a Hyperliquid deployment: separate chains, hosts, approvals and
// signing inputs.
type Network string

const (
	Mainnet Network = "mainnet"
	Testnet Network = "testnet"
)

// ParseNetwork accepts "mainnet" or "testnet".
func ParseNetwork(s string) (Network, error) {
	n := Network(s)
	if n != Mainnet && n != Testnet {
		return "", fmt.Errorf("hyperliquid: unknown network %q (want mainnet or testnet)", s)
	}
	return n, nil
}

// Source is the phantom agent's source in the L1 signing scheme.
func (n Network) Source() string {
	if n == Mainnet {
		return "a"
	}
	return "b"
}

// Action is an L1 action: it knows its msgpack field order and marshals to
// the JSON the exchange endpoint accepts.
type Action interface {
	ordered() omap
	// ActionType is the wire's type: order, cancelByCloid.
	ActionType() string
}

// Wire shapes: the single-letter keys of the exchange endpoint, in the Python
// SDK's order (order_request_to_order_wire, order_wires_to_order_action).

// OrderAction places orders.
type OrderAction struct {
	Type     string      `json:"type"` // "order"
	Orders   []OrderWire `json:"orders"`
	Grouping string      `json:"grouping"` // "na" | "normalTpsl" | "positionTpsl"
	Builder  *Builder    `json:"builder,omitempty"`
}

// OrderWire is one order on the wire.
type OrderWire struct {
	A int           `json:"a"` // asset
	B bool          `json:"b"` // isBuy
	P string        `json:"p"` // price
	S string        `json:"s"` // size
	R bool          `json:"r"` // reduceOnly
	T OrderTypeWire `json:"t"`
	C *string       `json:"c,omitempty"` // cloid
}

// OrderTypeWire is the order's type: a limit order or a trigger.
type OrderTypeWire struct {
	Limit   *LimitWire   `json:"limit,omitempty"`
	Trigger *TriggerWire `json:"trigger,omitempty"`
}

// LimitWire is a limit order's time in force.
type LimitWire struct {
	Tif TIF `json:"tif"`
}

// TriggerWire is a stop or take-profit trigger.
type TriggerWire struct {
	IsMarket  bool   `json:"isMarket"`
	TriggerPx string `json:"triggerPx"`
	Tpsl      string `json:"tpsl"` // "tp" | "sl"
}

// Builder is the optional builder-fee object; f is in tenths of a basis point.
type Builder struct {
	B string `json:"b"`
	F int    `json:"f"`
}

func (a OrderAction) ActionType() string { return a.Type }

func (a OrderAction) ordered() omap {
	orders := make([]omap, len(a.Orders))
	for i, o := range a.Orders {
		orders[i] = o.ordered()
	}
	m := omap{{"type", a.Type}, {"orders", orders}, {"grouping", a.Grouping}}
	if a.Builder != nil {
		m = append(m, field{"builder", omap{{"b", a.Builder.B}, {"f", a.Builder.F}}})
	}
	return m
}

func (o OrderWire) ordered() omap {
	m := omap{{"a", o.A}, {"b", o.B}, {"p", o.P}, {"s", o.S}, {"r", o.R}, {"t", o.T.ordered()}}
	if o.C != nil {
		m = append(m, field{"c", *o.C})
	}
	return m
}

func (t OrderTypeWire) ordered() omap {
	if t.Limit != nil {
		return omap{{"limit", omap{{"tif", string(t.Limit.Tif)}}}}
	}
	if t.Trigger != nil {
		return omap{{"trigger", omap{{"isMarket", t.Trigger.IsMarket}, {"triggerPx", t.Trigger.TriggerPx}, {"tpsl", t.Trigger.Tpsl}}}}
	}
	return omap{}
}

// CancelAction cancels orders by order id.
type CancelAction struct {
	Type    string       `json:"type"` // "cancel"
	Cancels []CancelWire `json:"cancels"`
}

// CancelWire is one cancel by order id.
type CancelWire struct {
	A int    `json:"a"`
	O uint64 `json:"o"`
}

func (a CancelAction) ActionType() string { return a.Type }

func (a CancelAction) ordered() omap {
	cancels := make([]omap, len(a.Cancels))
	for i, c := range a.Cancels {
		cancels[i] = omap{{"a", c.A}, {"o", c.O}}
	}
	return omap{{"type", a.Type}, {"cancels", cancels}}
}

// CancelByCloidAction cancels orders by client order id.
type CancelByCloidAction struct {
	Type    string              `json:"type"` // "cancelByCloid"
	Cancels []CancelByCloidWire `json:"cancels"`
}

// CancelByCloidWire is one cancel by cloid.
type CancelByCloidWire struct {
	Asset int    `json:"asset"`
	Cloid string `json:"cloid"`
}

func (a CancelByCloidAction) ActionType() string { return a.Type }

func (a CancelByCloidAction) ordered() omap {
	cancels := make([]omap, len(a.Cancels))
	for i, c := range a.Cancels {
		cancels[i] = omap{{"asset", c.Asset}, {"cloid", c.Cloid}}
	}
	return omap{{"type", a.Type}, {"cancels", cancels}}
}

// ActionHash is the Python SDK's action_hash: the connectionId of the phantom
// agent. vaultAddress is "" when trading for the signer's own account.
func ActionHash(action Action, nonce uint64, vaultAddress string, expiresAfter *uint64) ([]byte, error) {
	data, err := packAction(action.ordered())
	if err != nil {
		return nil, err
	}
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], nonce)
	data = append(data, n[:]...)
	if vaultAddress == "" {
		data = append(data, 0x00)
	} else {
		addr, err := decodeHex(vaultAddress)
		if err != nil || len(addr) != 20 {
			return nil, fmt.Errorf("hyperliquid: vault address %q is not a 20-byte hex address", vaultAddress)
		}
		data = append(data, 0x01)
		data = append(data, addr...)
	}
	if expiresAfter != nil {
		var e [8]byte
		binary.BigEndian.PutUint64(e[:], *expiresAfter)
		data = append(data, 0x00)
		data = append(data, e[:]...)
	}
	return Keccak256(data), nil
}

// Digest is what the API wallet signs for a message's action and nonce, on
// the network: the EIP-712 digest of the phantom agent. Both the signer and
// the engine compute it from the action themselves, never trusting a digest
// they were handed.
func Digest(action Action, nonce uint64, network Network) ([]byte, error) {
	h, err := ActionHash(action, nonce, "", nil)
	if err != nil {
		return nil, err
	}
	return L1TypedData(network, h).Digest()
}

// Keccak256 is the hash EVM signing schemes use.
func Keccak256(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// AddressOf is the EVM address of a public key: the last 20 bytes of the
// Keccak-256 of the uncompressed key without its 0x04 prefix, as 0x and 40
// lower-case hex characters.
func AddressOf(pub *secp256k1.PublicKey) string {
	raw := pub.SerializeUncompressed()[1:]
	return "0x" + hex.EncodeToString(Keccak256(raw)[12:])
}

// Signature is what the exchange endpoint accepts: r and s as 0x hex without
// leading zeros (the Python SDK's to_hex of an int) and v as 27 or 28.
type Signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V uint8  `json:"v"`
}

// SignatureFromRS formats a signature from its 32-byte r and s and its
// recovery id (0 or 1, or 27 or 28).
func SignatureFromRS(r, s []byte, recovery uint8) (Signature, error) {
	if len(r) != 32 || len(s) != 32 {
		return Signature{}, errors.New("hyperliquid: r and s are 32 bytes each")
	}
	v := recovery
	if v < 27 {
		v += 27
	}
	if v != 27 && v != 28 {
		return Signature{}, fmt.Errorf("hyperliquid: recovery id %d is not 0, 1, 27 or 28", recovery)
	}
	return Signature{R: minimalHex(r), S: minimalHex(s), V: v}, nil
}

func minimalHex(b []byte) string { return "0x" + new(big.Int).SetBytes(b).Text(16) }

// Bytes is the 65-byte r ‖ s ‖ v form.
func (s Signature) Bytes() ([]byte, error) {
	r, err := wordFromHex(s.R)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: signature r: %w", err)
	}
	sb, err := wordFromHex(s.S)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: signature s: %w", err)
	}
	if s.V != 27 && s.V != 28 {
		return nil, fmt.Errorf("hyperliquid: signature v must be 27 or 28, got %d", s.V)
	}
	out := make([]byte, 0, 65)
	out = append(out, r[:]...)
	out = append(out, sb[:]...)
	return append(out, s.V), nil
}

func wordFromHex(s string) ([32]byte, error) {
	var w [32]byte
	n, err := toBigUint(s)
	if err != nil {
		return w, err
	}
	if n.BitLen() > 256 {
		return w, errors.New("value does not fit 32 bytes")
	}
	n.FillBytes(w[:])
	return w, nil
}

// RecoverAddress returns the lowercase 0x address that produced sig over digest.
func RecoverAddress(digest []byte, sig Signature) (string, error) {
	raw, err := sig.Bytes()
	if err != nil {
		return "", err
	}
	compact := make([]byte, 0, 65)
	compact = append(compact, raw[64])
	compact = append(compact, raw[:64]...)
	pub, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		return "", fmt.Errorf("hyperliquid: recover signer: %w", err)
	}
	return AddressOf(pub), nil
}

// ---- msgpack, as Python packs a dict ----

// omap is a msgpack map whose keys are written in the order given.
type omap []field

type field struct {
	key string
	val any
}

// packAction encodes v exactly as Python's msgpack.packb would: fixmap/map16/
// map32, fixarray/array16/array32, str8/str16/str32 (never bin for strings),
// positive ints in the smallest unsigned form, negative ints in the smallest
// signed form, bools, nil. The vmihailenco encoder is used only for the
// primitive format bytes; the structure is walked here.
func packAction(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	if err := packValue(enc, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func packValue(enc *msgpack.Encoder, v any) error {
	switch x := v.(type) {
	case nil:
		return enc.EncodeNil()
	case omap:
		if err := enc.EncodeMapLen(len(x)); err != nil {
			return err
		}
		for _, f := range x {
			if err := enc.EncodeString(f.key); err != nil {
				return err
			}
			if err := packValue(enc, f.val); err != nil {
				return fmt.Errorf("key %q: %w", f.key, err)
			}
		}
		return nil
	case []omap:
		if err := enc.EncodeArrayLen(len(x)); err != nil {
			return err
		}
		for _, m := range x {
			if err := packValue(enc, m); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if err := enc.EncodeArrayLen(len(x)); err != nil {
			return err
		}
		for _, e := range x {
			if err := packValue(enc, e); err != nil {
				return err
			}
		}
		return nil
	case string:
		return enc.EncodeString(x)
	case bool:
		return enc.EncodeBool(x)
	case uint64:
		return enc.EncodeUint(x)
	case uint32:
		return enc.EncodeUint(uint64(x))
	case uint:
		return enc.EncodeUint(uint64(x))
	case int:
		if x < 0 {
			return enc.EncodeInt(int64(x))
		}
		return enc.EncodeUint(uint64(x))
	case int64:
		if x < 0 {
			return enc.EncodeInt(x)
		}
		return enc.EncodeUint(uint64(x))
	case []byte:
		return enc.EncodeBytes(x)
	default:
		return fmt.Errorf("hyperliquid: msgpack: unsupported value type %T", v)
	}
}

// ---- EIP-712, flat structs of atomic types, which is all Hyperliquid uses ----

// TypedDataField is one member of an EIP-712 struct type.
type TypedDataField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// TypedDataDomain is the EIP712Domain. The zero verifying contract is spelled
// out, not omitted.
type TypedDataDomain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// TypedData is an EIP-712 typed-data document and the input to Digest.
// Message values are plain Go values (string, bool, uint64, []byte, 0x hex
// strings) or what encoding/json produces when the document is round-tripped.
type TypedData struct {
	Types       map[string][]TypedDataField `json:"types"`
	PrimaryType string                      `json:"primaryType"`
	Domain      TypedDataDomain             `json:"domain"`
	Message     map[string]any              `json:"message"`
}

const zeroAddress = "0x0000000000000000000000000000000000000000"

var eip712DomainFields = []TypedDataField{
	{Name: "name", Type: "string"},
	{Name: "version", Type: "string"},
	{Name: "chainId", Type: "uint256"},
	{Name: "verifyingContract", Type: "address"},
}

// L1TypedData is the EIP-712 document for a phantom agent (sign_l1_action's
// l1_payload): domain Exchange/1/1337/0x0, Agent{source, connectionId}.
func L1TypedData(network Network, connectionID []byte) TypedData {
	return TypedData{
		Types: map[string][]TypedDataField{
			"EIP712Domain": eip712DomainFields,
			"Agent": {
				{Name: "source", Type: "string"},
				{Name: "connectionId", Type: "bytes32"},
			},
		},
		PrimaryType: "Agent",
		Domain:      TypedDataDomain{Name: "Exchange", Version: "1", ChainID: 1337, VerifyingContract: zeroAddress},
		Message:     map[string]any{"source": network.Source(), "connectionId": connectionID},
	}
}

// DomainSeparator is hashStruct(EIP712Domain, domain).
func (td TypedData) DomainSeparator() ([]byte, error) {
	fields := td.Types["EIP712Domain"]
	if fields == nil {
		fields = eip712DomainFields
	}
	msg := map[string]any{
		"name":              td.Domain.Name,
		"version":           td.Domain.Version,
		"chainId":           td.Domain.ChainID,
		"verifyingContract": td.Domain.VerifyingContract,
	}
	return hashStruct("EIP712Domain", fields, msg)
}

// StructHash is hashStruct(primaryType, message).
func (td TypedData) StructHash() ([]byte, error) {
	fields, ok := td.Types[td.PrimaryType]
	if !ok {
		return nil, fmt.Errorf("eip712: primary type %q is not in types", td.PrimaryType)
	}
	return hashStruct(td.PrimaryType, fields, td.Message)
}

// Digest is what gets signed: keccak256(0x19 0x01 ‖ domainSeparator ‖ structHash).
func (td TypedData) Digest() ([]byte, error) {
	ds, err := td.DomainSeparator()
	if err != nil {
		return nil, err
	}
	sh, err := td.StructHash()
	if err != nil {
		return nil, err
	}
	return Keccak256([]byte{0x19, 0x01}, ds, sh), nil
}

// encodeType spells the type the EIP-712 way: Name(type1 name1,type2 name2).
func encodeType(name string, fields []TypedDataField) (string, error) {
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('(')
	for i, f := range fields {
		if !isAtomicType(f.Type) {
			return "", fmt.Errorf("eip712: field %s.%s has type %q; only atomic types are supported", name, f.Name, f.Type)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(f.Type)
		b.WriteByte(' ')
		b.WriteString(f.Name)
	}
	b.WriteByte(')')
	return b.String(), nil
}

// hashStruct is keccak256(typeHash ‖ encodeData(fields...)).
func hashStruct(name string, fields []TypedDataField, msg map[string]any) ([]byte, error) {
	typ, err := encodeType(name, fields)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, 32*(len(fields)+1))
	buf = append(buf, Keccak256([]byte(typ))...)
	for _, f := range fields {
		v, ok := msg[f.Name]
		if !ok {
			return nil, fmt.Errorf("eip712: message is missing field %q", f.Name)
		}
		word, err := encodeValue(f.Type, v)
		if err != nil {
			return nil, fmt.Errorf("eip712: field %q: %w", f.Name, err)
		}
		buf = append(buf, word[:]...)
	}
	return Keccak256(buf), nil
}

func isAtomicType(t string) bool {
	switch t {
	case "string", "bytes", "bytes32", "address", "bool":
		return true
	}
	return uintBits(t) > 0
}

// uintBits returns N for "uintN" (N in 8..256, multiple of 8), else 0.
func uintBits(t string) int {
	if !strings.HasPrefix(t, "uint") {
		return 0
	}
	bits, err := strconv.Atoi(t[len("uint"):])
	if err != nil || bits <= 0 || bits > 256 || bits%8 != 0 {
		return 0
	}
	return bits
}

// encodeValue encodes one atomic value into its 32-byte EIP-712 word.
func encodeValue(typ string, v any) ([32]byte, error) {
	var word [32]byte
	switch {
	case typ == "string":
		s, ok := v.(string)
		if !ok {
			return word, fmt.Errorf("want string, got %T", v)
		}
		copy(word[:], Keccak256([]byte(s)))
	case typ == "bytes":
		b, err := toBytes(v)
		if err != nil {
			return word, err
		}
		copy(word[:], Keccak256(b))
	case typ == "bytes32":
		b, err := toBytes(v)
		if err != nil {
			return word, err
		}
		if len(b) != 32 {
			return word, fmt.Errorf("want 32 bytes, got %d", len(b))
		}
		copy(word[:], b)
	case typ == "address":
		b, err := toBytes(v)
		if err != nil {
			return word, err
		}
		if len(b) != 20 {
			return word, fmt.Errorf("want a 20-byte address, got %d bytes", len(b))
		}
		copy(word[12:], b)
	case typ == "bool":
		b, ok := v.(bool)
		if !ok {
			return word, fmt.Errorf("want bool, got %T", v)
		}
		if b {
			word[31] = 1
		}
	case uintBits(typ) > 0:
		n, err := toBigUint(v)
		if err != nil {
			return word, err
		}
		if n.BitLen() > uintBits(typ) {
			return word, fmt.Errorf("%s does not fit %s", n, typ)
		}
		n.FillBytes(word[:])
	default:
		return word, fmt.Errorf("unsupported type %q", typ)
	}
	return word, nil
}

func toBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case []byte:
		return x, nil
	case [32]byte:
		return x[:], nil
	case [20]byte:
		return x[:], nil
	case string:
		return decodeHex(x)
	}
	return nil, fmt.Errorf("want bytes or a hex string, got %T", v)
}

// decodeHex accepts hex with or without a 0x prefix, any case.
func decodeHex(s string) ([]byte, error) {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%q is not hex: %w", s, err)
	}
	return b, nil
}

func toBigUint(v any) (*big.Int, error) {
	switch x := v.(type) {
	case uint64:
		return new(big.Int).SetUint64(x), nil
	case uint:
		return new(big.Int).SetUint64(uint64(x)), nil
	case uint32:
		return new(big.Int).SetUint64(uint64(x)), nil
	case int:
		if x < 0 {
			return nil, fmt.Errorf("negative value %d", x)
		}
		return big.NewInt(int64(x)), nil
	case int64:
		if x < 0 {
			return nil, fmt.Errorf("negative value %d", x)
		}
		return big.NewInt(x), nil
	case *big.Int:
		if x.Sign() < 0 {
			return nil, fmt.Errorf("negative value %s", x)
		}
		return new(big.Int).Set(x), nil
	case string:
		base := 10
		s := x
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			base, s = 16, s[2:]
		}
		n, ok := new(big.Int).SetString(s, base)
		if !ok || n.Sign() < 0 {
			return nil, fmt.Errorf("%q is not a non-negative integer", x)
		}
		return n, nil
	}
	return nil, fmt.Errorf("want an unsigned integer, got %T", v)
}
