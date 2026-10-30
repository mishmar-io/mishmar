# Presigning for cb-mpc's two-party ECDSA: a note for the reviewer

Mishmar signs Hyperliquid orders with Coinbase's two-party ECDSA (cb-mpc,
C++, MIT) between an agent's signer and Mishmar's signer service. To make an
order a single hop, we split the library's four-message signing protocol:
messages 1 to 3 run ahead for a batch of nonces, and an order carries only
message 4. The library's protocol is audited; our split of it is not. This
note is what a reviewer needs to assess the split before the scheme signs
for real money.

Where the change is:

- `mishmar-io/cb-mpc`, branch `presign`, commit `06bdbed` on top of upstream
  `99ecc49`. `git diff 99ecc49 06bdbed` is the whole patch: six files,
  478 lines added, nothing removed. Working tree in this workspace:
  `cb-mpc-go/cb-mpc` (the submodule).
- `mishmar-io/cb-mpc-go`, branch `presign`, commit `e6ff887` on top of
  upstream `69f56c8`: the cgo binding for the three new calls, plus the
  module path rename. `git diff 69f56c8 e6ff887 -- internal/cgo/binding.go pkg/mpc/ecdsa.go`
  is the part that matters.
- On top of the library, in this repository: `ecdsa2p/` (the Go sessions and
  the single-use slots), `signer/ecdsa.go` and `signer/oneshot.go` (the
  agent's side), `signer/service/service.go` (Mishmar's side), and in the
  engine `internal/venue/hlagent/sign.go` (the relay between the two).
- The spike that measured it: `../spikes/cbmpc-presign/README.md`.

Line numbers below refer to those commits for the library and to the
repository at the time of writing for our code; our files move, the
function names do not.

## 1. The stock protocol

cb-mpc's `ECDSA-2PC-Sign-2P` (spec: `docs/spec/ecdsa-2pc-spec.pdf`; the
protocol names cited here are the ones the code's `@specs` annotations use,
`include-internal/cbmpc/internal/protocol/ecdsa_2p.h:34-35`; I could not
extract section numbers from the PDF in this environment, so the reviewer
should map the names to sections) is a Lindell 2017 style protocol with
additive key shares. P1 holds `x_share` = x1 and a Paillier private key,
plus `c_key`, the Paillier encryption of x1; P2 holds x2 and the Paillier
public key; the joint key is Q = (x1 + x2)·G (`key_t`,
`protocol/ecdsa_2p.h:12-19`; P1 derives P2's public share as Q − x1·G at
`protocol/ecdsa_2p.cpp:369-371` and `:573`). The nonce is
multiplicative, R = k1·k2·G.

The four messages, `sign_batch_impl` at `protocol/ecdsa_2p.cpp:241-404`:

1. P1 → P2: a commitment. P1 samples k1, computes R1 = k1·G and a batch
   proof of knowledge of k1 (`:272-276`), and commits to `(msgs, R1, pi_1)`
   (`:278`). The code's comment on the messages: "Adding msgs here serves
   as a way of checking the consistency of the input messages" (`:277`).
2. P2 → P1: R2 = k2·G with its proof (`:283-293`).
3. P1 → P2: the decommitment `(com.rand, R1, pi_1)` (`:305`), after
   verifying P2's proof and computing R = k1·R2 (`:299-303`).
