# The trade sign-in, byte by byte

How a computer signs in to trade for an AI app (`mishmar mcp --app <app>`), as the
client in `signer/signin` does it and the control plane must answer it. The plan is the
wiki's `design/app-trading-plan.md`, section 2.2; this page settles what it left open, so
the control plane's endpoints can be built and checked against the same expectations.
`signer/signin/signintest` is a stand-in that behaves this way, and its tests
(`signer/signin/signin_test.go`, `TestStandInSteps`) are the checks to hold the real
endpoints to. Not a docs.mishmar.io page.

Not in this version: signing in by typing a code on another device (RFC 8628), and
renewing a key in place (a computer that signs in again makes a new key; the engine
answers a public key already registered with 409).

## 1. Before anything starts

The app's MCP server, and the background signer again, look at the machine
(`signin.Detect`). Any of these refuses the sign-in with a plain message, and nothing is
started, made or sent:

| `machine_hint` | Signals |
| --- | --- |
| `ssh` | `SSH_CONNECTION`, `SSH_CLIENT` or `SSH_TTY` set |
| `container` | `/.dockerenv` or `/run/.containerenv` exists; `KUBERNETES_SERVICE_HOST` or `REMOTE_CONTAINERS` set |
| `cloud` | `CODESPACES`, `GITPOD_WORKSPACE_ID`, `CLOUD_SHELL` or `REPL_ID` set; Linux DMI `sys_vendor` `Amazon EC2` or `Google` |
| (none: no browser) | Linux with neither `DISPLAY` nor `WAYLAND_DISPLAY`, outside WSL (`WSL_DISTRO_NAME`, `WSL_INTEROP`, or `microsoft` in the kernel release) |

A variable counts when it is set to something other than spaces. So in this version
`machine_hint` on a request is always empty; the parameter is there for code entry.

## 2. The key and the secrets the client makes

- The key: an Ed25519 key from 32 random bytes (the seed). Kept in memory until the
  sign-in succeeds, then only in the app's config, as the secret
  `agentkey.NewSecret(environment, keyId, seed)` renders it
  (`msk_<environment>_<account suffix>_<key random part>_<seed in base64url>`), so
  `~/.mishmar/apps/<app>/agent.json` is today's `signer.Config` format.
- Its fingerprint and code: `agentkey.Fingerprint`, pinned by
  `spec/vectors/agent-key-fingerprint.json` (the first 80 bits of SHA-256 of the raw
  32-byte key, Crockford base32, `XXXX-XXXX-XXXX-XXXX`; the code is the first two
  groups).
- The PKCE verifier: 32 random bytes, base64url without padding (43 characters); the
  challenge is S256 of it.
- The state: 24 random bytes, base64url without padding (32 characters).

## 3. Authorize

The browser opens `{api}/oauth/authorize?…` with these parameters, each exactly once,
form-encoded in Go's `url.Values.Encode` order (sorted by name):

| Parameter | Value |
| --- | --- |
| `response_type` | `code` |
| `client_id` | `mishmar-cli` |
| `redirect_uri` | `http://127.0.0.1:<port>/callback`, the port the OS gave the listener; always IPv4 127.0.0.1, never `localhost` |
| `code_challenge`, `code_challenge_method` | the S256 challenge (43 characters), `S256` |
| `state` | as above |
| `resource`, `scope` | `urn:mishmar:trade`, `trade` |
| `agent_key` | the 32-byte public key, base64url **without padding** (43 characters) |
| `machine_name` | the host name without a `.local`, `.lan` or `.localdomain` ending, cleaned: control, bidirectional and zero-width characters out, white space collapsed to one space, at most 64 characters |
| `app` | the app's slug, `^[a-z0-9][a-z0-9-]{0,31}$` (`claude-code`) |
| `machine_hint` | always sent; empty means the client looked and found nothing |
| `cli_version` | the binary's version (`v0.5.0`, or `dev` for a development build) |

