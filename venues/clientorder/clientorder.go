// Package clientorder is the shape of the client order ids Mishmar puts on
// venue orders: msh-<agent id>-<6 random characters>, so a fill the venue
// reports can be traced to the agent and the decision that placed the order.
// Each venue package builds and checks them through here.
package clientorder

import (
	"crypto/rand"
	"strings"
)

// Prefix opens every id.
const Prefix = "msh-"

// New is a fresh id for the agent.
func New(agentID string) string {
	return prefix(agentID) + random(6)
}

// IsOf reports whether id is one of the agent's: its prefix and six
// base32 characters after it, within the 36 characters venues allow.
func IsOf(id, agentID string) bool {
	p := prefix(agentID)
	if !strings.HasPrefix(id, p) || len(id) != len(p)+6 || len(id) > 36 {
		return false
	}
	for _, c := range id[len(p):] {
		if !strings.ContainsRune(alphabet, c) {
			return false
		}
	}
	return true
}

// prefix is Prefix, the agent id without its kind prefix, and a dash.
func prefix(agentID string) string {
	short := agentID
	if i := strings.IndexByte(short, '_'); i >= 0 {
		short = short[i+1:]
	}
	return Prefix + short + "-"
}

const alphabet = "abcdefghijklmnopqrstuvwxyz234567"

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("clientorder: " + err.Error())
	}
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}