4. P2 → P1: for each message, the Paillier ciphertext c and a
   zero-knowledge proof that it was formed correctly (`:311-349`). P2 opens
   the commitment and verifies P1's proof (`:314-317`), computes
   r = R.x mod q and c = Enc(k2⁻¹·(m + r·(x1 + x2)) + ρ·q) by the Paillier
   homomorphism (`:320-337`), and proves it
   (`Prove-ZK-Two-Party-ECDSA-Sign-Integer-Commit-1P`, `:344-346`, with the
   session id and the batch index i as the proof's `aux`).

P1's output step (`:361-400`): verify the proof (`:372`), check the
ciphertext (`:378`), decrypt, s = Dec(c)·k1⁻¹ mod q, normalise to low s
(`:387-388`), DER-encode, and verify the signature against Q (`:395`).

Two modes. The default (proof) mode is the above. In "global abort" mode
P2 sends no proof and P1 relies on the final signature verification alone;
a failure then returns `E_ECDSA_2P_BIT_LEAK` (`:397`) and, per
`SECURE_USAGE.md:187`, the key must be locked: each such abort can leak one
bit of P1's share to a corrupted P2. The public API does not expose the
global-abort variant (`SECURE_USAGE.md:187`). The patch adds no global-abort
path.

The audit. The repository ships `docs/cure53-audit.pdf`. Public records
describe two Cure53 engagements on cb-mpc in 2024 (CBS-01 and CBS-02,
cryptography and source review). I could not extract the report's scope
list here; the reviewer should confirm from the PDF that `ECDSA-2PC-Sign-2P`
was in scope. The three functions the patch adds were not.

## 2. What the patch changes

`src/cbmpc/protocol/ecdsa_2p.cpp` (the protocol):

- `presig_t` (`protocol/ecdsa_2p.h:130-136`): one party's state for a
  batch: its role, the session id, its own nonces k (k1 on P1, k2 on P2),
  the joint points R, and P2's points R2. `MAX_PRESIGN_BATCH = 4096`
  (`:138`).
- `check_presig_slot` (`:436-442`): role must match the key's and the
  state's, the three vectors must have the same length, the index must be
  in range.
- `presign_batch` (`:444-515`) is messages 1 to 3 of `sign_batch_impl` for
  n nonces, with one difference: the commitment of message 1 covers
  `(R1, pi_1)` (`:469`) and the opening checks the same (`:499`); the
  messages are not known yet, and the stock code only used them as an
  input-consistency check (`:277-278`, `:314`). Everything else is the
  stock code: the session id is agreed the same way when the caller passes
  none (`:450-451` vs `:259-260`), P1 proves k1 (`:467`), P2 proves k2
  (`:480`), each verifies the other's proof (`:491`, `:501`), and both
  compute R (`:492`, `:502`). The state is stored (`:505-514`); nothing
  is sent that the stock protocol does not send.
- `presign_complete_p2` (`:517-552`) is the stock message-4 computation
  for one index (compare `:320-346`): r from the stored R (`:525`),
  fresh ρ and Paillier randomness (`:527-529`), the same plaintext and
  ciphertext algebra (`:531-543`), and the same proof with the stored
  session id and the index as `aux` (`:546-547`). The digest m enters
  here and nowhere earlier.
- `presign_finish_p1` (`:554-595`) is the stock P1 output step for one
  index (compare `:361-400`): deserialise c and the proof (`:566-567`),
  verify the proof against the stored R2 and the digest (`:574-576`),
  check the ciphertext (`:580`), decrypt and divide by the stored k1
  (`:584-586`), low-s (`:587-588`), verify against Q (`:593`). Default
  mode only: there is no path that skips the proof.

`src/cbmpc/api/ecdsa2pc.cpp` (the blob layer):

- `presig_blob_v1_t` (`:310-320`) serialises the state with a version,
  role and curve; `deserialize_presig_blob` (`:339-378`) checks the
  version, the role and curve against the key blob, the batch size, that
  every nonce is in range and non-zero, and that every point is on the
  key's curve (`:362-370`). `load_key_and_presig` (`:380-394`) runs the
  size limits and both deserialisations on every call.
- `presign`, `presign_complete`, `presign_finish` (`:397-432`) wrap the
  protocol functions; `presign` checks the job's role against the key's
  (`:406-407`).

`src/cbmpc/c_api/ecdsa2pc.cpp:262-370`: the C entry points
`cbmpc_ecdsa_2p_presign`, `_presign_complete`, `_presign_finish`, with the
same argument validation and ownership rules as `cbmpc_ecdsa_2p_sign`.

`cb-mpc-go`: `internal/cgo/binding.go:1757-1806` and
`pkg/mpc/ecdsa.go:51-73` expose `ECDSA2PPresign`, `ECDSA2PPresignComplete`
and `ECDSA2PPresignFinish`. Input bytes are copied into C memory for the
call and freed with `cbmpc_cmem_free` (`binding.go:457-466`); outputs are
copied into Go byte slices and the C buffer freed (`binding.go:512-516`).

Public headers: `include/cbmpc/api/ecdsa_2p.h:80-99` and
`include/cbmpc/c_api/ecdsa_2p.h:87-111` document the three calls and the
rule that one slot signs one message.

## 3. Why we believe there is no new math

- The messages exchanged are the stock protocol's messages 1 to 3 followed,
  later, by the stock message 4. The only element dropped is the digest
  list inside the message-1 commitment, which the library itself describes
  as a consistency check and which the spec's message 1 does not contain
  (the reviewer should confirm the latter against `ECDSA-2PC-Sign-2P` in
  the PDF).
- R is fixed before the message is known. In the stock protocol it is
  fixed before the message is used: m is first read in message 4
  (`:332`). What changes is that both parties, and anyone who sees the
  presigning transcript, know R before m. That is the definition of a
  presignature; see section 5 for what is known about it.
- The proof of message 4 binds the session id, the index, m, r and R2
  (`:546-547`, verified at `:574-576`), so a message 4 cannot be finished
  against another slot, another session, or another digest.
- P1's finish verifies the signature against Q before returning it
  (`:593`), and returns nothing on any failure.

## 4. The new state, and what can go wrong with it

Nonces now live across calls, on both sides, as `presig_t` inside a blob.
Everything below follows from that.

**A nonce used twice reveals the key.** This is plain ECDSA: two signatures
(r, s) and (r, s') on m and m' with the same k give k = (m − m')/(s − s')
and x = (s·k − m)/r. In the stock protocol it cannot happen because the
nonce exists only inside one call. In the patch it is prevented by the
callers:

- The C++ does not track used slots. `check_presig_slot` only checks the
  range (`:436-442`). Calling `presign_finish_p1` twice with the same index
  and two digests would produce two signatures under one nonce. This is
  deliberate (the state is immutable and serialisable) and it is why every
  layer above burns a slot before using it.
- Go, `ecdsa2p/ecdsa2p.go`: `Presig.take` (`:509-521`) marks a slot used
  under a mutex and refuses it a second time; both `Complete` (`:532`) and
  `Finish` (`:560`) take the slot before calling the library, so a failed
  call burns the slot too.
- Mishmar's signer service, `signer/service/service.go`: slots are issued
  with random ids after presigning round 2 (`PresignStep`, the loop that
  fills `s.slots`); `Finish` deletes the slot from the map before anything
  else, "whatever comes next", and refuses an unknown, spent, foreign or
  expired id with a 409.
- The agent's signer, `signer/ecdsa.go`: `takeSlot` (`:103-134`) pops the
  next slot under the pool's mutex and advances the cursor; a slot is never
  handed out twice. Its callers (`signer/oneshot.go:251-255`,
  `signer/signer.go:166-170`) then call `Complete`, which burns the slot
  again at the `Presig` level.
- The engine, `internal/venue/hlagent/sign.go`: `slots.take` (`:77-86`)
  deletes the slot id from the adapter's record before the message is
  checked (`TakeOneShot`, `:198-203`), and the channel path does the same
  (`:414-416`). The engine only relays; the service is the authority.

**An agent completing one slot twice gives its share away.** This is the
hazard the reviewer should weigh most. P2's message 4 for a slot with nonce
k2, point r and digest m decrypts, for P1, to d = k2⁻¹·(m + r·x) mod q with
x = x1 + x2. If P2 ever produced message 4 for the same slot and a second
digest m', P1 (or anyone holding P1's Paillier key) gets d' and computes
k2 = (m − m')/(d − d'), then x = (d·k2 − m)/r, then x2 = x − x1. So a
malicious service that could induce the agent to re-complete a slot (for
example by refusing the first order and asking for "the same order again")
would recover the agent's share and the whole key. The agent's `take`
makes that impossible: a refused or failed order never re-uses its slot,
and the retry takes a fresh one (`signer/oneshot.go`, `Propose`: a 409
from the engine drops the key's slots and resends through the channel with
a fresh nonce and a fresh slot). The reviewer should confirm there is no
path on the agent side that calls `Complete` twice for one `(presig, index)`.

**Replay of a message 4 to P1.** Refused: the service's slot is spent on
the first attempt, and `Presig.take` refuses the index afterwards
(`ecdsa2p_test.go`, `TestPresignCompleteFinish`). A replay would in any
case produce the same signature on the same digest, which is harmless.

**A message 4 paired with another digest.** Refused by the proof
(`:574-576`) and, independently, by the final verification (`:593`);
tested in `TestFinishRefusesWhatIsNotForTheSlot`. More important than the
test: neither side signs a digest it was told. The agent builds the venue
action itself and hashes it (`signer/oneshot.go:236`); the service decodes
the action bytes it is given, checks their freshness and that they match
an engine-signed decision, and hashes them itself (`Finish`:
`hyperliquid.Decode`, `CheckFresh`, `decision`, `matchHyperliquid`,
`hyperliquid.Digest`, then `key.Finish`); the engine checks the same bytes
before relaying (`hlagent/sign.go:218-250`). The agent's channel-path
checks recompute the digest the same way (`signer/checks.go:194`). This is
our answer to the raw-signing class of presignature misuse (section 5).

**Memory only.** Slots and sessions are never written to disk, on either
side: the service keeps them in `s.slots` and `s.presigns`
(`service.go`, the comment at the top of the file and the `Store` doc in
`store.go`), the agent in `oneShotState.presigs`
(`signer/oneshot.go:46-51`). A restart loses them and the agent presigns
again. The operational rule that follows: never snapshot, clone or fork a
running signer process or its VM, and never resume one from a snapshot; a
duplicated memory image is a duplicated nonce.

**Bounded outstanding presignatures.** One run makes at most 1024 nonces
(`ecdsa2p.go:71`, `MaxPresign`), the library allows 4096
(`protocol/ecdsa_2p.h:138`), the engine caps what a key may hold by the
account's entitlement (`internal/engine/keygen.go`, `SignerPresign`, and
`hlagent.PresignRound`), and a slot expires 24 hours after issue
(`service.go`, `NonceTTL`; the agent stops using one 30 s before,
`signer/oneshot.go:32`). For mainnet we intend a low default, 16 to 32 per
key.

