package signer

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/frost"
	"github.com/mishmar-io/mishmar/proposal"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// fixture is one signer's view: its key, its policy, and the dealt venue key.
type fixture struct {
	key         *agentkey.Key
	other       *agentkey.Key
	policy      Policy
	agentShare  *frost.Share
	engineShare *frost.Share
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	agent, engine, err := frost.Deal()
	if err != nil {
		t.Fatal(err)
	}
	share, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	key := agentkey.FromSeed(testSeed(1))
	return &fixture{
		key: key, other: agentkey.FromSeed(testSeed(2)), agentShare: agent, engineShare: engine,
		policy: Policy{
			KeyID: "key_a", PublicKey: key.PublicKeyB64(),
			VenueKeys: map[string]VenueKey{
				"vk_1":  {ConnectedAccountID: "rt_1", Venue: "binance", Scheme: SchemeFrostEd25519, Share: share},
				"vk_by": {ConnectedAccountID: "rt_3", Venue: "bybit", Scheme: SchemeFrostEd25519, Share: share},
			},
			Now:      func() time.Time { return now },
			Answered: func(id string) bool { return id == "sr_dup" },
		},
	}
}

// proposal is a signed limit buy of 0.5 BTCUSDT at 60000 on rt_1, unless
// mod changes it before signing.
func (f *fixture) proposal(t *testing.T, key *agentkey.Key, mod func(p *api.ProposalSubmit)) api.ProposalSubmit {
	t.Helper()
	price := "60000"
	p := api.ProposalSubmit{
		KeyId: "key_a", ConnectedAccountId: "rt_1", Kind: api.ProposalKindOrder, Symbol: "BTCUSDT", Side: api.SideBuy, Size: "0.5",
		Price: &price, OrderType: api.ProposalSubmitOrderTypeLimit, ReduceOnly: proposal.Bool(false), Nonce: "n1",
		ProposedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
	}
	if mod != nil {
		mod(&p)
	}
	s := submitOf(p)
	if err := s.Sign(key); err != nil {
		t.Fatal(err)
	}
	p.Signature = s.Signature
	return p
}

func market(p *api.ProposalSubmit) {
	p.OrderType, p.Price = api.ProposalSubmitOrderTypeMarket, nil
}

