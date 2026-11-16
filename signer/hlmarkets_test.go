package signer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/agentkey"
	"github.com/mishmar-io/mishmar/api"
	"github.com/mishmar-io/mishmar/proposal"
	"github.com/mishmar-io/mishmar/venues/hyperliquid"
)

// hlVenue stands in for Hyperliquid's info endpoint. It lists BTC (0),
// ETH (1), OLD (2, delisted) and SOL (3) until a test changes what it answers.
type hlVenue struct {
	mu     sync.Mutex
	status int
	body   string
	hits   int
	asked  []string
	srv    *httptest.Server
}

const hlTestList = `{"universe":[{"name":"BTC","szDecimals":5},{"name":"ETH","szDecimals":4},{"name":"OLD","szDecimals":1,"isDelisted":true},{"name":"SOL","szDecimals":2}]}`

func newHLVenue(t *testing.T) *hlVenue {
	t.Helper()
	v := &hlVenue{status: http.StatusOK, body: hlTestList}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked, _ := io.ReadAll(r.Body)
		v.mu.Lock()
		v.hits++
		v.asked = append(v.asked, r.Method+" "+r.URL.Path+" "+string(asked))
		status, body := v.status, v.body
		v.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(v.srv.Close)
	return v
}

func (v *hlVenue) answer(status int, body string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.status, v.body = status, body
}

func (v *hlVenue) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.hits
}

// hlFixture is a signer with one Hyperliquid testnet key on rt_hl whose
// market list comes from the stand-in venue, and a clock the test moves.
type hlFixture struct {
	s     *Signer
	key   *agentkey.Key
	venue *hlVenue
	clock time.Time
}

func newHLFixture(t *testing.T) *hlFixture {
	t.Helper()
	f := &hlFixture{key: agentkey.FromSeed(testSeed(1)), venue: newHLVenue(t), clock: now}
	f.s = &Signer{
		Config: &Config{KeyID: "key_a", AgentID: "agt_1", VenueKeys: map[string]VenueKey{
			"vk_hl": {ConnectedAccountID: "rt_hl", Venue: "hyperliquid", Scheme: SchemeECDSA2p, Network: "testnet"},
		}},
		Logger: slog.New(slog.DiscardHandler),
		Now:    func() time.Time { return f.clock },
		pub:    f.key.PublicKeyB64(),
	}
	f.s.answered = newIDSet(8)
	f.s.HyperliquidInfoURL = func(hyperliquid.Network) string { return f.venue.srv.URL + "/info" }
	return f
}

// read reads the list as Handle does before it checks a request.
func (f *hlFixture) read(t *testing.T) error {
	t.Helper()
	return f.s.refreshHLMarkets(context.Background(), hyperliquid.Testnet)
}

// proposal is the agent's signed limit buy of 0.5 ETH at 3000 on rt_hl.
func (f *hlFixture) proposal(t *testing.T, mod func(p *api.ProposalSubmit)) api.ProposalSubmit {
	t.Helper()
	price := "3000"
	p := api.ProposalSubmit{
		KeyId: "key_a", ConnectedAccountId: "rt_hl", Kind: api.ProposalKindOrder, Symbol: "ETH", Side: api.SideBuy, Size: "0.5",
		Price: &price, OrderType: api.ProposalSubmitOrderTypeLimit, ReduceOnly: proposal.Bool(false), Nonce: "n1",
		ProposedAt: f.clock.Add(-time.Second), ExpiresAt: f.clock.Add(time.Minute),
	}
	if mod != nil {
		mod(&p)
	}
	s := submitOf(p)
	if err := s.Sign(f.key); err != nil {
		t.Fatal(err)
	}
	p.Signature = s.Signature
	return p
}

func hlReduceOnly(p *api.ProposalSubmit) { p.ReduceOnly = proposal.Bool(true) }

func hlCancel(p *api.ProposalSubmit) {
	p.Kind, p.TargetClientOrderId = api.ProposalKindCancel, ptr("agt_1-target")
	p.Side, p.Size, p.Price, p.OrderType, p.ReduceOnly = "", "", nil, "", nil
}