**Session ids.** The stock API lets the caller pass a session id and
agrees one with `generate_sid_fixed_2p` only when none is given
(`protocol/ecdsa_2p.cpp:259-260`; `sid.h:12-17`, a weak agree-random with
P2 sending first). We pass one: SHA-256 of a 16-byte random half from the
agent followed by a 16-byte random half from the engine
(`ecdsa2p.go:382-400`, `NewSIDHalf`, `JointSID`), exchanged in the first
presigning round. The id goes into the commitment (`:458`), both batch
proofs (`:467`, `:480`) and every message-4 proof (`:546-547`). The
reviewer should say whether this derivation is acceptable in place of the
library's, and whether a party choosing its half after seeing the other's
matters (the engine's half is returned with message 1, after the agent's).

**Zeroisation, as found.** cb-mpc wipes its own memory: `bn_t`'s destructor
is `BN_clear_free` (`src/cbmpc/crypto/base_bn.cpp:85`), `buf_t` wipes on
free (`src/cbmpc/core/buf.cpp:77-82`), and the C API's `cbmpc_cmem_free`
wipes before freeing (`src/cbmpc/c_api/common.cpp:38-41`); the Go binding
frees the C copies of its inputs through that call (`binding.go:457-466`)
and its outputs after copying them out (`binding.go:512-516`). So `presig_t`
and `key_t`, and the C-side copies of the blobs, are wiped when they go out
of scope. What is not wiped: the Go byte slices that hold the key blob, the
presig blob and message 4 between calls (`ecdsa2p.Key.blob`,
`Presig.blob`), since Go offers no reliable secure wipe and the garbage
collector may copy them. The same is true of the stock wrapper's key blob;
the patch adds the presig blob to it.

