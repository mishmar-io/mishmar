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

// --capital goes on the new agent (contract 0.21.0: capital is the agent's,
// what its own leverage and drawdown rules are measured against), and a
// capital that is not a decimal is refused before anything is called.
func TestAddAgentGivesTheAgentCapital(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var created api.AgentCreate
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/connected-accounts/ta_bn":
			_ = json.NewEncoder(w).Encode(api.ConnectedAccount{Id: "ta_bn", VenueConnectionId: "bn_main", Name: "USDⓈ-M Futures", Type: api.ConnectedAccountTypeDerivatives,
				VenueRef: "usdm-futures", Venue: api.VenueBinance, Status: api.ConnectedAccountStatusActive,
				Connection: api.VenueConnectionRef{Id: "bn_main", Name: "Main", Kind: api.VenueConnectionKindExchange, AccountCount: 2, VenueAccount: api.VenueAccount{Role: api.VenueAccountRoleMain}}})
		case "POST /v1/agents":
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(api.Agent{Id: "agt_1", Name: created.Name, Permission: created.Permission, CapitalUsd: created.CapitalUsd, Status: api.AgentStatusActive})
		default:
			w.WriteHeader(http.StatusInternalServerError) // the setup stops after the agent
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	o := addAgentOptions{engine: srv.URL + "/v1", token: "tok", name: "Reversion", capital: "2000", config: filepath.Join(dir, "agent.json"), connectedAccounts: []string{"ta_bn"}}
	var out strings.Builder
	err := addAgent(context.Background(), o, &out)
	if err == nil || !strings.Contains(err.Error(), "agent agt_1 was created but no key was issued") {
		t.Fatalf("after the agent: %v", err)
	}
	if created.CapitalUsd == nil || *created.CapitalUsd != "2000" || created.Permission != api.AgentPermissionTrade {
		t.Fatalf("created %+v", created)
	}
	if !strings.Contains(out.String(), "agent agt_1 (Reversion) created, with capital 2000 USD\n") {
		t.Fatalf("output: %q", out.String())
	}

	seen = nil
	for _, bad := range []string{"-5", "0", "0.00", "2e3", "$2000", "2,000"} {
		o.capital, o.config = bad, filepath.Join(dir, "other.json")
		if err := addAgent(context.Background(), o, io.Discard); err == nil || !strings.Contains(err.Error(), "--capital") {
			t.Fatalf("capital %q: %v", bad, err)
		}
	}
	if len(seen) != 0 {
		t.Fatalf("a refused capital called the engine: %v", seen)
	}
}