// request is the sign request an engine sends for the proposal: an order
// (or the cancel of its target) built on the asset index the engine chose.
func (f *hlFixture) request(t *testing.T, p api.ProposalSubmit, index int, mod func(in *hyperliquid.Intent)) api.SignRequest {
	t.Helper()
	nonce := hyperliquid.Nonce(f.clock)
	req := api.SignRequest{
		Id: "sr_1", AgentId: "agt_1", ConnectedAccountId: "rt_hl", VenueKeyId: "vk_hl", Venue: api.VenueHyperliquid,
		Scheme: api.SigningSchemeEcdsa2p, Proposal: &p, IssuedAt: f.clock, ExpiresAt: f.clock.Add(time.Minute),
	}
	var message []byte
	var err error
	if p.Kind == api.ProposalKindCancel {
		req.Purpose = api.SignRequestPurposeCancel
		message, err = hyperliquid.BuildCancel(index, *p.TargetClientOrderId, nonce)
	} else {
		req.Purpose, req.ClientOrderId = api.SignRequestPurposeOrder, ptr("agt_1-order")
		in := hlIntentOf(p)
		if mod != nil {
			mod(&in)
		}
		message, err = hyperliquid.BuildOrder(in, hyperliquid.Rules{AssetIndex: index, SizeDecimals: 4}, "agt_1-order", nonce)
	}
	if err != nil {
		t.Fatal(err)
	}
	_, action, err := hyperliquid.Decode(message)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hyperliquid.Digest(action, nonce, hyperliquid.Testnet)
	if err != nil {
		t.Fatal(err)
	}
	req.Message = base64.StdEncoding.EncodeToString(digest)
	req.VenueRequest = &api.VenueRequest{Method: http.MethodPost, Url: "/exchange", Body: ptr(base64.StdEncoding.EncodeToString(message))}
	return req
}

func (f *hlFixture) check(req api.SignRequest) *Refusal {
	_, r := f.s.Policy().Check(req)
	return r
}

func wantHLRefusal(t *testing.T, r *Refusal, code, detail string) {
	t.Helper()
	switch {
	case r == nil:
		t.Fatalf("signed; want a refusal with code %s", code)
	case r.Code != code:
		t.Fatalf("refused with %s; want code %s", r.Reason(), code)
	case !strings.Contains(r.Detail, detail):
		t.Fatalf("refused with %q; want the detail to say %q", r.Detail, detail)
	}
}

// The asset index is the venue's for the proposal's coin. An order the
// engine built on another index is refused, whichever index that is.
func TestHyperliquidOrderMustBeOnTheVenuesIndexForTheCoin(t *testing.T) {
	f := newHLFixture(t)
	if err := f.read(t); err != nil {
		t.Fatal(err)
	}
	if r := f.check(f.request(t, f.proposal(t, nil), 1, nil)); r != nil {
		t.Fatalf("ETH on the venue's index 1: %s", r.Reason())
	}
	for _, index := range []int{0, 3, 2, 4, 10000} {
		wantHLRefusal(t, f.check(f.request(t, f.proposal(t, nil), index, nil)), RefuseMessage, "is not ETH's 1")
	}
	// A cancel is held to the same index.
	if r := f.check(f.request(t, f.proposal(t, hlCancel), 1, nil)); r != nil {
		t.Fatalf("cancel on index 1: %s", r.Reason())
	}
	wantHLRefusal(t, f.check(f.request(t, f.proposal(t, hlCancel), 3, nil)), RefuseMessage, "is not the target's 1")
}