## 5. What is known about presignatures and about this protocol

- Lindell, "Fast Secure Two-Party ECDSA Signing", CRYPTO 2017: the base
  protocol family. In it the message is used only by P2 in its last
  message, which is what makes this split mechanical.
- Groth and Shoup, "On the Security of ECDSA with Additive Key Derivation
  and Presignatures", EUROCRYPT 2022 (ePrint 2021/1330): analyses ECDSA
  with presignatures in a generic group model tailored to elliptic curves
  (their EC-GGM). Presignatures alone come out secure; the security loss
  they found is in the combination of presignatures with additive key
  derivation (BIP32-style tweaks), and they propose re-randomised
  presignatures as a cheap mitigation. We use no key derivation: each venue
  key is a plain key pair. The reviewer should say whether anything in our
  use calls for re-randomisation anyway, and should note the caveat that
  a generic-group result rules out generic attacks only.
- CGGMP21 (Canetti, Gennaro, Goldfeder, Makriyannis, Peled): presigning is
  a standard, proven mode of threshold ECDSA with an offline phase that
  outputs nonce material and a one-round online phase. A 2025 advisory
  against a Rust implementation of it (the cggmp21 crate; we have read the
  summary only) concerned an API that let a caller sign attacker-chosen
  digests with presignatures, fixed by API safeguards. Our rule that each
  party only signs a digest it computed from an action it decoded and
  checked (section 4) is the same safeguard, enforced in our code rather
  than the library's.