`{api}` is the hosting API's base URL with no path and no trailing slash
(`https://api.mishmar.io`; `http` is taken only for a loopback host, in development). It
is also the issuer the callback must name and the proof signs, and the host the engine
is reached through.

The control plane refuses a request outside this (another client, a redirect that is not
loopback `/callback`, another resource or scope, a key that is not 32 bytes, an unknown
hint, a parameter twice) with a plain page and no redirect, and otherwise stores the
request with the key and its fingerprint and sends the browser to the console's approval
page, which shows the code.

## 4. The callback

The browser comes back to `GET http://127.0.0.1:<port>/callback` with either
`code`, `state` and `iss`, or `error`, `error_description`, `state` and `iss` (as
`oauthDeny` already answers: `error=access_denied`).

The client checks them in this order:

- A parameter twice: refused, the flow goes on.
- `state` first. Missing or not the flow's (compared in constant time): a 400 page, and
  the flow goes on waiting for its own answer. Nothing else in such an answer is read, so
  a page that does not know the state cannot end the flow.
- Only the first answer with the right state is handled; another gets a 409 page.
- `iss` only then, on that first answer: missing or not exactly `{api}`, the flow fails
  (RFC 9207, a mix-up) and nothing is kept.
- `error=access_denied`: the flow ends `denied`. Any other `error`: `failed`.
- After the outcome, the listener closes. A flow nobody answers ends `expired` after 10
  minutes (the control plane's request lives 10 minutes).

## 5. The token step

`POST {api}/oauth/token`, `application/x-www-form-urlencoded`, no `Authorization`
header, no `client_secret`, each field once:

| Field | Value |
| --- | --- |
| `grant_type` | `authorization_code` |
| `code`, `code_verifier` | the callback's code, the PKCE verifier |
| `client_id` | `mishmar-cli` |
| `redirect_uri` | exactly the one on the authorize request |
| `resource` | `urn:mishmar:trade` |
| `key_proof` | the machine proof, base64 standard padded |
| `key_signed_at` | the time the proof signs, exactly as signed |

The machine proof (`agentkey.MachineProof`, pinned by `spec/vectors/machine-proof.json`
with the refusals the control plane must make) is Ed25519 with the computer's key over
the canonical JSON

```
{"code":"<SHA-256 of the code's bytes, lower-case hex>","issuer":"<{api}>","kind":"mishmar-machine-proof","publicKey":"<the key, base64 standard padded>","signedAt":"<RFC 3339 UTC, fraction trimmed>"}
```

The control plane rebuilds the bytes from the code it issued, its own base URL and the
key stored at authorize (decoded from `agent_key`, re-encoded base64 standard padded), and
takes `key_signed_at` within 60 seconds of its clock either way. It spends the code on
the first try, right or wrong (code TTL 2 minutes), and refuses with OAuth's JSON
`{"error":…,"error_description":…}`: `invalid_grant` for a code unknown, used or
expired, another client or redirect, a wrong verifier or a proof that does not verify
or is stale; `invalid_target` for another resource; `invalid_request` for a missing
proof.

## 6. The token answer, and what the client checks

200, `application/json`:

```json
{
  "token_type": "mishmar_agent_key",
  "scope": "trade",
  "mishmar": {
    "machineId": "mch_…", "accountId": "acc_…", "agentId": "agt_…", "agentName": "Claude Code",
    "keyId": "key_<account suffix>_<12>", "keyExpiresAt": "2027-05-12T09:30:00Z",
    "environment": "prod", "engine": "https://api.mishmar.io/a/acc_…/v1",
    "connectedAccountId": "ta_…", "venueKeyId": "vk_…",
    "fingerprint": "AS3N-N9TM-CD3M-R0M5",
    "next": "https://app.mishmar.io/agents/agt_…?setup=1"
  }
}
```

`fingerprint` is an addition to the plan's answer: the fingerprint of the public key the
control plane registered. The client refuses, keeping nothing, an answer where:

- `token_type` is not `mishmar_agent_key` or `scope` not `trade`;
- `access_token` or `refresh_token` is present (a trade grant has no bearer token);
- `fingerprint` is not this computer's key's;
- `agentId`, `accountId` or `connectedAccountId` is missing, or `keyId` is not
  `key_<the account's suffix>_<…>`;
- `environment` is not the one asked for (`--env`; any valid word with `--api`);
- `engine` is not exactly `{api}/a/{accountId}/v1`;
- `keyExpiresAt` has passed, or is more than 366 days ahead.

`machineId`, `venueKeyId` and `next` are kept for the status (`app.json`); `next` must be
an absolute `https` URL (or `http` on loopback) for the browser to be sent there.

Then the client asks the engine `GET /agent/me` signed with the new key. The engine
verifies the signature with the public half registered under `keyId`, so an answer
proves Mishmar holds this computer's key. The answer must name the same key, agent and
account, and `keyScope` `trade` when present. A 4xx (but 429) fails the sign-in; a 5xx,
a 429 or no answer does not (the key is registered; the signer asks again when it
starts).

Only then are the config and `app.json` written (0600, in `apps/<app>/`, 0700), the
app's signer started, and the browser sent on with a 302 to `next` (a plain "signed in"
page without one). The token step and the engine's check together have 60 seconds.

