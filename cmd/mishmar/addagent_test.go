package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mishmar-io/mishmar/api"
)

// An account Mishmar only reads is refused before anything is created, with
// the engine's reason quoted.
func TestAddAgentRefusesAnAccountThatCannotBeTraded(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	no, reason := false, "Mishmar does not trade spot on Binance yet"
	label := "UID …7868"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/v1/connected-accounts/ta_5" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(api.ConnectedAccount{Id: "ta_5", VenueConnectionId: "bn_carry", Name: "Spot", Type: api.ConnectedAccountTypeSpot, VenueRef: "spot",
			Venue: api.VenueBinance, Status: api.ConnectedAccountStatusActive, Tradable: &no, UntradableReason: &reason,
			Connection: api.VenueConnectionRef{Id: "bn_carry", Name: "Carry", Kind: api.VenueConnectionKindExchange, AccountCount: 2,
				VenueAccount: api.VenueAccount{Role: api.VenueAccountRoleMain, Label: &label}}})
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "agent.json")
	err := addAgent(context.Background(), addAgentOptions{engine: srv.URL + "/v1", token: "tok", name: "Carry bot", config: path, connectedAccounts: []string{"ta_5"}}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ta_5 (Binance · Carry · Spot · UID …7868) cannot be traded: "+reason) {
		t.Fatalf("refusal: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a config was written: %v", err)
	}
	if strings.Join(seen, ",") != "GET /v1/connected-accounts/ta_5" {
		t.Fatalf("calls: %v", seen)
	}
}
