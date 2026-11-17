# DRAFT: bounded existing-worker launch authority

Status: **proposal for joint acceptance; unavailable and inactive**. MUST and
MUST NOT below describe this candidate, not shipped capability. This supplement
proposes one closed M0 subset under the `dispatcher-request/1` signing profile.
Selected committed-start **A** follows the [landed receiver design][interlock];
strict native-release **B** remains the direction for full acceptance. Selection
is a design decision, not implementation, qualification or activation.
The selected G2 security-classification branch B in §5 does not select strict
native-start option B: committed-start A remains the M0 selection.
Keymaker, Architect, broker and integration must review the exact candidate;
curator and independent review precede runtime code. Owner acceptance is still
required before protected operator installation or activation. Neither a valid
fixture nor a schema publication satisfies those gates.

## 1. Scope and relation to the full contract

One nonfederated trusted Linux worker, one existing active UID/generation, one
immutable worker plan, one physical launch maximum, and owned-run cancel. There
is no provision, standalone start, delegation, renewal, restart, pool, session,
remote target, untrusted placement or automatic retirement in this subset.
macOS remains separately native-gated. All applicable confinement, network,
capacity, export/custody, time, restore and operator guarantees in the
[main specification](dispatcher.md) remain.

The proposed protected policy substitutes **only in this separately selected
subset** for the full path's caller/dispatcher grant chains and fresh broker
`revocation.check` launch-authority dependency (main §§3.1–3.4). Activated broker
v0 supplies a lease for an exact operator-allowlisted worker UID. It supplies no
caller authority, freshness verdict or binding. M0 neither calls a fictitious
broker grant/bind/revocation operation nor produces an empty or fake grant.
The full grant-bearing `dispatch-op/1` grammar and its acceptance remain closed
and unchanged. An authenticated key association is necessary, never sufficient
launch authority. There is no fallback flag or credential-copy path.

Before dispatch, the operator performs ordinary `user.create`, waits for durable
`active`, then ordinary Keymaker register for that exact active/unbound
generation, installs the exact v0 UID allowlist and protected association/policy.
Provision/confirm is bump-only with a removed predecessor; M0 cannot fabricate a
predecessor or import pre-active provisioning. A lease-only worker does not need
a Keymaker key merely to execute; any selected enrollment is still operator-only.
Caller and worker are separately pinned and may be distinct. **No qualified
first caller is established by this proposal.** Enrollment, time/custody and
purpose-6 bootstrap staging/currentness remain owning-lane gates. Dispatch never
creates an account, adopts/rebinds one, changes generation or adds an allowlist
entry. A recreated generation requires fresh explicit operator setup.

The selected [user-manager setup/ownership delta][manager] (§“Minimal path and
release boundary”, including its creating-dispatcher requirement) remains a gate:
station-v0 create is dispatcher-only, and launch/stop check the original creating
dispatcher. A reviewed protected operator setup and dispatcher/account association
must satisfy those checks without rewriting allocation ownership, granting the
operator launcher access or adding a request-field bypass.

Purpose-6 registry publication is not required for this local association. That
does not waive actual QT-2c, custody, independent freshness or old-instance
exclusion dependencies imported by the selected bootstrap/time path. For the
actual M0 importing scopes — Keymaker bootstrap/custody, broker time settlement
(W/hold/in-flight), and broker D5 (acknowledged store head/start fence) — the
selected required path is **WC1 plus one common synchronous independent G17
custodian**, with a **separate lifetime fence** excluding restored, prepared and
old-instance effects, writes, signing and releases. Broker time settlement
(W/hold/in-flight) and broker D5 (acknowledged store head/start fence) are two
separate broker importing scopes.
Each importing scope, including Keymaker bootstrap/custody, retains its own
history, enrollment, identities, installation binding, namespace, credentials,
original occurrences and independently operator-approved first checkpoint/genesis.
A common custodian does not merge streams, credentials or genesis approval or
permit cross-consumer evidence substitution, including D5 history for time
settlement or time settlement history for D5. Missing or mismatched approved
genesis refuses import; automatic re-enrollment, a first-seen head or adopting
the other scope's history cannot repair it. Manual restore,
local approval or a local watermark cannot substitute for this custody or fence.

Ordinary current user-manager create/retire/launch/stop operations are not M0
custodian consumers. Later manager PC2 MC-Q5/A3d-2 is a separate consuming scope
on the common custodian only when separately published and qualified; no current
manager custodian adapter or protocol is implied. The selected importing path
does not turn ordinary broker-v0 alternatives into a universal service dependency.
Provider/placement selection, typed protocol/profile, complete originals,
currentness/continuity, bounded endpoint/load and exact native qualification
remain closed gates; selection alone supplies none of their evidence.

### First-run static cut: local caller key (PROPOSED)

For the first enforcing run only, the owner accepted a reduced gate: one
owner-signed static statement (`swarma-s7 static-gate/1`, format owned by
Keymaker and pending its publication) replaces, for a bounded window on one
host, the live custodian import of the three importing scopes above and the
B0–B5 association install. This section states what that cut means for the
request forms of §2.

It changes no form and no check of §2. For this run only, and by owner decision
rather than as a fallback, it departs from three statements: the purpose-6 key,
registration number and master epoch of the §2 Association row (see below); the
§2 signing paragraph, because the caller tool signs R with the local key
instead of Keymaker `sign_station_caller`; and main §3.3's rule that caller
private keys stay in Keymaker custody with no readable private key. The caller
tool signs only the R fixed by the protected signing plan, and any other R
refuses as before.

The statement adds one refusal outside §2 and removes none: before each
dispatch, and before owned cancel without the window check, the dispatcher
verifies the statement and compares its `association` and `dispatcher` sections
with its protected policy and configuration, under the `static-gate/1` consumer
rule.

- **The association key is a local key.** `association.key` is an Ed25519
  public key whose private half is a file made by the installer for the caller
  account: owned by the caller UID, mode `0600`. It is not derived through
  Keymaker purpose 6, and Keymaker has no custody of it. The file is created
  only after the owner's explicit consent to a new key on that host.
- **Where the key file may be.** The private key must not be readable by, or
  reachable from, the worker UID or any account other than the caller and
  root; the rules below enforce this. The file lies in a caller-owned directory of
  mode `0700`, reached through non-symlink ancestors owned by root or the
  caller UID that are not group- or other-writable, and outside every worker
  home, workspace, export and retention path. The caller tool opens it with
  `O_NOFOLLOW` and refuses any other owner, mode or file type. The private key is never
  printed, logged, journaled, passed in argv or the environment, copied, or
  included in a backup or export.