// A coin that is not in the venue's list of perps has no index to sign for.
func TestHyperliquidCoinMustBeInTheVenuesList(t *testing.T) {
	f := newHLFixture(t)
	if err := f.read(t); err != nil {
		t.Fatal(err)
	}
	for _, symbol := range []string{"DOGE", "ETHUSDT", "@1", "PURR/USDC"} {
		p := f.proposal(t, func(p *api.ProposalSubmit) { p.Symbol = symbol })
		wantHLRefusal(t, f.check(f.request(t, p, 1, nil)), RefuseMalformed, "is not in Hyperliquid's list of perp markets")
	}
	// Two coins that differ only in case, named in a third spelling.
	f.venue.answer(http.StatusOK, `{"universe":[{"name":"kPEPE","szDecimals":4},{"name":"KPEPE","szDecimals":4}]}`)
	f.clock = f.clock.Add(HLListFresh)
	if err := f.read(t); err != nil {
		t.Fatal(err)
	}
	p := f.proposal(t, func(p *api.ProposalSubmit) { p.Symbol = "kpepe" })
	wantHLRefusal(t, f.check(f.request(t, p, 0, nil)), RefuseMalformed, "differ only in letter case")
	p = f.proposal(t, func(p *api.ProposalSubmit) { p.Symbol = "KPEPE" })
	if r := f.check(f.request(t, p, 1, nil)); r != nil {
		t.Fatalf("KPEPE named exactly: %s", r.Reason())
	}
}

// What is signed depends on how old the venue's last answer is: fresh,
// everything; old, only reduce-only orders and cancels; none, nothing.
func TestHyperliquidListAgeDecidesWhatIsSigned(t *testing.T) {
	type want struct{ order, reduceOnly, cancel string }
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *hlFixture)
		want  want
	}{
		{"read just now", func(t *testing.T, f *hlFixture) {
			if err := f.read(t); err != nil {
				t.Fatal(err)
			}
		}, want{}},
		{"read a second under the limit ago", func(t *testing.T, f *hlFixture) {
			if err := f.read(t); err != nil {
				t.Fatal(err)
			}
			f.clock = f.clock.Add(HLListFresh - time.Second)
		}, want{}},
		{"read at the limit, venue down since", func(t *testing.T, f *hlFixture) {
			if err := f.read(t); err != nil {
				t.Fatal(err)
			}
			f.venue.answer(http.StatusTooManyRequests, "rate limited")
			f.clock = f.clock.Add(HLListFresh)
			if err := f.read(t); err == nil {
				t.Fatal("the read of a venue that answers 429 passed")
			}
		}, want{order: RefuseVenueList}},
		{"read two days ago, venue down since", func(t *testing.T, f *hlFixture) {
			if err := f.read(t); err != nil {
				t.Fatal(err)
			}
			f.venue.srv.Close()
			f.clock = f.clock.Add(48 * time.Hour)
			if err := f.read(t); err == nil {
				t.Fatal("the read of a closed venue passed")
			}
		}, want{order: RefuseVenueList}},
		{"old and nobody tried to read again", func(t *testing.T, f *hlFixture) {
			if err := f.read(t); err != nil {
				t.Fatal(err)
			}
			f.clock = f.clock.Add(HLListFresh + time.Minute)
		}, want{order: RefuseVenueList}},
		{"never read, venue down", func(t *testing.T, f *hlFixture) {
			f.venue.answer(http.StatusBadGateway, "")
			if err := f.read(t); err == nil {
				t.Fatal("the read of a venue that answers 502 passed")
			}
		}, want{RefuseVenueList, RefuseVenueList, RefuseVenueList}},
		{"never read, nobody tried", func(*testing.T, *hlFixture) {}, want{RefuseVenueList, RefuseVenueList, RefuseVenueList}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHLFixture(t)
			tc.setup(t, f)
			for _, c := range []struct {
				what string
				mod  func(*api.ProposalSubmit)
				code string
			}{{"order", nil, tc.want.order}, {"reduce-only order", hlReduceOnly, tc.want.reduceOnly}, {"cancel", hlCancel, tc.want.cancel}} {
				r := f.check(f.request(t, f.proposal(t, c.mod), 1, nil))
				switch {
				case c.code == "" && r != nil:
					t.Errorf("%s: refused with %s; want it signed", c.what, r.Reason())
				case c.code != "" && r == nil:
					t.Errorf("%s: signed; want %s", c.what, c.code)
				case c.code != "" && r.Code != c.code:
					t.Errorf("%s: refused with %s; want %s", c.what, r.Reason(), c.code)
				}
			}
		})
	}
}