- BitForge, Fireblocks, August 2023 (CVE-2023-33242 for Lindell17):
  deployed implementations mishandled aborts, so a corrupted party could
  learn one bit of the other's key per failed signature and recover it in
  roughly 256 attempts. The attack lives in the global-abort shape of the
  protocol. cb-mpc's default mode has P2 prove message 4 correct and P1
  refuse before decrypting anything malformed (`:574-581`), and the
  library's own guidance is to use that mode unless aborts are handled
  with care (`SECURE_USAGE.md:187`). The patch uses only the default mode.
  The reviewer should confirm that the proof-mode argument is unaffected
  by running the proof per slot at a later time, and that P1's finish has
  no other failure that depends on secret data in a way P2 can observe.

## 6. What we would like the reviewer to answer

1. Is dropping the digests from the message-1 commitment safe? They are a
   consistency check in the code and absent from the spec's message 1; is
   there any argument in the spec or the theory document that relies on
   them?
2. Does the security argument for `ECDSA-2PC-Sign-2P` in proof mode carry
   over unchanged when messages 1 to 3 run ahead and message 4 runs later,
   once per slot, with the message chosen after R is known by both
   parties (and by the engine that relays)?
3. The message-4 proof uses `sid` and `aux = index`. Is that binding
   sufficient so that one slot's message 4 cannot be used for another slot,
   batch, session or key? Is anything else needed in `aux` (for example a
   key identifier)?
