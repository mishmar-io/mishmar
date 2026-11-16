package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mishmar-io/mishmar/api"
)

// proposeWith runs "mishmar propose" with --key against an engine that
// answers every proposal with d, and returns what the command printed.
func proposeWith(t *testing.T, d api.Decision, args ...string) string {
	t.Helper()
	const keyID = "key_main_abcdefgh2345"
	secret, _ := newAPIKey(t, "self", keyID)
	t.Setenv("MISHMAR_AGENT_SECRET", secret)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/proposals" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d)
	}))
	t.Cleanup(srv.Close)
	out, err := execute(context.Background(), t, strings.NewReader(""), 10*time.Second,
		append([]string{"propose", "--engine", srv.URL + "/v1", "--key", keyID, "--connected-account", "ta_1", "--symbol", "BTC"}, args...)...)
	if err != nil {
		t.Fatalf("propose: %v\n%s", err, out)
	}
	return out
}

var proposeAckAt = time.Date(2026, 11, 16, 14, 22, 13, 0, time.UTC)

// A cancel's acknowledgement carries no venue order id: the line names the
// cancelled order by its clientOrderId, with no gap where an id would be.
func TestProposeCancelLineHasNoGap(t *testing.T) {
	target := "msh-agtcancel-0001"
	out := proposeWith(t, api.Decision{
		Id: "dec_cancel", Verdict: api.VerdictAllow,
		Proposal: api.Proposal{Kind: api.ProposalKindCancel, Venue: api.VenueHyperliquid, Symbol: "BTC", Side: api.SideBuy, Size: "0.001", NotionalUsd: "0", ClientOrderId: "msh-agtcancel-0002", TargetClientOrderId: &target},
		VenueAck: &api.VenueAck{Status: "cancelled", ReceivedAt: proposeAckAt},
	}, "--cancel", target)
	if strings.Contains(out, "order  at") {
		t.Fatalf("a hole where the order id would be:\n%s", out)
	}
	if want := "  venue: cancelled order " + target + " at 2026-11-16T14:22:13Z\n"; !strings.Contains(out, want) {
		t.Fatalf("want %q in:\n%s", want, out)
	}
	if strings.Contains(out, "pass it to --cancel") {
		t.Fatalf("a cancel's own clientOrderId offered to --cancel:\n%s", out)
	}
}

// An acknowledgement with no order id, for an order, prints no "order" at all.
func TestProposeVenueLineWithoutAnOrderID(t *testing.T) {
	out := proposeWith(t, api.Decision{
		Id: "dec_order", Verdict: api.VerdictAllow,
		Proposal: api.Proposal{Kind: api.ProposalKindOrder, Venue: api.VenueBinance, Symbol: "BTCUSDT", Side: api.SideBuy, Size: "0.001", NotionalUsd: "60", ClientOrderId: "msh-agtorder-0001"},
		VenueAck: &api.VenueAck{Status: "new", ReceivedAt: proposeAckAt},
	}, "--size", "0.001", "--price", "60000")
	if want := "  venue: new at 2026-11-16T14:22:13Z\n"; !strings.Contains(out, want) {
		t.Fatalf("want %q in:\n%s", want, out)
	}
}

// An order that reached the venue, or waits for a person, prints the
// clientOrderId --cancel takes.
func TestProposeOrderPrintsItsClientOrderID(t *testing.T) {
	const id = "msh-agtorder-0001"
	approval := "apr_1"
	for name, d := range map[string]api.Decision{
		"forwarded": {
			Id: "dec_order", Verdict: api.VerdictAllow,
			Proposal: api.Proposal{Kind: api.ProposalKindOrder, Venue: api.VenueHyperliquid, Symbol: "BTC", Side: api.SideBuy, Size: "0.001", NotionalUsd: "60", ClientOrderId: id},
			VenueAck: &api.VenueAck{Status: "resting", OrderId: "123456", ReceivedAt: proposeAckAt},
		},
		"parked": {
			Id: "dec_order", Verdict: api.VerdictRequireApproval, ApprovalId: &approval,
			Proposal: api.Proposal{Kind: api.ProposalKindOrder, Venue: api.VenueHyperliquid, Symbol: "BTC", Side: api.SideBuy, Size: "0.001", NotionalUsd: "60", ClientOrderId: id},
		},
	} {
		out := proposeWith(t, d, "--size", "0.001", "--price", "60000")
		if want := "  clientOrderId: " + id + "  (pass it to --cancel)\n"; !strings.Contains(out, want) {
			t.Fatalf("%s: want %q in:\n%s", name, want, out)
		}
		if name == "forwarded" && !strings.Contains(out, "  venue: resting order 123456 at 2026-11-16T14:22:13Z\n") {
			t.Fatalf("%s: the venue line changed:\n%s", name, out)
		}
	}

	// A blocked order rests nowhere: nothing to cancel.
	out := proposeWith(t, api.Decision{
		Id: "dec_order", Verdict: api.VerdictBlock,
		Proposal: api.Proposal{Kind: api.ProposalKindOrder, Venue: api.VenueHyperliquid, Symbol: "BTC", Side: api.SideBuy, Size: "0.001", NotionalUsd: "60", ClientOrderId: id},
	}, "--size", "0.001", "--price", "60000")
	if strings.Contains(out, "clientOrderId:") {
		t.Fatalf("a blocked order offered to --cancel:\n%s", out)
	}
}

// The help says which venues go one-shot.
func TestProposeHelpNamesBothOneShotVenues(t *testing.T) {
	long := newProposeCmd().Long
	for _, want := range []string{"Binance", "Hyperliquid", "one-shot"} {
		if !strings.Contains(long, want) {
			t.Fatalf("propose --help does not say %q:\n%s", want, long)
		}
	}
}