- **Caller and worker are different accounts.** In this cut the association
  UID MUST differ from the worker UID, and the worker UID MUST NOT be in any
  group of the caller. The installer refuses a policy that violates either
  rule; the dispatcher does not check it.
- **The key's only protection.** Main §3.3 custody does not hold for this key.
  The rules above are its only protection, and the host administrator can
  still read it.
- **Disposal.** Once the run's stop and cleanup are terminal, the installer
  removes the association and deletes the key file. A later run needs a new
  key and new consent.
- **The checks of §2 stay in full.** Every request carries `auth={key,sig}`;
  the key must equal the protected association key, the signature must verify
  over the exact preimage, the kernel peer UID must equal the association UID
  and the caller generation must be the independently active one. A request
  without `auth`, with another key or from another UID refuses as before.
- **Transport admission is separate from this cut.** The reference transport
  admits only unmanaged callers, with one named exception: the protected M0
  caller list ([transport](transport.md), "Managed M0 callers"). The caller's
  managed UID reaches these checks only through that list, which admits the
  connection and grants no authority; the association equality above still
  decides. A managed UID that is not on the list never reaches M0.
- **`registration_number` and `master_epoch` carry no Keymaker meaning in this
  cut.** They remain required members of the closed association, each in
  1..9007199254740991. The protected policy records one fixed value for each;
  a request copy must equal it, and at cancel it must equal the original
  dispatch record. They are not evidence of a Keymaker registration, and
  nothing may read them as one.
- **What pins the key.** The protected policy holds the association, so the
  policy digest covers the key. The static statement carries that policy digest
  and names the caller key, UID and generation directly, so the owner signs the
  public key the run relies on.
- **What the cut does not give.** Custody of the caller key by Keymaker, key
  rotation through a registered generation, and any protection against the
  host's own administrator. These return with purpose 6 in the full path. The
  purpose-6 number stays reserved and unallocated; this cut does not use or
  redefine it.
- **Window.** At or after the statement's `not_after`, the dispatcher admits no
  dispatch. The installer refuses a policy whose `not_after` is later than the
  statement's `not_after`, so committed start C (§3 step 2) and the run's own
  expiry fall inside the statement's window. Owned cancel skips only the window check: the statement's other
  checks still apply, and if they refuse, the retained stop obligation and the
  automatic stop of §3 continue. Stop, cleanup and the Keymaker and broker end
  operations of a run admitted inside the window keep working, as they do
  after the policy's own `not_after`.

The cut ends with the first enforcing run and is not a second permanent
profile. For that run it stands in only for the importing-owners row of §7
(Keymaker bootstrap/custody, broker time settlement, broker D5), for the
purpose-6 bootstrap and first-caller parts of the "Keymaker +
generation/time/storage owners" row, and for the purpose-6 staging, rotation
and epoch parts of the "Operator/curator and installer" row. Every other §7
gate applies to that run unchanged. The cut is unavailable until the
`static-gate/1` format is published, one statement is signed, and the
dispatcher's statement check and the caller tool have landed.

## 2. Closed proposed forms and signing

The complete proposed shapes are [request](../schemas/m0-request-1.schema.json),
[policy](../schemas/m0-policy-1.schema.json) and
[protected signing plan](../schemas/m0-signing-plan-1.schema.json), JSON Schema
2020-12. All object members are required and closed; only the two operation
branches differ. Unknown or cross-operation members refuse, including nested
ones. These are new proposal discriminators, not an already negotiated socket
capability or a relaxation of the full grammar. Selection/version negotiation
and receiver results remain publication gates; no incomplete response grammar
is enabled here and no `hello` or `method` member is imported into a request.

| Form | Exact semantic meaning, in addition to the schemas |
|---|---|
| Common request R | `v=1`, `kind="swarma-dispatcher m0-request/1"`, random 128-bit lowercase-hex `request_id`, exact protected `receiver` instance identity, `policy` ID/revision, complete `association`, existing `worker` tuple and exact `plan` pins. |
| Dispatch | Common R plus `op="dispatch"`; consumes the single launch budget for that existing worker. Unlike main §8.1 combined dispatch, it has no provision members, grants, caller bytes, command, arguments or environment. |
| Cancel | Common R plus `op="cancel"`, `dispatch_request_id` naming the original owned dispatch. This lookup resolves only that original run/effect; it is not a caller-selected PID or a new run selector. All pins equal the original record. |
| Auth | Wire request adds exactly `auth={key,sig}`. Key equals the protected association key; signature verifies R. Schema-valid example signatures do not verify. |
| Association | Protected ID/revision, kernel caller UID, independently active generation, purpose-6 public key, Keymaker registration number and master epoch. Caller-supplied copies must exactly match current protected evidence and kernel peer; they select no identity. |
| Plan pins | `worker_plan`, Curator `profile`, `harness`, `exec_profile`: exact content-addressed identities, no path/URL/wildcard. Protected catalog pins authenticated immutable origin, full typed bodies and ownership. Digest equality alone never validates an opaque executable payload. |
| Policy | Protected ID/revision, receiver, full association, worker, plan pins, protected `catalog` and exact accepted `qualification` identity; operations exactly `["dispatch","cancel"]`; one worker/start and finite time/stop bounds. It is local authority data, **not** an owner-signature/install envelope. |
| Signing plan | Exactly `v`, `kind="swarma-dispatcher m0-signing-plan/1"`, `profile="dispatcher-request/1"`, unsigned `request`. Native `plan_id` is `sha256:` plus lowercase SHA-256 of CCJ-1 of this entire object, without a prefix or final LF. This is dispatcher adapter data, not invented Keymaker bootstrap fields. |

Keymaker resolves the protected typed signing plan referenced by
`sign_station_caller {profile,plan_id}`, independently validates its full body,
digest, authenticated origin, current authority and receiver, constructs R and
its preimage itself, and returns `{key,sig}`. The dispatcher signing adapter
independently resolves the same validated values, assembles matching R and
inserts auth without changing R. It does not submit a free-form body for signing. The dispatcher independently
validates the same schema, catalog provenance and protected authority source.
The worker-plan/executor intake, exec-profile, catalog provenance/install and
receiver-internal encodings must be fully published and jointly selected before
use. A reference is not permission to admit an unpublished body as opaque bytes.