4. The caller-supplied session id (SHA-256 of two 16-byte halves, the
   agent's first): acceptable in place of the library's agreement?
5. Is there any way for P2 to learn about P1's share, or for P1 to learn
   about P2's, from a `presign_finish_p1` failure after the proof verified
   (ciphertext check, decrypt, final verification)?
6. The single-use enforcement lives in callers, not in the library. Is the
   division acceptable, and is there a cheap way to make the C++ refuse a
   second use that is worth having (the state is immutable by design)?
7. `deserialize_presig_blob`: is the validation complete (nonce range,
   points on the curve, sizes, role and curve matching the key)? Is any
   check missing that `sign_batch_impl` relied on implicitly because the
   values never left the process?
8. Zeroisation: is the Go-side exposure of the blobs between calls
   acceptable for a signer process, or should the Go layer hold the C
   handles instead?
9. The outstanding-presignature bound: is 16 to 32 per key a sensible
   mainnet default, and is 24 hours a sensible lifetime?
10. Anything in the cgo binding (`binding.go:1757-1806`) that mishandles
    ownership, errors or lengths.

## 7. What is tested today

`ecdsa2p/ecdsa2p_test.go`:

- `TestKeygenAndKeyFiles`: key generation through the step sessions, the
  JSON key files, role checks, tampering refused.
- `TestPresignCompleteFinish`: n slots signed and verified, each recovering
  the joint key; a used slot refused on both sides; an index out of range
  refused; the engine cannot `Complete`, the agent cannot `Finish`.
- `TestFinishRefusesWhatIsNotForTheSlot`: finishing as another slot, with
  another digest, and with a flipped byte all refused with no signature,
  and each attempt burns the slot it named.
- `TestSessionIDs`: the halves, the joint id, order dependence, bad sizes.
- `TestSessionsFailCleanly`: a corrupted key-generation message is refused;
  an abandoned run cannot finish; presigning under a session id that does
  not match the other side's is refused.

`signer/service/service_test.go`:

- `TestKeygenPresignFinish`: key generation, a second generation for the
  same id refused (409), presigning, a single-hop finish whose signature
  recovers the key's address, a spent slot refused (409), another order's
  decision refused, a blocked decision refused, a forged engine signature
  refused, every attempt burning its slot, forget.
- `TestFrost`: the FROST side of the same service.

`signer/` tests cover the agent's checks of a Hyperliquid message before it
completes a slot. The engine's `internal/e2e/hyperliquid_test.go` runs key
generation, approval, presigning, a one-shot limit order, a one-shot cancel
and a market order through the channel against a fake venue that recovers
the joint address from every signature. The spike's soak (400 orders, five
byte flips, wrong slot, other digest) is in `../spikes/cbmpc-presign`.

To build and run: the fork's `scripts/go_with_cpp.sh` wraps every Go
command, with `CBMPC_OPENSSL_ROOT` pointing at the static OpenSSL its
scripts build and `CXXFLAGS=-DDY_NO_LOG`; `make test` in this repository.

## 8. Known gaps and open questions

- The C++ does not track used slots; single use is enforced three times
  above it (section 4), never in the library.
- Zeroisation stops at the Go boundary (section 4).
- `cb-mpc-go` is a beta wrapper (its upstream marks v0.1.0 as beta), and
  our binding for the three new calls has had no review beyond ours.
- The session id derivation is ours (section 4).
- The presig blob crosses the C API on every call and is deserialised and
  validated each time (`api/ecdsa2pc.cpp:339-394`); the key blob likewise,
  as in the stock `sign` (`api/ecdsa2pc.cpp:139-213`, every entry point
  calls `deserialize_key_blob`). The spike measured about 3 ms of the
  engine's 15 ms per finish in that re-validation. A handle-based API
  would remove it and would also keep the secrets in C memory; not done.
- We have not asked Coinbase to review or merge the patch. An upstream
  pull request would put their maintainers' eyes on it and, if merged,
  remove the fork; that decision is open.
- Mainnet policy intended but not yet enforced in configuration: a low
  per-key cap on outstanding presignatures, and alerting on repeated
  finish refusals for one key, since a corrupted agent probing the service
  would show up there first.