// With an old list the checks still run against it: a reduce-only order or
// a cancel on another index is refused, and reduce-only is the order's own
// flag held to the proposal's, not the engine's word.
func TestHyperliquidOldListStillChecksWhatItSigns(t *testing.T) {
	f := newHLFixture(t)
	if err := f.read(t); err != nil {
		t.Fatal(err)
	}
	f.venue.srv.Close()
	f.clock = f.clock.Add(time.Hour)
	_ = f.read(t)

	wantHLRefusal(t, f.check(f.request(t, f.proposal(t, hlReduceOnly), 3, nil)), RefuseMessage, "is not ETH's 1")
	wantHLRefusal(t, f.check(f.request(t, f.proposal(t, hlCancel), 3, nil)), RefuseMessage, "is not the target's 1")

	// The proposal is not reduce-only; the engine's order says it is.
	req := f.request(t, f.proposal(t, nil), 1, func(in *hyperliquid.Intent) { in.ReduceOnly = true })
	req.Order = &api.SignRequestOrder{Symbol: "ETH", ReduceOnly: true}
	wantHLRefusal(t, f.check(req), RefuseMessage, "reduceOnly is true, the proposal says false")

	// The proposal is reduce-only; the engine's order is not.
	req = f.request(t, f.proposal(t, hlReduceOnly), 1, func(in *hyperliquid.Intent) { in.ReduceOnly = false })
	wantHLRefusal(t, f.check(req), RefuseMessage, "reduceOnly is false, the proposal says true")

	// The refusal of a new order says since when and what still works.
	r := f.check(f.request(t, f.proposal(t, nil), 1, nil))
	wantHLRefusal(t, r, RefuseVenueList, "since 12:00 UTC on 26 October; until it can, only reduce-only orders and cancels are signed")
}

// A coin the venue marks delisted takes no new order; closing one is allowed.
func TestHyperliquidDelistedCoin(t *testing.T) {
	f := newHLFixture(t)
	if err := f.read(t); err != nil {
		t.Fatal(err)
	}
	old := func(p *api.ProposalSubmit) { p.Symbol = "OLD" }
	wantHLRefusal(t, f.check(f.request(t, f.proposal(t, old), 2, nil)), RefuseMalformed, "OLD is delisted on Hyperliquid")
	if r := f.check(f.request(t, f.proposal(t, func(p *api.ProposalSubmit) { old(p); hlReduceOnly(p) }), 2, nil)); r != nil {
		t.Errorf("reduce-only on a delisted coin: %s", r.Reason())
	}
	if r := f.check(f.request(t, f.proposal(t, func(p *api.ProposalSubmit) { old(p); hlCancel(p) }), 2, nil)); r != nil {
		t.Errorf("cancel on a delisted coin: %s", r.Reason())
	}
}

