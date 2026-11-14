package daemon

import "time"

// The local API's shapes (version APIVersion).

// Health is GET /health: who answers, for the handshake.
type Health struct {
	Version   string     `json:"version"`
	API       int        `json:"api"`
	PID       int        `json:"pid"`
	StartedAt time.Time  `json:"startedAt"`
	Apps      []AppBrief `json:"apps"`
	// InProcess is a Manager inside the MCP server (Windows, or no daemon
	// could run): it signs only while the app is open.
	InProcess bool `json:"inProcess,omitempty"`
}

// AppBrief is one app in Health.
type AppBrief struct {
	Slug  string   `json:"slug"`
	State AppState `json:"state"`
}

// AppState is where an app stands on this computer.
type AppState string

// The app states.
const (
	// SignedOut: no key for the app here.
	SignedOut AppState = "signed_out"
	// SigningIn: a sign-in waits for the person in the browser.
	SigningIn AppState = "signing_in"
	// SignedIn: the app's key is here and its signer runs.
	SignedIn AppState = "signed_in"
	// Ended: Mishmar no longer takes the app's key (revoked, expired or
	// unknown); a new sign-in makes a new key.
	Ended AppState = "ended"
)

// ShutdownRequest is POST /shutdown. Reason upgrade is a newer binary
// replacing this daemon, refused (409 not_newer) unless Version is newer
// than the daemon's; stop is a person's.
type ShutdownRequest struct {
	Reason  string `json:"reason"`
	Version string `json:"version,omitempty"`
}

// SignInRequest is POST /apps/{slug}/sign-in.
type SignInRequest struct {
	// API is the hosting API's base URL; Environment the hosted
	// environment (prod, testnet, stage) or empty with an API given by hand.
	API         string `json:"api"`
	Environment string `json:"environment,omitempty"`
	// MachineName overrides this computer's name.
	MachineName string `json:"machineName,omitempty"`
	// Hints are what the asking process found remote about this machine;
	// any refuses the sign-in.
	Hints []string `json:"hints,omitempty"`
}

// SignIn is a sign-in's state.
type SignIn struct {
	// State is waiting, signed_in, denied, expired or failed (signin.Status).
	State string `json:"state"`
	// URL is the link the person opens; Code what the page must show;
	// Fingerprint the whole key's.
	URL         string    `json:"url,omitempty"`
	Code        string    `json:"code,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt,omitzero"`
	// Started says this request started the sign-in (so its caller opens the
	// browser); false when one was waiting already.
	Started bool `json:"started,omitempty"`
	// Error is why it did not end signed in.
	Error string `json:"error,omitempty"`
}

// Record is what a sign-in said about the app's grant, kept next to its
// config (app.json).
type Record struct {
	App                string    `json:"app"`
	API                string    `json:"api"`
	Environment        string    `json:"environment"`
	MachineName        string    `json:"machineName"`
	MachineID          string    `json:"machineId"`
	AccountID          string    `json:"accountId"`
	AgentID            string    `json:"agentId"`
	AgentName          string    `json:"agentName"`
	KeyID              string    `json:"keyId"`
	KeyExpiresAt       time.Time `json:"keyExpiresAt"`
	Fingerprint        string    `json:"fingerprint"`
	ConnectedAccountID string    `json:"connectedAccountId"`
	VenueKeyID         string    `json:"venueKeyId,omitempty"`
	Next               string    `json:"next,omitempty"`
	SignedInAt         time.Time `json:"signedInAt"`
}

// Status is GET /apps/{slug}/status: everything the status tool says.
type Status struct {
	App   string   `json:"app"`
	State AppState `json:"state"`
	// SignIn is the sign-in waiting, or the last one when it did not end
	// signed in.
	SignIn *SignIn `json:"signIn,omitempty"`
	// Record is the signed-in app's grant; Code its key's short code.
	Record *Record `json:"record,omitempty"`
	Code   string  `json:"code,omitempty"`
	// Permission is read or trade, as the app's signer learnt from GET
	// /agent/me (the agent's permission and the key's scope); empty until
	// it has.
	Permission string `json:"permission,omitempty"`
	// Signer is whether the app's signer runs, and why not.
	Signer SignerState `json:"signer"`
	// VenueKeys are the agent's trading keys as the engine lists them, with
	// whether this signer holds the share.
	VenueKeys []VenueKeyState `json:"venueKeys,omitempty"`
	// ConnectedAccount is the account the sign-in gave the agent.
	ConnectedAccount *AccountBrief `json:"connectedAccount,omitempty"`
	// Ended is why Mishmar no longer takes the key, when it does not.
	Ended   string    `json:"ended,omitempty"`
	EndedAt time.Time `json:"endedAt,omitzero"`
	// ReadError is why the engine could not be read for this status.
	ReadError string     `json:"readError,omitempty"`
	Daemon    DaemonInfo `json:"daemon"`
}

// SignerState is whether the app's signer runs.
type SignerState struct {
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

// VenueKeyState is one trading key of the agent.
type VenueKeyState struct {
	ID                 string     `json:"id"`
	Venue              string     `json:"venue"`
	ConnectedAccountID string     `json:"connectedAccountId"`
	Status             string     `json:"status"`
	Keygen             string     `json:"keygen,omitempty"`
	PublicKey          string     `json:"publicKey,omitempty"`
	HyperliquidStatus  string     `json:"hyperliquidStatus,omitempty"`
	APIKeyRegistered   bool       `json:"apiKeyRegistered"`
	ExpiresAt          *time.Time `json:"expiresAt,omitempty"`
	// Mine says this computer's key generated it (agentKeyId), or nothing
	// says another did.
	Mine bool `json:"mine"`
	// Held says this computer holds its share.
	Held bool `json:"held"`
	// Network is a Hyperliquid key's network, from the share.
	Network string `json:"network,omitempty"`
	// Name is the name its Hyperliquid API wallet is approved under.
	Name string `json:"name,omitempty"`
}

// AccountBrief names a connected account.
type AccountBrief struct {
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	Venue          string `json:"venue,omitempty"`
	ConnectionName string `json:"connectionName,omitempty"`
}

// DaemonInfo is about the process that answers.
type DaemonInfo struct {
	Version   string `json:"version"`
	PID       int    `json:"pid"`
	InProcess bool   `json:"inProcess,omitempty"`
	// SurvivesReboot is false in this version: nothing starts the signer
	// at login.
	SurvivesReboot bool `json:"survivesReboot"`
}

// info is run/daemon.json.
type info struct {
	PID       int       `json:"pid"`
	Version   string    `json:"version"`
	API       int       `json:"api"`
	Socket    string    `json:"socket"`
	StartedAt time.Time `json:"startedAt"`
}