// binanceOrder builds the parameter string the engine signs for the
// proposal, the way its Binance client does (url.Values.Encode), with
// overrides and drops applied after.
func binanceOrder(p api.ProposalSubmit, clientOrderID string, override map[string]string, drop ...string) string {
	v := url.Values{
		"symbol": {strings.ToUpper(p.Symbol)}, "side": {strings.ToUpper(string(p.Side))}, "quantity": {p.Size},
		"newOrderRespType": {"RESULT"}, "newClientOrderId": {clientOrderID}, "timestamp": {"1761480000000"}, "recvWindow": {"5000"},
	}
	if p.OrderType == api.ProposalSubmitOrderTypeMarket {
		v.Set("type", "MARKET")
	} else {
		v.Set("type", "LIMIT")
		v.Set("price", *p.Price)
		v.Set("timeInForce", "GTC")
	}
	if p.ReduceOnly != nil && *p.ReduceOnly {
		v.Set("reduceOnly", "true")
	}
	for k, val := range override {
		v.Set(k, val)
	}
	for _, k := range drop {
		v.Del(k)
	}
	return v.Encode()
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// orderRequest is the request the engine makes for the proposal, with the
// message given.
func orderRequest(p api.ProposalSubmit, message string) api.SignRequest {
	return api.SignRequest{
		Id: "sr_1", SessionId: "sr_1", Round: 1, Attempt: 1, AgentId: "ag_1", ConnectedAccountId: "rt_1", VenueKeyId: "vk_1",
		Venue: api.VenueBinance, Scheme: api.SigningSchemeFrostEd25519, Purpose: api.SignRequestPurposeOrder,
		DecisionId: ptr("dec_1"), ClientOrderId: ptr("msh-ag1-abc123"), Proposal: &p,
		Order: &api.SignRequestOrder{
			Symbol: "BTCUSDT", Side: p.Side, Size: p.Size, Price: p.Price,
			OrderType: api.SignRequestOrderOrderType(p.OrderType), ReduceOnly: p.ReduceOnly != nil && *p.ReduceOnly,
		},
		Message: b64(message), VenueRequest: &api.VenueRequest{Method: "POST", Url: "/fapi/v1/order"},
		IssuedAt: now, ExpiresAt: now.Add(10 * time.Second),
	}
}

func verifyRequest(message string) api.SignRequest {
	return api.SignRequest{
		Id: "sr_v", SessionId: "sr_v", Round: 1, Attempt: 1, AgentId: "ag_1", ConnectedAccountId: "rt_1", VenueKeyId: "vk_1",
		Venue: api.VenueBinance, Scheme: api.SigningSchemeFrostEd25519, Purpose: api.SignRequestPurposeVerify,
		Message: b64(message), VenueRequest: &api.VenueRequest{Method: "GET", Url: "/fapi/v2/account"},
		IssuedAt: now, ExpiresAt: now.Add(10 * time.Second),
	}
}

const readMessage = "recvWindow=5000&timestamp=1761480000000"

// cancelRequest is the request the engine makes to pull an order, for the
// agent's cancel proposal, with the message given.
func cancelRequest(p api.ProposalSubmit, message string) api.SignRequest {
	r := orderRequest(p, message)
	r.Purpose, r.Order, r.ClientOrderId = api.SignRequestPurposeCancel, nil, nil
	r.VenueRequest = &api.VenueRequest{Method: "DELETE", Url: "/fapi/v1/order"}
	return r
}

const cancelMessage = "origClientOrderId=msh-ag1-abc123&recvWindow=5000&symbol=BTCUSDT&timestamp=1761480000000"

func TestPolicyCheck(t *testing.T) {
	f := newFixture(t)
	limit := f.proposal(t, f.key, nil)
	mkt := f.proposal(t, f.key, market)
	sell := f.proposal(t, f.key, func(p *api.ProposalSubmit) { p.Side = api.SideSell })
	reduce := f.proposal(t, f.key, func(p *api.ProposalSubmit) { p.ReduceOnly = proposal.Bool(true) })
	pull := f.proposal(t, f.key, func(p *api.ProposalSubmit) {
		p.Kind, p.TargetClientOrderId = api.ProposalKindCancel, ptr("msh-ag1-abc123")
		p.Side, p.Size, p.Price, p.OrderType, p.ReduceOnly = "", "", nil, "", nil
	})
	const cid = "msh-ag1-abc123"
	cases := []struct {
		name string
		req  api.SignRequest
		want string // refusal code; "" for accepted
	}{
		{"market order accepted", orderRequest(mkt, binanceOrder(mkt, cid, nil)), ""},
		{"limit order accepted", orderRequest(limit, binanceOrder(limit, cid, nil)), ""},
		{"floored quantity and a tick below accepted", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"quantity": "0.499", "price": "59999.9"})), ""},
		{"buy exactly 1 percent below accepted", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"price": "59400"})), ""},
		{"sell a tick above accepted", orderRequest(sell, binanceOrder(sell, cid, map[string]string{"price": "60000.1"})), ""},
		{"reduce-only accepted", orderRequest(reduce, binanceOrder(reduce, cid, nil)), ""},
		{"symbol with a suffix accepted", orderRequest(
			f.proposal(t, f.key, func(p *api.ProposalSubmit) { p.Symbol = "btc" }),
			binanceOrder(limit, cid, map[string]string{"symbol": "BTCUSDT"})), ""},
		{"wrong clientOrderId", orderRequest(limit, binanceOrder(limit, "msh-ag1-other", nil)), RefuseMessage},
		{"other side", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"side": "SELL"})), RefuseMessage},
		{"other symbol", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"symbol": "ETHUSDT"})), RefuseMessage},
		{"bigger quantity", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"quantity": "0.5001"})), RefuseMessage},
		{"zero quantity", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"quantity": "0"})), RefuseMessage},
		{"quantity not a decimal", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"quantity": "1e-3"})), RefuseMessage},
		{"buy price above the proposal", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"price": "60000.1"})), RefuseMessage},
		{"sell price below the proposal", orderRequest(sell, binanceOrder(sell, cid, map[string]string{"price": "59999.9"})), RefuseMessage},
		{"buy price more than 1 percent below", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"price": "59399.99"})), RefuseMessage},
		{"limit without a price", orderRequest(limit, binanceOrder(limit, cid, nil, "price")), RefuseMessage},
		{"market with a price", orderRequest(mkt, binanceOrder(mkt, cid, map[string]string{"price": "60000"})), RefuseMessage},
		{"market sent as limit", orderRequest(mkt, binanceOrder(mkt, cid, map[string]string{"type": "LIMIT"})), RefuseMessage},
		{"reduceOnly added", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"reduceOnly": "true"})), RefuseMessage},
		{"reduceOnly dropped", orderRequest(reduce, binanceOrder(reduce, cid, nil, "reduceOnly")), RefuseMessage},
		{"unexpected parameter", orderRequest(limit, binanceOrder(limit, cid, map[string]string{"closePosition": "true"})), RefuseMessage},
		{"parameter given twice", orderRequest(limit, binanceOrder(limit, cid, nil)+"&symbol=ETHUSDT"), RefuseMessage},
		{"empty message", orderRequest(limit, ""), RefuseMessage},
		{"message not base64", func() api.SignRequest {
			r := orderRequest(limit, binanceOrder(limit, cid, nil))
			r.Message = "not base64!"
			return r
		}(), RefuseMalformed},
		{"foreign proposal", func() api.SignRequest {
			p := f.proposal(t, f.other, func(p *api.ProposalSubmit) { p.KeyId = "key_b" })
			return orderRequest(p, binanceOrder(p, cid, nil))
		}(), RefuseForeignProposal},
		{"proposal signed by another key under this key id", func() api.SignRequest {
			p := f.proposal(t, f.other, nil)
			return orderRequest(p, binanceOrder(p, cid, nil))
		}(), RefuseBadSignature},
		{"proposal changed after signing", func() api.SignRequest {
			p := limit
			p.Size = "5"
			return orderRequest(p, binanceOrder(p, cid, nil))
		}(), RefuseBadSignature},
		{"proposal for another connected account", func() api.SignRequest {
			p := f.proposal(t, f.key, func(p *api.ProposalSubmit) { p.ConnectedAccountId = "rt_2" })
			return orderRequest(p, binanceOrder(p, cid, nil))
		}(), RefuseConnectedAccountMismatch},
		{"proposal expired", func() api.SignRequest {
			p := f.proposal(t, f.key, func(p *api.ProposalSubmit) { p.ProposedAt, p.ExpiresAt = now.Add(-time.Hour), now.Add(-time.Second) })
			return orderRequest(p, binanceOrder(p, cid, nil))
		}(), RefuseProposalExpired},
		{"no proposal", func() api.SignRequest {
			r := orderRequest(limit, binanceOrder(limit, cid, nil))
			r.Proposal = nil
			return r
		}(), RefuseNoProposal},
		{"expired request", func() api.SignRequest {
			r := orderRequest(limit, binanceOrder(limit, cid, nil))
			r.ExpiresAt = now
			return r
		}(), RefuseExpired},
		{"unknown venue key", func() api.SignRequest {
			r := orderRequest(limit, binanceOrder(limit, cid, nil))
			r.VenueKeyId = "vk_9"
			return r
		}(), RefuseUnknownKey},
		{"venue key on another connected account", func() api.SignRequest {
			r := orderRequest(limit, binanceOrder(limit, cid, nil))
			r.ConnectedAccountId = "rt_2"
			return r
		}(), RefuseKeyMismatch},
		{"wrong scheme", func() api.SignRequest {
			r := orderRequest(limit, binanceOrder(limit, cid, nil))
			r.Scheme = api.SigningSchemeRsa2p
			return r
		}(), RefuseScheme},
		{"cancel accepted", cancelRequest(pull, cancelMessage), ""},
		{"cancel of another order", cancelRequest(pull, strings.Replace(cancelMessage, "abc123", "zzz999", 1)), RefuseMessage},
		{"cancel of another symbol", cancelRequest(pull, strings.Replace(cancelMessage, "BTCUSDT", "ETHUSDT", 1)), RefuseMessage},
		{"cancel by venue order id", cancelRequest(pull, cancelMessage+"&orderId=1"), RefuseMessage},
		{"cancel carrying an order proposal", cancelRequest(limit, cancelMessage), RefuseMessage},
		{"cancel as a POST", func() api.SignRequest {
			r := cancelRequest(pull, cancelMessage)
			r.VenueRequest.Method = "POST"
			return r
		}(), RefuseMessage},
		{"order request carrying a cancel", orderRequest(pull, cancelMessage), RefuseMessage},
		{"duplicate request id", func() api.SignRequest {
			r := orderRequest(limit, binanceOrder(limit, cid, nil))
			r.Id = "sr_dup"
			return r
		}(), RefuseReplay},
		{"no engine commitment is the signer's problem, not the check's", func() api.SignRequest {
			r := orderRequest(limit, binanceOrder(limit, cid, nil))
			r.Engine = nil
			return r
		}(), ""},
		{"verify accepted", verifyRequest(readMessage), ""},
		{"verify with an order inside", verifyRequest(binanceOrder(limit, cid, nil)), RefuseMessage},
		{"verify with a symbol", verifyRequest("recvWindow=5000&symbol=BTCUSDT&timestamp=1761480000000"), RefuseMessage},
		{"verify as a POST", func() api.SignRequest {
			r := verifyRequest(readMessage)
			r.VenueRequest = &api.VenueRequest{Method: "POST", Url: "/fapi/v1/order"}
			return r
		}(), RefuseMessage},
		{"unsupported venue", func() api.SignRequest {
			r := verifyRequest(readMessage)
			r.Venue, r.ConnectedAccountId, r.VenueKeyId = api.VenueBybit, "rt_3", "vk_by"
			return r
		}(), RefuseVenue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message, got := f.policy.Check(tc.req)
			switch {
			case tc.want == "" && got != nil:
				t.Fatalf("refused: %s", got.Reason())
			case tc.want == "" && string(message) != mustDecode(t, tc.req.Message):
				t.Fatalf("accepted with message %q, want the request's", message)
			case tc.want != "" && got == nil:
				t.Fatalf("accepted, want %s", tc.want)
			case tc.want != "" && got.Code != tc.want:
				t.Fatalf("refused as %s, want %s", got.Reason(), tc.want)
			}
		})
	}
}

func mustDecode(t *testing.T, s string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRefusalReason(t *testing.T) {
	r := &Refusal{RefuseMessage, "side \"SELL\" is not the proposed BUY"}
	if r.Reason() != "message_mismatch: side \"SELL\" is not the proposed BUY" {
		t.Fatal(r.Reason())
	}
	if (&Refusal{Code: RefuseExpired}).Reason() != RefuseExpired {
		t.Fatal("a refusal without detail should be its code alone")
	}
	if !strings.HasPrefix(r.Error(), "signer: refused: ") {
		t.Fatal(r.Error())
	}
}