The [DRAFT executor-entry /1 proposal](m0-executor-entry.md) separately specifies a closed finite FD3 input and deterministic existing runner mapping. It remains pending owning selection and does not enable opaque plan bodies or settle Result authority. Protected equality belongs to D before handoff; bound timing, proposed PATH narrowing and native exec/stdin-pipe/status attribution remain owning selection and qualification gates.

Use registrar [CCJ-1 §2.1 at 5f44ebf][ccj], with its public [ccj][codec] and
[envelope][envelope] boundaries, **not Keymaker native JSON spelling**. Reject
duplicate names at every depth (including escaped aliases), invalid UTF-8/BOM,
lone surrogates, non-integer numeric tokens, negative zero and integers outside
the CCJ safe range; depth is at most 16. CCJ-1 sorts names by Unicode scalar,
preserves array order, applies no Unicode normalization, emits shortest integer
spelling and only its prescribed string escapes. Input whitespace/order can
differ; the resulting signed semantic object cannot.

```text
preimage = ASCII("swarma-dispatcher request/1") || 0x0a || CCJ-1(R)
R        = request with only top-level auth omitted
```

There is **no final LF**, recursive auth stripping, digest substitution,
envelope wrapper, native frame, SSHSIG or caller-chosen domain. Whole preimage
MUST be <=24576 bytes. Ed25519 key is `ed25519:` plus canonical unpadded
base64url of 32 bytes; signature is canonical unpadded base64url of 64 bytes.
Decode/re-encode, strict key/point/scalar and signature-equation verification
remain mandatory beyond shape validation. Keymaker's <=65536-byte native frame
is independent; dispatcher keeps main [§8.3](dispatcher.md#83-socket-dispatch1)
framing, connection/peer/FD rules and 128 KiB frame cap. Neither frame cap
enlarges the preimage bound. The registrar envelope API does not accept a new
dispatcher domain merely because its framing is similar; no API extension is
claimed by this reference.

### Proposed finite bounds

These choices require joint acceptance; they are not owner-decided facts. IDs
are 32 lowercase hex characters to match a 128-bit generation/correlation space;
digests are `sha256:` plus 64 lowercase hex characters. UID range is the existing
agent range 30000–59999, never authority to admit the range. Revision, registration
and epoch integers are 1..9007199254740991. Policy timestamps are integer Unix
seconds in 0..253402300799, avoiding alternate time spellings; qualified authority
time remains required. Require `not_before < not_after` and window <=3600 s.
Maximum runtime is 1..3600 s, additionally cut by policy expiry/removal. M0 is an
ordinary bounded run, not a standing allocation or a change to standing-runtime
rules. One hour limits this candidate's exposure and state horizon rather than
claiming an existing deployment default. This M0 window/runtime does not clip
broker lease authority: the actual broker policy governs its separate horizon.

For these closed ASCII request shapes, maximum CCJ-1 request preimages are
**964 bytes dispatch / 1019 bytes cancel**, including the 28-byte domain and LF.
Four serial fields (policy revision, association revision, registration number,
master epoch) each have at most 16 decimal digits; all other spellings have fixed
lengths. Dispatch is 28 + 872 + 4×16 = 964 bytes; cancel adds 55 bytes for its
operation spelling and target member. The 904-byte dispatch specimen uses four
one-digit serials. These finite maxima do not widen to Keymaker's separate generic
24576/24577-byte boundary. [Inert counterexamples][counterexamples] distinguish
that generic boundary from this subset; runtime enforcement remains pending.

One selected policy lineage, one worker, one admitted dispatch ID and one cancel
ID; same-ID retries reuse records. Further IDs refuse before reservation (one
dispatch already consumed) or reuse the original owned stop through authorized
recovery. Protected catalog has at most two signing plans, one per operation,
with the cancel target pinned to the selected dispatch ID. Reserve <=64 KiB
durable control/result state for this lineage, **in addition to** separately
bounded receiver, audit, receipt and cleanup stores whose complete accounting
is a gate. Tombstones/claims cannot be evicted on expiry or capacity pressure;
refuse forward work when reserves cannot cover the full transaction. This is not
an unlimited request log or an invented share of Keymaker's owning-store budget.
SC-Q6 must separately allocate and fence that store. Bounds on internal evidence
must be selected before activation, not filled with unbounded opaque records.

## 3. Protected installation, currentness and ordering

The managed M0 caller list in the protected `--m0-config` file is protected
installation data, installed only by the protected operator path together with
policy and association, under the [Managed M0 callers](transport.md#managed-m0-callers)
transport exception. Membership admits a kernel UID to M0 frames only; it never
modifies authority or replaces current active generation, association, policy or
any other M0 check.

### Closed daemon composition

The protected canonical M0 file is `swarma-dispatcher m0-config/3`, `v=3`:
exactly `callers`, `enabled`, `entry`, `kind`, `p17`, `recovery_attested`,
`state_dir`, `supported_local_storage`, `v`. Configurations /1 and /2 refuse;
there is no upgrade by omission. The required closed `p17` object is validated
even with `enabled:false`. Its exact members are `bind_timeout_ms`,
`keymaker_gid`, `keymaker_mode`, `keymaker_socket_dir`, `keymaker_socket_name`,
`keymaker_uid`, `recovery_attested`, `source_control_dir`, `source_generation`,
`source_socket_name`, `source_uid`, `state_dir`, `station`,
`supported_local_storage`. Missing, unknown, duplicate (including
escaped-alias) members and bad pins refuse `request_invalid` with a named
configuration/member detail. The existing 4096-byte, depth-3 and 256-token
limits still apply independently.

Service UID/GID pins are `1..4294967294`; `keymaker_mode` is a decimal permission
integer `1..511`. Source's service generation is 32 lowercase hex; its socket
name is exactly `origin.sock`. Keymaker's socket name is a nonempty basename
of at most 255 bytes, neither `.` nor `..`. Directories are absolute canonical
paths; P17 `state_dir` differs from M0 `state_dir` and neither contains the
other. `station` is the clients' canonical positive decimal uint64 string;
`bind_timeout_ms` is `1..2000`. No worker UID/range or mutable installation
progress is a configuration member. Placement is `isolation.Fixed` of the
installed policy's worker UID. Source has its own pinned adapter and dialer;
`keymaker.UnixDialer` with the configured socket/peer pins is the only Keymaker
dialer the composition supplies. P17's required `supported_local_storage` and
`recovery_attested` are booleans with the journal's existing storage and
established-recovery meanings, independently covering the journal in P17
`state_dir`. They are never inherited from the top-level attestations, which
cover only the separate M0 journal. Explicit false values parse but do not
authorize the corresponding storage/established-recovery operation.

The installer precreates the service-owned `0700` P17 state directory and its
empty `records` and `inputs` subdirectories through non-symlink, root/service-
owned ancestors not writable by group/others. The daemon never repairs them.
A private lifetime `.composition.lock` serializes bootstrap. Independent durable
markers `.ledger-install` and `.record-install` contain respectively
`starting\n`/`established\n` and `initialize\n`/`established\n`. Each publication
is temporary write, file fsync, rename, directory fsync. Explicit first-start
intent is durable before the first quarantine start; the record initialize fact
is also durable before that call. Under intent only empty record/input
directories, the two facts, bootstrap/launcher locks, non-authoritative reserved
`.install-*`/`.quarantine-*` temporaries and an optional client-format ledger
with ZERO entries may exist. A missing fact alongside an existing ledger,
journal or record refuses; a nonzero ledger under intent refuses. A missing
established ledger holds every UID and fails startup, never reinitializes.
Without `.ledger-install`, only `.composition.lock`, `.install-*` and empty
`records`/`inputs` may exist. A record fact, `launcher.lock` or a quarantine
temporary without ledger intent refuses even when the ledger is also missing.
Those artifacts follow the directory-fsynced ledger intent in every reachable
interruption; accepting them as fresh would erase possible pre-record holds.

Start the shared quarantine (acquiring `launcher.lock`) before initializing or
opening the one record store. Flip its ledger marker immediately after the
first successful `Quarantine.Start`, before any `Begin`; pass the selected
ledger fact unchanged to both launcher configuration and every quarantine
start for that process lifetime. Explicit intent permits crash resumption
whether the client's atomic zero-ledger publication happened or not; absence
alone cannot select fresh over existing state. The independent journal marker
flips immediately after the first successful record `Create`, before binding
continues, not on boot. Failed flips latch must-exit and prevent further
admission. `runrecord.Initialize` is lazy; there is no probe record. The old
M0 journal's explicit `entry` selection is independent; daemon progress never
rewrites the configuration or its digest.

A crash after the first durable record `Create` but before the established
record fact makes later startup refuse. For disposition, stop the daemon and
keep forward admission disabled; preserve the protected directory, both facts,
quarantine ledger, journal, record/input files and the corresponding effect,
generation and retained-stop inventory. Do not infer child absence or release
UID holds from an initialize marker. The daemon does not repair the ambiguity.
Under exclusive ownership, a separately qualified operator recovery procedure
may validate the existing journal and durable first record, reconcile every
possible effect/hold and establish the independent P17 recovery prerequisite.
Only then may it durably publish `.record-install=established` with atomic
replacement and file/directory fsync, leaving the ledger/history intact, and
restart with the protected configuration and authoritative digest checked.
Otherwise keep the scope quarantined/offline and use the existing retained-stop
and explicit operator-release disposition. Do not delete state or reset facts
to force a fresh boot; reprovisioning needs separately authorized reconciliation.
This unit supplies neither a repair tool nor new operator-release authority.

After quarantine and record-store construction, call `launcher.New` once,
`m0hook.New` once (including its H6 restart pass), then `m0.Attach`. Wrap exactly
the M0 launcher seam supplied by the caller, default `m0.UnpinnedLauncher`.
Before boot admits anything, the stop-identity producer must prove its pin with
`VerifyPinned`, never an invented stop request or identity. All original stop
provenance, repeated-consumption and out-of-journal emergency-stop duties remain
with that producer. A new `StaticGate.Verify(class)` refuses by name by default:
class `start` once per selected startup attempt and before every dispatch,
class `end` before each decoded cancel, before M0 establishes ownership.
Classification uses `m0.DecodeRequest`, never a second parser, before
authentication, correlation checks and replay lookup. Bodies that decoder
rejects, including duplicate members, are never classified. Ordinary verifier
refusals return a definite M0 refusal envelope; fatal duties return no envelope.
Replays of already-recorded dispatches still pass `Verify(start)`; the verifier
unit will decide whether that stays. The future verifier's end class omits only
the start window. No Keymaker verifier is inferred. Every other unpinned service
still refuses as listed in the README.

The command installs SIGTERM/SIGINT handling first, before protected server
load or compose attach/H6, and carries that same context into the snapshot
serve lifecycle without a signal handoff gap. Interrupted load/attach unwinds
gracefully unless a fatal exit duty must be reported. Existing umask protection
still precedes file creation. Must-exit and `transport.ErrFatal` keep exit 1 and
the operator line `journal sync failure: process exit required; recovery is held until host restart or operator disposition`.

Shutdown stops/drains extension admission, closes the M0 extension and P17
record store, then closes quarantine and bootstrap ownership. This order does
not establish child absence or release retained work. The extension observes
one optional exit-latch callback after startup and each Serve; a nonnil cause
returns `transport.ErrFatal` joined with that cause and no success envelope.
Fatal is permanent for the extension and no later request is served. Nil
observer preserves existing behavior; ordinary launcher errors stay unknown.

RO-GEN closes the generation spelling incompatibility. Root Begin accepts only
an opaque, nonzero 32-lowercase-hex generation and forwards its bytes unchanged
to Source and Keymaker; it does not parse, pad, truncate or case-fold. Invalid
spellings retain `run_origin_refused` / `Source input generation` before service
contact. The M0 request/policy shapes are not changed by this client adaptation.

The signed composed control now completes Source prepare/validate, one Keymaker
bind, one committed inner start, durable spawn/input-release recording and an
acknowledged end on cancel, with replay causing no second bind/start. Fake
protocol peers, a synthetic child and explicit fixture export/release evidence
make this composition evidence, not native service, receiver/helper, privilege
separation, input-release or retained-stop/operator-release qualification.
A different valid Source generation binding refuses before Keymaker/records/spawn;
malformed, uppercase and all-zero tokens refuse before any service dial beside
unchanged-32-hex controls. Decimal is now refused by both former boundary layers.
The all-defaults capacity-only audit and every unpinned producer remain closed.

Only the protected operator path may install, replace or remove policy,
association, catalog or qualification pins, after exact-content owner acceptance.
Agent-writable files, a signed request, an active station, key availability and
allowlist membership cannot modify authority. Atomic policy update/removal must
durably advance a monotonic revision/tombstone and invalidate all consumers.
Reusing an ID/revision or reinstalling an older body refuses. The single launch
debit belongs to the policy lineage **and worker generation**; editing revision,
changing an ID or uninstall/reinstall cannot replenish it. A fresh launch scope
requires new explicit operator setup/acceptance, outside this subset.

Local high-water marks cannot detect coherent rollback. Restore/reboot/lost
counter or uncertain currentness holds forward work until main §11.1 external
fence, receiver coverage/inventory and independent restore evidence are reconciled.
Neither a newer wall clock nor an internally consistent snapshot reopens it.
Use main §3.4 qualified time, durable watermark and boot-inclusive monotonic
elapsed checks; unknown time closes forward work and time-based reclamation.
Invalidation of association, epoch, plan/provenance, qualification or tuple also
closes forward admission and initiates the retained stop obligation. Planned policy removal/expiry
and true tuple security revocation have the distinct dispositions in §5;
ordinary invalidation ordering does not provide a security material fence.

1. **Prepare.** Acquire external peer/generation/association/time/catalog/policy
   evidence outside the short dispatcher state lock. Re-read current protected
   policy and exact revisions. Independently validate R, signature, currentness,
   catalog and exact worker active generation, confinement/capacity and tuple.
   Under local serialization revalidate evidence versions/fences; reserve the
   durable request, capacity claim and launch debit before recording launch intent.
2. **Committed start C under selected A.** Re-read policy and qualified
   time/currentness, every pin, stop/cancel intent and original deadline. Journal
   the original in-flight execution/effect identity; unlock for service I/O.
   After root receipt and verified UID/GID drop, before any target, loader,
   constructor, interpreter or executor code, the receiver-owned durable
   original-execution gate serializes C with durable invalidation Rv. At C,
   check exact protected publisher/projection/version, active generation,
   claim/controller, stop markers and validity, and persist consumption of the
   generation's original one-start debit. The dispatcher owns policy evaluation;
   the receiver checks only its selected protected facts. A prepare check,
   dispatcher mutex or unchanged controller epoch cannot supply this gate.
   Expiry or Rv before C refuses; C at expiry or at/after Rv also refuses. Expiry
   closes C locally even during dispatcher failure, without a removal round trip.
3. **Close before publish.** Dispatcher durably journals original removal intent
   and closes admission, releases its state lock, then contacts the receiver.
   Under C exclusion, receiver durably closes its gate and records the original
   stop obligation before ACK. Only then may the protected installer publish
   completed removal. Rv is that durable receiver boundary, not ACK, publication
   or file unlink. All removal/replacement paths participate. Unavailable receiver
   means pending removal with admission closed and debt retained, never completed
   removal or absence. Original removal intent/incident/detection and expiry
   events and clocks survive contact failure, delayed Rv, ACK and publication;
   none may postpone an earlier deadline. An out-of-protocol file edit is an
   incident requiring detection and stop, never retrospective ordered Rv evidence.
4. **C wins before Rv/expiry.** The original execution, its preparatory child and
   every resumer are already-started stop work under A, even if S/exec or receipt
   follows expiry. S is the committed handoff making the prepared child runnable,
   not a promise of first-instruction CPU time. Qualify a bounded C-to-exec-or-abort
   interval and maximum residual worker-code interval after expiry; both must fit
   inside the original component/completion deadlines in §5, with no fresh time
   allowance. Independent receiver stop authority/progress for this exact original
   generation must be explicitly installed and qualified during dispatcher failure;
   ordinary dispatcher/operator authority or retirement cannot be borrowed. A does
   not promise that all worker instructions cease at expiry. Missing interval,
   time or independent-stop qualification keeps M0 closed.
5. **Late refusal and recovery.** A committed start missing its qualified
   completion interval or any original deadline is refused, never launch success.
   Refusal does not prove no effect or absence: retain original receipt,
   incarnation/effect, spent debit, claim, stop debt and reconciliation. A late
   receipt advances only original stop/reconciliation; lost replies remain unknown.
   Deadline miss, crash, restore or conclusive non-start never rearm the debit,
   create a new child/effect or authorize a retry launch. All resumers, recovery,
   invalidators and generation/stop/emergency writers honor the same gate; no late
   resumer may run after confirmed account-wide absence.

The selected receiver design is not supplied by today's helper: current root
receipt/drop and generation writer fences alone do not order policy invalidation
against C or delayed exec. No helper/launcher member, policy/grant verifier,
boolean switch or executable wire grammar is added. The proposed named
`m0_start_invalidated` outcome still needs result/error publication; it is not a
new existing error or a seventh `effect.observe` state.

No dispatcher state lock crosses Keymaker, broker, user-manager, launcher or
control I/O. External evidence and policy projection preparation stay outside
receiver locks; the proposed receiver rank is `effects.lock → gate.lock →
data.lock → receiver start-gate lock`, terminal with no reverse acquisition.
Only bounded protected reads/checks and durable C commit belong in its final
window; no RPC, inventory, child/exec-event wait or storage stall is made safe by
holding locks. Existing 250 ms cooperative reader windows remain additional
limits, not renewed authority. Exact bounded lock integration, writer coverage,
fairness and wait graph remain joint publication/qualification gates.

### Strict native B remains the full-acceptance direction

A separates C from S. C → expiry → S, or C → Rv → S, can leave worker effects
before bounded stop under A; this is not strict B. B requires no native release S
at/after expiry or after Rv wins shared exclusion, including `ExpiryAtNativeS`
and atomic native deadline/fence/release composition. Its non-runnable preparation,
receipt/drop/native-barrier order, all-resumer fencing and native failure evidence
remain the [receiver B gates][interlock], not selected or qualified primitives.
A userspace time check then exec, timer, ptrace stop or EXITKILL alone proves
neither B nor A's residual/completion bounds. The full main contract is unchanged;
B's additional strict gates are later full-acceptance obligations, not M0-A
prerequisites or currently available behavior.

Unknown evidence, missing source/exclusion or failed reads refuse forward work.
Authorized original-record status/reconciliation/stop remain available under main
§8.3 control prerequisites. They do not require resurrecting expired launch
authority: cancel after removal uses the retained original owned-run stop consent,
sound current caller identity and exact original pins; stale caller association
cannot impersonate its successor. If current caller signing is unavailable, the
dispatcher's already-authorized automatic stop or separately authorized operator
stop remains required. This subset grants no new status or operator wire grammar.

## 4. Durable replay and unknown effects

Journal canonical R and its digest, authenticated caller tuple, all pins,
original deadlines, reservation/debit, original run identity and each distinct
dispatcher-owned launcher/stop effect identity before its call. Main §4.5's
original effect derivation applies to those dispatcher-owned operations; receiver
encodings remain gated. Worker-owned broker lease effects are separate: neither
this journal nor an inherited full-path effect name authorizes dispatcher broker
calls. Only the allowlisted worker kernel peer UID executes `lease.request` and
normal `lease.release`; dispatcher is never added to that allowlist. Any required
authenticated executor/intake evidence handoff must be separately published and
qualified; no request field or broker method is invented here. Serialize
competing IDs and both operations against the same lineage.
Reservation holds the sole entitlement; C durably consumes its original debit.
Failure or confirmed absence does not give another attempt a new entitlement.

The replay key is `(caller association ID, caller generation, request_id)`.
Same ID and identical canonical R (auth verifies independently) recovers only the
original record after current gates for the requested operation. Changed body
is `request_id_conflict` before any new effect; the remembered body is immutable.
Current authority is needed for any new C or other unperformed forward admission;
replay of a receipt cannot renew it. Completing the original already-committed C
within its qualified handoff/residual and original deadlines is not a new C;
expiry still closes every new start and retains the original stop obligation. Safe original-record observation/stop is distinct from
forward recovery. A second dispatch ID, concurrent admission, a new native signer
ID or a replaced policy cannot reset the worker's one-start ledger.

If a crash occurs before durable reservation, no effect could have been issued.
After reservation but before intent, retain the debit and reconcile; after durable
intent, unknown receipt or unknown receiver observation retains the claim and
same effect ID. Query/reconcile the original receiver effect; **do not issue a
second physical launch, recreate/adopt the account or allocate a replacement**.
A same-effect retry may resolve/recover only the original transaction under
qualified durable one-launch replay/observation and all §3 interval, deadline
and resumer fences; it cannot authorize another physical launch. Otherwise
observation/stop only. Lost or restored evidence cannot be classified as never launched. The
receiver's durable deduplication must ensure at most one child start under every
crash cut; inability to establish that closes this receiver selection.

## 5. Planned policy removal, security response and retained cleanup

### M0PolicyClassificationClockSecurityResponse

Following the [landed first-run selection][manager] and [receiver design][interlock],
**planned M0 launch-policy removal or expiry is not security revocation and is
not an EIC-10-05 P7 trigger** (G2 branch B). Removal intent closes dispatcher admission immediately; durable Rv closes new C,
and original expiry closes C locally. Each triggers durable account-generation-wide
stop, retaining any C-winning original execution and every resumer. The
**proposed M0 removal/expiry-to-absence envelope** is observation <=30 s plus fenced confirmed absence <=270 s, with
`detect_seconds + stop_seconds <=300 s`, or shorter actual qualified bounds.
Its original absolute deadline starts at the original authoritative removal
intent/incident or original expiry, not delayed Rv/ACK/completed publication.
An independently already-outstanding real P7 remains <=300 s from its original
detection event, or its shorter selected-profile bound; planned removal/expiry
does not start that clock. Retain the separate original events and absolute
deadlines across retry/restart and enforce the earliest of any outstanding original P7, the M0
envelope, **detection +270 s component**, or any shorter applicable qualified
deadline. Neither receipt arrival nor delayed polling may reset them. Detection includes watch/poll loss, time
scheduling and restart; stop includes in-flight launch reconciliation, fencing
further starts, all descendants and the receiver's quiet period/confirmation.
The ordinary minute sweep alone proves no such bound. For removal at t=0 and
original M0 detection at t=20, with no real P7 outstanding, the M0 total is t=300,
but the detection +270 component requires **t=290**. An independently outstanding
real P7 deadline at t=320 still leaves t=290; an earlier P7 deadline at t=280
requires t=280. Completion at t=291 misses the t=290 component even though it
meets the total t=300. A shorter qualified bound also wins.
Detection <=30 s remains independently required. Restart, delayed receipt and
C-to-exec/residual progress cannot reset any component or completion clock.

The M0 envelope is a new conditional joint worst-case qualification requirement,
**not a demonstrated capability or a source P7 guarantee**. Before activation,
time/currentness and receiver owners must qualify the source, maximum detection
delay, scheduler/failure assumptions, stop duration and independent stop progress
during dispatcher failure for the exact Linux tuple. If the actual path cannot
meet every applicable deadline, refuse activation; do not reset it or declare
success. On overrun/unknown, retain the live claim, durable stop/incident and
cleanup debt, close affected forward work and report the unmet bound. Stop work
continues under its original authority and effect; unknown time does not delay
an explicit authorized stop. Delivered vendor tokens retain their real lifetime;
stopping processes does not retract credentials already issued.

Planned removal/expiry leaves credential material issuance under the broker's
own protected policy until worker stop. It supplies no NEW-lease interception,
owner-intent broker field, new operation, mandatory immediate global Replace or
automatic material/security fence. Existing broker security, cancellation and
final currentness/material-release predicates remain mandatory; this classification
grants no access beyond qualified original bounds. Lease release/expiry and
delivered-token debt have separate dispositions below.

**True EIC-10-05 tuple security revocation remains a closed gate for M0.**
The canary claims no EIC-10-05 conformance. On an actual security event the
operator stops the worker and uses the actual broker policy Replace operation.
Issuance between detection and successful Replace commit is a named real window,
not an immediate fence. Missing/failed Replace or stop retains unknown outcome,
original effects, claim and cleanup debt; an attempted action is not successful
cleanup. Automatic authoritative-event-to-final-material-fence joining is a
post-M0 broker-owned contract and native qualification obligation. Owner risk
acceptance of this canary limitation remains pending; technical selection grants
no owner, root, install, signing or activation approval.

Only fresh, receiver-owned **account-wide confirmed absence**, with generation,
complete launch-history coverage, start/ingress fences and required quiet period,
can release a live claim. Leader exit, PID disappearance, socket EOF, expired
lease, missing receipt and failed inventory are not absence. A known never-started
generation still needs authoritative no-launch coverage and fenced absence;
unknown history cannot use that disposition.

### Broker-v0 ownership, horizon and cleanup debt

[Pre-B0 setup](m0-bootstrap-budget.md#5-pre-b0-setup-and-worker-status-boundary)
is a separate one-time operator procedure: admission closed, no active leases
anywhere on the broker, separately bounded setup time/currentness and exact
worker/foreign-peer status evidence. No broker operation occurs in B0–B5.
Emergency global Replace is an actual daemon capability: it invalidates
old-generation leases and connections. Active-lease emergency use is distinct
from the authorized no-active-lease setup procedure, and planned M0 removal/expiry
does not require it. Replace does not supply delivered-token revocation or a
successful old-generation release receipt; failed release cleanup retains its
original debt. The setup prohibition is not a claim that the daemon rejects
active-lease Replace. All original horizon (b) requirements below remain.

[Broker v0][broker] §§2, 6–7 defines worker kernel-peer ownership. On normal
teardown the worker confirms its private app-server descendants absent and
releases its own lease. An admitted foreign peer receives `lease_not_owner`;
dispatcher is not an allowlisted worker peer and cannot release on its behalf.
If the worker is killed/dies before cleanup, no callback or release is assumed:
the lease remains until original `authorized_until`, at most the actual selected
broker-policy limit (default 3600 s). Disconnect/restart neither releases nor
extends it; `lease.renew` never extends `authorized_until`. Expiry without release
is an expiry disposition, **not successful release**. Release also cannot revoke
a delivered vendor token's independent lifetime.

V0 provides no dispatcher lease-state observer or foreign-owner release receipt;
its peer-scoped account `status` is not lease observation. Dispatcher records
receiver-owned stop and confirmed account-wide absence, the sound original lease
horizon when known, and retained evidence/debt. It asserts no broker-side cleanup
receipt. A worker's absence proves neither a prior release nor absence of an
earlier delivered credential. Stop/absence progress is independent of lease
and delivered-token disposition.

Horizon provenance is a cross-owner publication/qualification gate. The only
selected path is **horizon (b): a protected actual broker-policy maximum plus
qualified complete coverage of every possible original, in-flight and latest
worker lease issuance**. Retain each original occurrence and authorized absolute
horizon, actual policy-version history and qualified sound time. Coverage must
join latest-possible issuance, including in-flight calls, to receiver stop and
account-wide confirmed absence; first start plus a guessed hour is insufficient.
Separately published/qualified authenticated executor evidence may corroborate
this coverage, never replace it or constitute an alternative horizon path.
The M0 validity window does not shorten a lease. No caller assertion, guessed
request field, missing worker or unavailable v0 API can establish this evidence.
The exact intake/coverage/time join must be selected by executor, broker and
dispatcher owners before it is used. Incomplete possible-issuance coverage or
unknown horizon, time or provenance retains unknown lease debt and refuses its
completion while required stop continues, even if the worker is absent and
schemas pass. A sound
elapsed upper horizon plus receiver absence may record lease expiry, never a
successful release or vendor-token revocation; unknown token disposition remains
separate retained debt. No unavailable observer is a prerequisite.

There is **no binding to unbind**. Additional helper cleanup, if needed, remains
a publication gate. The preprovisioned station and its enrollment remain
operator-owned; M0 does not retire it or borrow station-v0 operator-only
export-waiver retirement. Output/export/custody and ordinary
retirement guarantees remain intact; if the selected worker produces workspace
content, preserve the full export/custody obligations and keep the claim/debt
until their separately published path is available. A terminal execution verdict
does not assert cleanup completion while any obligation remains.

## 6. Adjacent contract vectors

All examples are synthetic and nonproduction. The [fixture index][fixtures]
distinguishes executable **shape checks** from these semantic obligations;
neither is runtime qualification. [Paired inert byte/semantic records][counterexamples]
make selected counterexamples reproducible as documents, with runtime enforcement
PENDING. Each negative below has its adjacent allowed control, conditional on
every gate in §7. Schema-valid semantic violations must still refuse at
signer/receiver; a schema pass never attests authority.

| Surface | Positive control | Negative/control boundary |
|---|---|---|
| Exact scope | Current pinned caller/key/generation, worker plan/profile/harness and dispatch; owned original cancel. | Wrong caller, key, generation, receiver, epoch, plan or op; caller command/environment; delegation or cross-run stop: refuse before effects. |
| Canonical values | Valid R yields domain + one LF + CCJ-1(R); input key order/whitespace variation yields identical bytes. | Extra/nested member, duplicate alias, float/-0, wrong domain/hash/wrapper, final LF, base64 padding/tail bits or native-frame confusion: cannot sign/verify the candidate. |
| Current policy / selected A | Current exact protected projection at prepare and C after receipt/drop, before target code. | Rv/expiry before C or equality refuses; C before either retains original stop through delayed S/receipt. B separately keeps strict S/expiry. |
| A bounded handoff / late refusal | C before expiry, delayed S within qualified handoff/residual and every original deadline. | Missing interval/deadline refuses success while original effect/claim/debit/stop remain; no fresh child or retry launch. |
| All-resumer closure | Original committed incarnation resolves under stop fences; confirmed whole-account absence prevents every later resume. | Recovery, generation/stop writer or second resumer bypasses C/Rv or resumes after absence: forbidden. |
| Removal / original clocks | Journal/close admission, unlock, durable Rv plus stop, ACK, then completed publication. | Contact failure is pending; delayed Rv/ACK or out-of-protocol edit cannot assert completed removal/absence or move original deadlines; earliest component wins. |
| M0PolicyClassificationClockSecurityResponse | Planned removal/expiry closes starts under A, retains original 30/270/300 components and any earlier outstanding P7/shorter deadline; material stays under broker policy, with separate lease/token debt. Real security event: operator stop plus broker Replace with an explicit detection-to-commit issuance window and closed EIC-10-05 gate. | False P7 trigger or later-P7 extension/reset, implicit material fence/NEW-lease hook, immediate security conformity, setup/emergency conflation or erased failed-release debt: forbidden. See the adjacent named inert groups. |
| One launch/replay | One reserved dispatch; exact retry recovers original effect with current gates. | Concurrent/changed-body/new-ID/reinstalled-policy/crash replay cannot launch twice; unknown receipt retains claim and original effect. |
| Recovery boundary | Sound owned cancel/recovery after launch authority closes, or independently authorized operator stop. | Reconciliation renews launch, old signature/association revives authority, cancel selects a foreign account, unknown time blocks required explicit stop: forbidden. |
| Lease owner | Allowlisted worker requests and normally releases its own lease after teardown. | Dispatcher lease call or admitted foreign-owner release; no allowlist widening; foreign release is `lease_not_owner`. |
| Selected importing custody | WC1/common synchronous independent G17 with separate old-instance fence; separate Keymaker bootstrap/custody, broker time settlement and broker D5 histories/identities/independently operator-approved genesis and all provider/protocol/native gates. | Manual/local-watermark substitute, D5-for-time or time-for-D5 history, or missing/mismatched/shared genesis cannot authorize effects, writes, signing or releases; ordinary manager operations gain no custodian dependency. |
| Killed worker/horizon | Horizon (b): protected actual policy maximum, complete original/in-flight/latest issuance coverage, original absolute horizons and sound time plus receiver account-wide absence; retain evidence and separate debt. | Executor-only report or uncovered issuance cannot clear lease debt; required stop continues. Lost release/EOF/restart is not release, renewal cannot extend the original horizon. |
| Absence/cleanup | Fenced generation-bound account-wide quiet-period absence; lease/token and custody dispositions tracked separately. | Leader exit/lost receipt/failed read becomes absence, expiry or missing release is success, automatic station retirement/unbind or discarded debt: forbidden. |

## 7. Publication, review and activation gates

| Owner(s) | Required exact input; current disposition |
|---|---|
| Integration, Architect, dispatcher and Keymaker | Accept this single policy/request/signing-plan variant and numeric bounds; jointly amend SC-Q2 for existing-worker dispatch/cancel rather than the full provision/start form. Exact candidate consumer review pending. |
| Operator/curator and installer | Owner acceptance, protected install/removal transaction, source path/ACL/atomicity/currentness/audit, acyclic purpose-6 public staging, rotation and association version/epoch rules. No install/activation performed or delegated. |
| Keymaker + generation/time/storage owners | SC-Q4 final generation/exclusion/release ordering and authority time; SC-Q6 owning-store budget, durable result/replay/audit and old-reader fence; qualified first caller and conditional published [purpose-6 bootstrap at 79b37d2](https://github.com/relux-works/swarma-keymaker/blob/79b37d2d0a6fbacae1174ec97c8f07d09681fe00/spec/keymaker-station-caller.md#41-m0-operator-bootstrap-transaction), still unqualified/unactivated. The [dispatcher B0–B5 budget contribution](m0-bootstrap-budget.md) pins Keymaker 1453290's original 120 s proposal, dispatcher candidate endpoint/load ceilings and incomplete joint workload/separate pre-B0 worker-status setup budget. No implementation, qualification, activation or guessed bootstrap fields. |
| Keymaker bootstrap/custody + broker time settlement + broker D5 importing owners (two separate broker scopes) | Required WC1/common synchronous independent G17 plus separate old-instance lifetime fence; each scope has isolated owning histories/enrollment/identities/installation bindings/namespaces/credentials/original occurrences and independently operator-approved first checkpoint/genesis. Provider/placement, typed protocol/profile, complete originals/currentness/continuity, endpoint/load and exact native qualification remain closed. Ordinary manager operations are excluded; later PC2 MC-Q5/A3d-2 is separate. No manual/watermark or cross-consumer substitute in either broker direction; missing/mismatched genesis cannot use automatic re-enrollment, first-seen adoption or the other scope. |
| User-manager/launcher + dispatcher | Selected launcher/FD3/exec-profile/start/stop/effect.observe intake and encoding, protected setup/creating-dispatcher association, receiver-owned durable original-execution C/Rv/expiry interlock under A, qualified C-to-exec-or-abort/residual intervals, all-resumer closure, one-launch replay, original-effect/account-wide absence and independently installed stop within every original P7/M0 component/completion deadline. Protected control/store/authentication/outcome, currentness/retention/restore/time and bounded lock integration remain unpublished/unqualified; B additionally retains strict native S/deadline composition. No launcher policy evaluator. Station `active` alone is insufficient. |
| Dispatcher + user-manager + credential-broker + catalog/policy owners | Review the separate [DRAFT executor-entry /1](m0-executor-entry.md), closed grammar and inert controls; select exact content before adapter code. Typed catalog/install/provenance, native FD3/C/exec/clock qualification and authenticated Result joins remain gates. |
| Credential-broker + dispatcher | Pin the `run-claude` broker contract (slice S3a) at a named commit and record that pin in the [executor-entry spec](m0-executor-entry.md) §4: option set, complete status set without 78, and the reach/custody/placeholder/settings admission gate. Not pinned; `run-claude` stays refused. |
| Executor/harness/catalog owners | Compatible one-shot executor intake and full typed immutable plan/profile/harness/exec-policy provenance/body/ownership, confinement, export/custody and finite evidence/result accounting. No arbitrary command or opaque enabled plan. |
| Broker + integration | **Actual v0 activation**, worker-owned versioned lease adapter, exact worker-only allowlist UID mapping and broker's actual final credential/currentness predicates; only horizon (b), protected actual policy maximum with complete original/in-flight/latest issuance coverage, actual policy history, original occurrences/absolute horizons, sound time, receiver stop/account-wide absence and separate retained lease/token/custody debt. Executor evidence is corroborative only. No dispatcher broker observer or foreign release. All required broker activation audit rows remain, not just peer + runner; no copied credential fallback. |
| Native qualification authority + all consumers | Exact Linux OS/kernel/architecture/hardware/firmware, boot/node/backend/image/profile, all service/harness/collector revisions, source/test/config, caller/worker UID-generation/association/epoch, units/users/groups/FDs/sandbox/mount/env/network tuple. Qualify the production entry and original M0 stop clocks plus any independently outstanding P7, not support labels; true security revocation stays closed as stated in §5. Linux CI is not deployment; macOS stays native-gated. |
| Keymaker, Architect, broker, integration; independent reviewer; curator | Review exact draft and subsequent implementation separately, preserve negative controls and exact evidence, then explicit owner acceptance before installation/activation. No consumer or independent acceptance is claimed here. |

[ccj]: https://github.com/relux-works/swarma-registrar/blob/5f44ebf/spec/registrar.md#21-json-and-ccj-1-normative
[codec]: https://github.com/relux-works/swarma-registrar/blob/5f44ebf/ccj/ccj.go
[envelope]: https://github.com/relux-works/swarma-registrar/blob/5f44ebf/envelope/envelope.go
[fixtures]: ../fixtures/m0-launch/README.md
[manager]: https://github.com/relux-works/swarma-user-manager/blob/5db5b8bbeba8af7ed6962c87586ef48a0cd5b0b3/docs/design/first-run-launch-stop.md
[broker]: https://github.com/relux-works/swarma-credential-broker/blob/8709e215a1b6bc7f42094a14e4a5620e2baed290/spec/v0-codex-refresher.md
[counterexamples]: ../fixtures/m0-launch/counterexamples.md
[interlock]: https://github.com/relux-works/swarma-user-manager/blob/5db5b8bbeba8af7ed6962c87586ef48a0cd5b0b3/docs/design/m0-start-invalidation.md