// The list is asked for once per HLListFresh, a failed read is not repeated
// for hlListRetry and leaves the last answer in place, and an answer that is
// not the list is not taken.
func TestHyperliquidListIsReadFromTheVenueAndKept(t *testing.T) {
	f := newHLFixture(t)
	if err := f.read(t); err != nil {
		t.Fatal(err)
	}
	if got := f.venue.asked[0]; got != `POST /info {"type":"meta"}` {
		t.Fatalf("the venue was asked %q", got)
	}
	f.clock = f.clock.Add(HLListFresh - time.Second)
	if err := f.read(t); err != nil || f.venue.count() != 1 {
		t.Fatalf("a fresh list was read again: %v, %d reads", err, f.venue.count())
	}

	// Old now. Each bad answer is refused and the first list stays.
	f.clock = f.clock.Add(time.Second)
	reads := 1
	for name, bad := range map[string]struct {
		status int
		body   string
	}{
		"an error status":     {http.StatusInternalServerError, hlTestList},
		"not JSON":            {http.StatusOK, "<html>"},
		"an empty list":       {http.StatusOK, `{"universe":[]}`},
		"larger than the cap": {http.StatusOK, `{"universe":[{"name":"BTC","szDecimals":5}],"pad":"` + strings.Repeat("x", hlListMaxBytes) + `"}`},
	} {
		f.venue.answer(bad.status, bad.body)
		if err := f.read(t); err == nil {
			t.Fatalf("%s: the read passed", name)
		}
		reads++
		if f.venue.count() != reads {
			t.Fatalf("%s: %d reads, want %d", name, f.venue.count(), reads)
		}
		// Asked again at once: the venue is left alone, the error is the same.
		if err := f.read(t); err == nil || f.venue.count() != reads {
			t.Fatalf("%s: read again within the wait: %v, %d reads", name, err, f.venue.count())
		}
		m, err := f.s.hlMarket(hyperliquid.Testnet, "SOL")
		if err != nil || m.Rules.AssetIndex != 3 || !m.ReadAt.Equal(now) {
			t.Fatalf("%s: the last good list was not kept: %+v, %v", name, m, err)
		}
		f.clock = f.clock.Add(hlListRetry)
	}

	// The venue is back with a longer list.
	f.venue.answer(http.StatusOK, `{"universe":[{"name":"BTC","szDecimals":5},{"name":"ETH","szDecimals":4},{"name":"OLD","szDecimals":1,"isDelisted":true},{"name":"SOL","szDecimals":2},{"name":"NEW","szDecimals":0}]}`)
	if err := f.read(t); err != nil {
		t.Fatal(err)
	}
	m, err := f.s.hlMarket(hyperliquid.Testnet, "NEW")
	if err != nil || m.Rules.AssetIndex != 4 || !m.ReadAt.Equal(f.clock) {
		t.Fatalf("the new list: %+v, %v", m, err)
	}
	// Mainnet has its own list, and none was read.
	if _, err := f.s.hlMarket(hyperliquid.Mainnet, "ETH"); err != ErrHLListUnread {
		t.Fatalf("mainnet: %v; want ErrHLListUnread", err)
	}
}

// A redirect is not followed: the answer comes from the host asked or not at all.
func TestHyperliquidListReadDoesNotFollowARedirect(t *testing.T) {
	f := newHLFixture(t)
	elsewhere := newHLVenue(t)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.srv.URL+"/info", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	f.s.HyperliquidInfoURL = func(hyperliquid.Network) string { return redirect.URL + "/info" }
	if err := f.read(t); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("read through a redirect: %v", err)
	}
	if elsewhere.count() != 0 {
		t.Fatalf("the redirect was followed: %d reads at the other host", elsewhere.count())
	}
	if _, err := f.s.hlMarket(hyperliquid.Testnet, "ETH"); err != ErrHLListUnread {
		t.Fatalf("a list was taken: %v", err)
	}
}

// Each network's key reads that network's list.
func TestHyperliquidListIsReadForTheKeysNetwork(t *testing.T) {
	f := newHLFixture(t)
	f.s.Config.VenueKeys["vk_main"] = VenueKey{ConnectedAccountID: "rt_main", Venue: "hyperliquid", Scheme: SchemeECDSA2p, Network: "mainnet"}
	var asked []hyperliquid.Network
	f.s.HyperliquidInfoURL = func(n hyperliquid.Network) string {
		asked = append(asked, n)
		return f.venue.srv.URL + "/info"
	}
	p := f.proposal(t, nil)
	f.s.Handle(context.Background(), api.SignRequest{Id: "sr_t", Venue: api.VenueHyperliquid, VenueKeyId: "vk_hl", Proposal: &p})
	f.s.Handle(context.Background(), api.SignRequest{Id: "sr_m", Venue: api.VenueHyperliquid, VenueKeyId: "vk_main", Proposal: &p})
	if len(asked) != 2 || asked[0] != hyperliquid.Testnet || asked[1] != hyperliquid.Mainnet {
		t.Fatalf("networks asked: %v", asked)
	}
}

// Handle reads the list itself before it checks, and refuses on the venue's
// index when the engine's order names another.
func TestHandleRefusesAnOrderOnAnotherIndex(t *testing.T) {
	f := newHLFixture(t)
	answer := f.s.Handle(context.Background(), f.request(t, f.proposal(t, nil), 3, nil))
	if answer.Refusal == nil || !strings.Contains(answer.Refusal.Reason, RefuseMessage+": asset 3 is not ETH's 1") {
		t.Fatalf("answer: %+v", answer)
	}
	if f.venue.count() != 1 {
		t.Fatalf("%d reads of the venue's list, want 1", f.venue.count())
	}
}