## 7. What the console's approval page shows

The code (the fingerprint's first two groups) the app's tool answer showed, and the whole
fingerprint (`XXXX-XXXX-XXXX-XXXX`, labelled "The key's full fingerprint"); the app and the
machine name as the program's claims. The program prints the whole fingerprint too, on the
line after the code in its sign-in text, in `get_trading_status` and in `mishmar daemon
status`, so a careful person can compare all of it.

What the code proves: the page in front of the person is for the key this program made. It
catches the case where the person asked their own app to sign in and the page they are
looking at is for some other request. What it does not prove is who started the request.
Anyone can build an authorize link around a key they made, and whoever makes a key knows its
code, so someone who starts a request and gets the person to open it can also tell them which
code to expect, and the codes will match. The code is also short: 40 bits, chosen by whoever
makes the key. That is enough against someone guessing while the person waits, but it is not
a strong commitment against someone with time and hardware who already knows the code they
need to match; the whole fingerprint is 80 bits.

What protects against a request someone else started is where the answer goes. Approving
sends the one-time code only to the loopback redirect (`127.0.0.1` or `[::1]`) on the
person's own computer, and the token step needs that code, the PKCE verifier and a
signature from the key (section 5). Someone elsewhere gets nothing unless the person also
hands over the loopback address from their browser. Approving does register the key at the
engine before the token step, but nothing can use it: its key id is never shown to whoever
made the request, and the reaper revokes it a few minutes later.

So the page, the program's sign-in text and the docs give the person the same advice:
approve only if you asked the app on this computer to set up trading in the last few
minutes; nobody from Mishmar will send you this link or a code.

Approving needs a passkey or wallet proof bound to this request and this key's fingerprint,
made in the last two minutes. A proof bound to a request stands for that request only: it
does not also count as the general ten-minute proof for the session's other approvals and
guarded changes. Approving onto an existing agent gives that agent the connected account
chosen on the page, so the agent reads that account as well as trading on it. Removing the
member who approved a computer, or demoting them below admin, ends that computer, and with
it its key and its trading keys.

Approving registers the key with `registerAgentKey {publicKey (base64 standard padded),
scope: trade, label: machine_name, expiresAt: now + 180 days}` and sets a trading key aside with
`createVenueKey {connectedAccountId, agentKeyId: <that key's id>}`. The key names its holder
(`VenueKey.agentKeyId`) from creation, so only the computer's signer generates it: another
of the agent's keys, a bot's say, gets 409 `conflict` with `details.venueKeyId`, and the
signer of any other key leaves a trading key that names another holder alone (no key
generation, no commitments, no presigning). A trading key that names no holder is generated
by the first trade key's signer to sync, and names that key only if it is a registered one.