// A one-step order is built on the venue's index for the coin, and the
// engine is not asked for the coin's rules: this signer has no engine to ask.
func TestOneStepHyperliquidOrderIsBuiltOnTheVenuesIndex(t *testing.T) {
	f := newHLFixture(t)
	seen := filepath.Join(t.TempDir(), "request.json")
	f.s.Config.Hook = "cat > " + seen
	vk := f.s.Config.VenueKeys["vk_hl"]

	price := "3000"
	p := api.ProposalSubmit{
		KeyId: "key_a", ConnectedAccountId: "rt_hl", Kind: api.ProposalKindOrder, Symbol: "SOL", Side: api.SideBuy, Size: "1.5",
		Price: &price, OrderType: api.ProposalSubmitOrderTypeLimit, Nonce: "n1", ProposedAt: f.clock, ExpiresAt: f.clock.Add(time.Minute),
	}
	// The key has no share here, so the build stops after the hook saw the request.
	if err := f.s.prepareHyperliquid(context.Background(), &p, "vk_hl", vk); err == nil {
		t.Fatal("prepared with no share")
	}
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the hook saw no request: %v", err)
	}
	var req api.SignRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	body, err := base64.StdEncoding.DecodeString(*req.VenueRequest.Body)
	if err != nil {
		t.Fatal(err)
	}
	_, action, err := hyperliquid.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	o, err := hyperliquid.DecodeOrder(action)
	if err != nil {
		t.Fatal(err)
	}
	if o.Asset != 3 || o.Size != "1.5" {
		t.Fatalf("built %+v; want SOL's index 3 and size 1.5 from the venue's list", o)
	}

	// The venue cannot be reached and the list is old: a new order is
	// refused before anything is built, a reduce-only order is still built.
	f.venue.srv.Close()
	f.clock = f.clock.Add(time.Hour)
	_ = os.Remove(seen)
	p.ProposedAt, p.ExpiresAt = f.clock, f.clock.Add(time.Minute)
	err = f.s.prepareHyperliquid(context.Background(), &p, "vk_hl", vk)
	r, ok := err.(*Refusal)
	if !ok || r.Code != RefuseVenueList {
		t.Fatalf("a new order on an old list: %v; want %s", err, RefuseVenueList)
	}
	if _, err := os.Stat(seen); err == nil {
		t.Fatal("an order was built on an old list")
	}
	p.ReduceOnly = proposal.Bool(true)
	if err := f.s.prepareHyperliquid(context.Background(), &p, "vk_hl", vk); err == nil {
		t.Fatal("prepared with no share")
	} else if _, refused := err.(*Refusal); refused {
		t.Fatalf("a reduce-only order on an old list was refused: %v", err)
	}
	if _, err := os.Stat(seen); err != nil {
		t.Fatal("the reduce-only order was not built")
	}

	// A signer that never read the list builds nothing.
	g := newHLFixture(t)
	g.venue.srv.Close()
	err = g.s.prepareHyperliquid(context.Background(), &p, "vk_hl", vk)
	if r, ok := err.(*Refusal); !ok || r.Code != RefuseVenueList {
		t.Fatalf("never read: %v; want %s", err, RefuseVenueList)
	}
}

// setsHLAddress matches a line that assigns Signer.HyperliquidInfoURL.
var setsHLAddress = regexp.MustCompile(`HyperliquidInfoURL\s*(=[^=]|:)`)

// Nothing a program is started with can move the address: the only way to
// set it is the field these tests use, and no program in this repository
// sets that field.
func TestNoProgramSetsTheHyperliquidAddress(t *testing.T) {
	for _, dir := range []string{"../cmd", "../mcp", "../signer"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if setsHLAddress.MatchString(line) {
					t.Errorf("%s:%d sets the address: %s", path, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
