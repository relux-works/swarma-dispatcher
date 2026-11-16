# Host registration: `host-registration/1` (minimal form)

Status: draft specification. This file defines the minimal static form of the
dispatcher contract for one Session Host on the same machine: registration,
caller authentication, epochs and fencing, `host_start`/`host_stop` ownership,
loss and reconnect. It defines no implementation and enables nothing by
itself. The session-host backend stays behind the activation gate of
[dispatcher §12](dispatcher.md#12-session-host-backend-later-activation-gate)
(including its publication gate I4) and §16 question 3; this file is the
registration and fencing part of that joint publication, not a substitute
for the rest of it. Where this file names a member, code, phase or bound
that no earlier contract named, the naming sentence is in this file and the
item is marked as defined here; everything else that is not settled is an
OPEN item in section 11.

The key words MUST, MUST NOT, MAY and SHOULD have their RFC 2119 meanings.
Bare imperatives are requirements.

## 1. Purpose and ownership

One dispatcher and at most one Session Host ("the Host") run on one machine.
The Host holds the life of a logical session: its input queue, its delivery
fence and its liveness. The dispatcher owns the physical start and stop
authority for the runner process of that session and the record of every
standing run.

Ownership is fixed by the operator's ruling of 2026-11-16 on the open
question of which record is authoritative for a standing run:

| Record or fact | Owner | Everyone else |
| --- | --- | --- |
| Station description: environment, model and effort, profile, clone, identity | The workbench registry, where the book puts it | Read-only to the dispatcher. This file defines no registry read format. |
| Standing-run record: start/stop journal, operation id, epochs, unknown-outcome reconciliation, current state | The dispatcher, as the effect owner | Nobody else writes it. |
| Session liveness | The Host holds it and reports it | Reports are information. They never write the record (section 6.5). |

The book basis of the ruling is chapter 7 of the Swarma book: the section
"Верстак" ("Workbench") puts the launch description in the workbench registry
record, the section "Пробуждение и перенос контекста" ("Wake-up and context
transfer") has standing sessions raised through the dispatcher, and the
section "Диспетчер и транзакция запуска" ("Dispatcher and the one-shot
transaction") gives the dispatcher the start transaction, the effect
identities and the rule that a lost reply is reconciled against the same
effect and never by launching a second program.

The minimal form is accepted as the first step: a static registration entry
in protected configuration, kernel peer credentials and pin checks on local
Unix sockets, and epoch fencing. Section 10 lists what a later version
adds. Nothing in this file designs the later versions.

## 2. Sources

| Source | Pin or location | What this file takes from it | Status at that pin |
| --- | --- | --- | --- |
| Swarma book, chapter 7, `book/ru/07-zapusk-i-host-sessiy.md` | swarma-docs `006f653dbe914e23a40d37cd2790528ae795151e` | "Верстак" (registry record holds the launch description); "Пробуждение и перенос контекста" (standing sessions raised through the dispatcher; an operator-stopped session stays stopped); "Диспетчер и транзакция запуска" (one identifier per external effect; a lost start reply reconciles the same effect; unknown outcome is not absence); "Служба, раннер и единственный писатель" ("Service, runner and the single writer": the Host persists intent with delivery closed, then asks the dispatcher for `host_start`; only the configured host service account may call that path); "Разрешение на преемника" ("Permission for a successor": `host_stop` goes through a journaled stop with confirmed absence and grants no next start); "Забор, парковка и перезапуск" ("Fence, parking and restart": the Host itself sends no process signals) | Book text. Section names are cited in Russian with an English rendering where this file first uses them. |
| The operator's ruling of 2026-11-16 | Private input to this specification | The ownership table of section 1 and acceptance of this minimal form (static pin in protected configuration plus epoch fencing; dynamic registration later) | Decided. No identifier from it is reproduced here. |
| station/1, `protocol/station.md` | curator-spec `3463191e69fbc8a3c3c992547fec0fa8a68b4b05` | Actor `DI` (dispatcher: holds the session record, owns physical start and stop authority); `SR1`-`SR6`; records `R03` (phases `lease_requested`, `start_requested`), `R09`, `R10`, `R23`; the `held:start` row and U5 ("never sends a second start request"); codes `station_state_stale` and `station_session_journal_fenced`; the "starter crash" event column; clocks `start_detect` and `end_to_absence` | Unreleased draft. Where it is not settled this file says so and opens an item in section 11 instead of deciding for it. `SR6` is still OPEN OQ18 at this pin; the ruling above is the input that answers it for this contract. |
| CIP-0024, `cips/CIP-0024-agent-stations.md` | Same curator-spec pin | Owner decision 9 (registry and lifecycle ownership; the dispatcher retains allocation and execution orchestration); "Ownership and records" (no direct bypass of dispatcher physical-start authority); "Lifecycle and failure handling" (a lost response does not authorize another start; `unknown` retains claims; stopped only after authoritative termination) | Draft; its owner decisions are decided and are not reopened. station/1 is the normative successor of its lifecycle and ownership sections. |
| Session Host documents | swarma-session-host `b132e5528d4c7648dae632c64668e1cf70d8730e`: `docs/selected-manager-handler.md`, `docs/host-dogfood-composition.md`, `docs/prototype-m2.md` | What the Host side exposes today on its own seams: a private manager socket with durable operation reconciliation, status by exactly one session handle or operation id, unknown start outcomes never resubmitted; and its stated limits (no dispatcher admission, no caller-scoped authentication, no descendant confinement claim in the prototype slices) | No dispatcher-facing Host wire is specified there. Every Host-side member this contract would need beyond the ids and outcome values of section 6 is OPEN for the Host owner (section 11). No Host field is invented here. |
| [Transport subset `dispatch/1`](transport.md) | This repository | Protected configuration snapshots and canonical bytes; the principal pin (`dev`, `dir`, `ino`, `uid` as one pin); kernel peer uid (`SO_PEERCRED`) and endpoint identity checks; the "Managed M0 callers" exception as the precedent for a separate protected caller file; the configuration digest defined for the daemon configuration unit | Reference subset only. It admits no host operation; section 4 of this file reuses its mechanisms without widening that subset. |
| [Dispatcher specification](dispatcher.md) | This repository | §4.1 journal-before-effect and in-flight markers; §4.5 effect ids and receiver `effect.observe`; §4.6 observation classes; §7.1 reconciliation; §8.1 the gated `host_start`/`host_stop` rows and their members; §8.2 result envelope; §11.1 state loss and restore; §12 session-host backend, successor table and publication gate; Appendix A refusal codes | Design contract. The §12 backend is disabled until joint publication and qualification. |
| [Local durable journal](journal.md) | This repository | Append, fsync commit, two-pass replay; replay retains every complete unacknowledged frame; a lost reply authorizes no new intent or effect | Normative draft. Payload schemas stay with their owning contracts; section 7 of this file owns its phase names. |
| [Byte frames](frame.md) | This repository | One frame is a 4-byte big-endian length and 1..131072 payload bytes; a framing error discards the stream | Draft library contract. It authenticates nothing. |
| [M0 launch supplement](m0-launch.md) | This repository | Closed-member configuration beside the main contract; replay key discipline (same id and same canonical body recovers the original record; a changed body is `request_id_conflict`); integer bound 1..9007199254740991 for revisions and epochs | Draft proposal, inactive. Cited for mechanism shape only; no M0 authority is imported. |

## 3. Two directions, one machine

Both directions are local Unix stream sockets on the same machine. Cross-host
transport, signed envelopes and leases across hosts are a later version
(section 10) and are not designed here.

| Direction | Connector and listener | Authentication of the caller | Authentication of the receiver before any request byte is written | Operations |
| --- | --- | --- | --- | --- |
| Host to dispatcher | The Host connects to the dispatcher's socket (`dispatch/1`) | Kernel peer uid from `SO_PEERCRED` must be the pinned Host service uid, and the pinned principal directory of section 4 must pass the transport pin checks before hello and again before the result. The Host is a pinned principal of the dispatcher's transport and reaches the dispatcher in no other way. | The Host pins the dispatcher endpoint exactly as a transport client does: directory and socket identity, service owner, group, mode and socket type before connect, re-stated after connect, then the service uid from `SO_PEERCRED`. | `host_start`, `host_stop`, `host_liveness` (sections 6.2, 6.3, 6.5) |
| Dispatcher to Host | The dispatcher connects to the Host socket named in the registration entry | The dispatcher is the caller; the Host decides its own admission of that peer. What the Host checks is the Host owner's contract (OPEN H2). This file requires the dispatcher to connect only from its service uid and to present no identity inside the request body. | The dispatcher verifies, through a pinned parent directory with no symlink component: the path is a socket, its owner is the configured expected owner, and after connect the kernel peer uid is the pinned Host service uid. Any failure is `host_socket_untrusted` and zero request bytes are written (section 8). | `attach`, `query_operation` (sections 6.1, 6.4) |

Rules that apply to both directions:

- Identity comes only from kernel peer credentials and the pins of section 4.
  A uid, name, pid or generation inside a request body selects nothing and
  authenticates nothing. Same-uid processes are the same principal; this is
  the stated transport limitation, unchanged here.
- Framing is [the byte-frame contract](frame.md): payload 1..131072 bytes.
  Socket adapters keep the transport obligations of
  [transport.md](transport.md) for descriptor-aware receives, ancillary-data
  refusal, close-on-exec descriptors and monotonic deadlines. A framing
  error closes the connection with no result for the incomplete message.
- Bodies are CCJ-1 canonical bytes in the ASCII subset of the transport
  contract: string content `0x20..0x7e` excluding quote and backslash, no
  escapes, BOM, whitespace outside strings, duplicate or unsorted keys or
  trailing bytes; integers are shortest unsigned decimal. A byte-level
  bounded scan runs before recognition; parse-then-normalize is not
  acceptance. Depth and member rules are in section 5.
- Host operations in this minimal form are not caller-signed requests.
  Decided point: the authentication is the kernel peer uid plus the pin
  checks above, rechecked per operation. This is a stated departure from
  the signature rule for mutating `dispatch-op/1` requests in dispatcher
  §3.3, which does not apply to the operations of section 6. Signed
  envelopes are a later version. Whether joint publication should require
  signatures in addition is OPEN S7; the v1 design does not.
- No state-transition lock is held across a call in either direction
  (dispatcher §4.1, R-LOCK). Under the lock the dispatcher fixes canonical
  arguments and journals intent; it releases the lock, makes the call, then
  revalidates epochs and fences before recording the result. A stale late
  result may resolve historical evidence; it cannot admit work, open
  delivery or resurrect a cancelled start.
- Every call has a finite monotonic deadline. The numeric deadline values
  for this seam are not defined here (OPEN S3). A timeout after request
  bytes were written is an unknown outcome (section 7), never a refusal
  and never a reason to send the effect again under a new identity.

## 4. The registration entry: protected configuration

There is no dynamic registry, no self-registration and no request-supplied
identity. The one Host is a static entry in one protected configuration
file. The file is selected by an explicit daemon flag named here
`--host-config` (the parallel of `--m0-config` in
[transport.md](transport.md#managed-m0-callers)); without that flag no
host is registered and every operation of section 6 is closed.

The file follows the protected snapshot rules of
[transport.md](transport.md), "Protected configuration and unmanaged
principals", without change: one explicit absolute path, loaded once before
listen and connect, opened through pinned root-owned ancestors that are not
writable by group or others and contain no symlink; the file is regular,
root-owned and single-link, server mode `0440` with group equal to the
daemon's effective gid; at most limit+1 bytes are read and oversize refuses;
device, inode and size are verified with `fstat` and a path recheck before
the snapshot is accepted. No process repairs ownership, modes or content
into compliance, and no environment value, account lookup, discovery,
compiled default or reload supplies or overrides any part of it.

Bytes are canonical ASCII configuration JSON under the same rules: no
whitespace outside strings, no escapes, no duplicate or unsorted keys, no
BOM or trailing bytes; numbers are shortest unsigned decimal integers. The
byte cap is 4096 and the maximum container depth is 3, both reused from the
M0 protected file. Every member below is required and no others are
allowed, at any depth. A missing, unknown, duplicated or out-of-range
member, an unreadable, empty, oversize or malformed file, or a wrong owner,
mode or group refuses daemon startup or attach with a local failure and
zero protocol bytes, exactly as a transport configuration failure does.
This refusal happens before serving; it is not a wire result.

| Member | Type and bound | Meaning |
| --- | --- | --- |
| `v` | integer, exactly `1` | Version of this configuration form. |
| `kind` | string, exactly `swarma-dispatcher host-registration/1` | Discriminator of this file. The string is defined here, in the repository-qualified spelling of dispatcher §3.3 (D-WIRE-NAMES), parallel to `swarma-dispatcher m0-config/2`. Any other value refuses; it never acquires a new meaning by reuse. |
| `host` | one principal object with exactly `dev`, `dir`, `ino`, `uid` | The pinned Host principal, in the exact shape and bounds of [transport.md](transport.md): `uid` is the Host service uid, and `dir` with its `dev` and `ino` is that principal's pinned directory; uid and directory identity are one pin. There is one such object, never an array: at most one Session Host per dispatcher in v1. The Host uid is nonroot, differs from the dispatcher service uid and is outside the managed agent range `30000..59999`, under the same deployment rule as every transport caller pin. |
| `socket_path` | absolute ASCII path, 1..512 bytes, no empty, `.` or `..` component, no repeated separator, no trailing slash | The Host socket the dispatcher connects to for section 6.1 and 6.4. Path syntax and the 512-byte bound are reused from the transport principal `dir` rule. A path the platform cannot represent as a Unix socket address fails the connect checks of section 3; it is never truncated into a different address. |
| `socket_owner_uid` | integer `1..4294967294`, equal to `host.uid` in v1 | The expected owner of the socket file at `socket_path`, checked before connect. Equality with the service uid is a v1 rule of this file; a distinct socket owner is not designed here. |
| `installation_generation` | integer `1..9007199254740991` | The generation of this installed entry. It is the first half of the registration epoch (section 5). |

Changing the entry is a new installation generation. The dispatcher journals
the pair (`installation_generation`, digest of the configuration bytes) at
the first accepted attach under that generation. On a later load:

- the same generation with different bytes refuses startup and attach with
  `host_registration_conflict` (defined in section 8), effect: none;
- a lower generation than the journaled one refuses with
  `host_registration_stale`, effect: none;
- a higher generation is a new entry: the old registration epoch is dead,
  open operations stay under their original records until reconciled, and
  no operation under the new generation is admitted before a successful
  attach under it.

The entry is part of the daemon's protected configuration digest. The
transport contract defines that digest over the server and M0 files for the
separate daemon configuration unit. When `--host-config` is attached, this
file defines the extended form for that unit:
`SHA-256(CCJ-1({"host_registration":<sha256 hex of the host-registration bytes>,"m0":<sha256 hex of the M0 config bytes>,"server":<sha256 hex of server config bytes>})`.
Without `--host-config` the existing two-member definition is unchanged.
The transport-only reference subset, which has no host operation, is
unaffected either way.

The dispatcher validates the Host principal pin with the same checks as a
transport caller pin (`principalPin` in transport terms): at configuration
load, before hello and again before every host operation result on the
Host-to-dispatcher direction, and before every dispatcher-to-Host result is
acted on. The directory is opened through nonsymlink ancestors owned by
root or the Host uid and not group/other-writable; `fstat` must show the
configured device and inode, the Host uid as owner and mode exactly `0700`.
A missing, unreadable or replaced pin refuses; no new or empty directory is
accepted as enrollment. Root-controlled enrollment must not recycle the
configured Host uid while its old pin survives; that non-reuse rule is the
deployment invariant of the transport contract and applies unchanged.

## 5. Common wire members and the two epochs

Every request body in section 6 is one closed CCJ-1 object. Every member
listed for an operation is required; unknown, missing, cross-operation or
out-of-bound members refuse with `request_invalid`, effect: none. No member
is optional in v1 unless its row says so. Container depth is at most 3 (the
outer object, one array or object inside it, one object inside that), which
every shape below fits.

Common members and shared bounds:

| Member | Bound | Meaning |
| --- | --- | --- |
| `v` | integer, exactly `1` | Wire version of this contract. |
| `op` | one of `attach`, `host_start`, `host_stop`, `query_operation`, `host_liveness` | Operation discriminator. `host_start` and `host_stop` are the existing gated rows of dispatcher §8.1; `attach`, `query_operation` and `host_liveness` are operation names defined here. An `op` value sent in the wrong direction refuses `request_invalid`. |
| `request_id` | string of 1..64 bytes matching `[A-Za-z0-9._:-]`; generated ids are 32 lowercase hex characters | Correlation and replay identity of this request, in the grammar of [transport.md](transport.md). It is scoped to the calling principal. |
| `registration_generation` | integer `1..9007199254740991` | Must equal `installation_generation` of the accepted entry. On `attach` it is the value being established; on every other operation it must equal the current journaled generation. |
| `attach_counter` | integer `1..9007199254740991` | The dispatcher's journaled attach counter (below). On `attach` it is the new value; on every other operation it must equal the current journaled counter exactly. |
| `state_version` | integer `1..9007199254740991` | The station state version carried as the operation epoch (station/1 SR3, R03). Required on every operation that names a logical session: `host_start`, `host_stop`, `query_operation` and each entry of `host_liveness`. `attach` names no session and carries none; whether station/1 wants a session scope on attach is OPEN S1. |
| `logical_session_id` | string of 1..64 bytes matching `[A-Za-z0-9._:-]` | The logical session, as named in dispatcher §12. The dispatcher finds the session-scoped host admission permit by this value; the permit reference encoding stays unpublished (§16 question 3) and no permit member is added here. |
| `execution_id` | string of 1..64 bytes matching `[A-Za-z0-9._:-]` | The execution claim and launcher/stop receipt identity (dispatcher §4.5). This file bounds its spelling on this wire only; its construction stays as §4.5 leaves it. |
| `controller_epoch` | integer `1..9007199254740991` | The Host's per-session controller fence epoch (dispatcher §12). The integer spelling is defined here for this minimal form; the Host owner must confirm its epoch is a monotonic integer of this range (OPEN H5). |
| `op_id` | string of 1..64 bytes matching `[A-Za-z0-9._:-]` | The idempotent operation identity of one `host_start` or `host_stop`, the `op_id` of dispatcher §12. The pair (`logical_session_id`, `op_id`) is the durable replay key of that operation. |

Responses reuse the result envelope of dispatcher §8.2 on both directions:
`{v, request_id, ok: true, result: {...}}` or
`{v, request_id, ok: false, error: {code, message}}`, with `request_id`
echoing the request. `message` is human text of 1..128 bytes in the same
ASCII subset and carries no semantics; a receiver that acts on message text
instead of `code` does not conform. The `result` members of each operation
are closed sets defined in section 6. No other envelope member exists in v1.

The two epochs are distinct values and neither substitutes for the other:

| Value | Owner | Scope | Rule |
| --- | --- | --- | --- |
| Registration epoch: the pair (`registration_generation`, `attach_counter`) | `installation_generation` is installed with the protected entry; `attach_counter` is journaled by the dispatcher | The one dispatcher-Host pairing | The dispatcher increments `attach_counter` by exactly 1 and commits it to the journal before each attach attempt: at the first attach under a generation, after every dispatcher restart, and after every loss of the Host connection that ends an attach. A counter is never reused, including after a lost attach reply. The current epoch is the last pair the dispatcher journaled and had accepted by `attach`. Every operation other than `attach` must carry that exact pair; any other pair refuses `host_registration_stale`. |
| Operation epoch: `state_version` | The station state record. At the station/1 pin SR1-SR3 assign its commits to the station operation service and SR6 leaves the authoritative-record question open (OPEN OQ18); the operator's ruling assigns the standing-run record to the dispatcher. The join is OPEN S1. | One station and its logical session | Every effect request carries the state version of its operation (SR3; for a start also SR4, recorded in R03), and two operations from one version commit at most once (SR2). The dispatcher journals the highest committed `state_version` it has accepted per `logical_session_id`. A new operation must carry a higher value than that journaled high-water mark; an equal or lower value on a new operation refuses `station_state_stale`. An exact replay (same (`logical_session_id`, `op_id`), same canonical arguments) is not a new operation and returns the recorded result at its recorded version. A value far in the future is not a fence bypass either: accepting it journals it as the high-water mark, so every lower later version refuses stale; whether gaps are admissible at all belongs to the record owner's compare-and-set and is part of OPEN S1. This high-water check is fencing at the effect owner; it does not make the dispatcher the compare-and-set writer of the station record until OPEN S1 is settled in station/1. |

Other epochs already defined by the dispatcher contract are not on this
wire and are never accepted in place of the two above: the daemon-instance
epoch (§4.1, fences supervisor writes), the execution claim epoch and
`controller_epoch` (§12; `controller_epoch` is carried and checked against
the session record, but it fences delivery at the Host, not registration),
and the account generation (user manager). Dispatcher §12 keeps these
distinct, and so does this file.

## 6. Operations

Checks run in this order on every operation, and the first failure is the
result; no later check is asked. Every failure before an operation's intent
is journaled is a refusal of section 8 with effect: none.

1. Transport and pin checks of section 3 for the direction.
2. Closed-shape, canonical-byte and bound checks of section 5.
3. Registration epoch: `registration_generation` and `attach_counter` are
   the current pair (on `attach`, the pair being established). If no
   `--host-config` entry is attached to this daemon, Host-to-dispatcher
   operations refuse `host_not_registered` before this step.
4. Operation epoch: `state_version` against the journaled high-water mark
   of its logical session, with the replay exception of section 5.
5. The operation's own checks below, including all admission gates that
   dispatcher §§7.1, 8.3, 9 and 12 already impose. This file adds no
   admission path around them: forward admission stays closed during
   recovery, maintenance, restore and hold exactly as those sections say,
   and an accepted `attach` admits no work by itself.

### 6.1 `attach` (dispatcher to Host)

`attach` establishes the registration epoch for the pairing. It has no
physical effect. It admits no work.

Closed request members: `v`, `op` = `attach`, `request_id`,
`registration_generation`, `attach_counter`.

Closed `result` members: `registration_generation`, `attach_counter`. Both
must echo the request exactly; any other value is a failed attach, the
counter stays consumed, and the next attach attempt uses the next counter.
Any Host incarnation or Host epoch attestation in the reply is the Host
owner's to publish (OPEN H1); no such member exists in v1 and an extra
member refuses the reply.

Sequence:

1. The dispatcher completes its own startup or recovery prerequisites for
   serving this seam: protected configuration loaded, journal replayed,
   and every open operation classified under section 7. It increments
   `attach_counter`, journals the phase `attach_requested` with the new
   pair and commits it, all before any request byte is written. The
   counter is scoped to the generation and its first journaled value under
   a generation is 1. A counter that cannot increment inside its bound is
   never wrapped or reused; a new `installation_generation` is required.
2. It verifies the Host socket and peer uid (section 3) and sends `attach`.
   A failure before any byte is written is `host_socket_untrusted` or
   `host_unavailable` (section 8); the journaled counter stays consumed.
3. The Host accepts only a pair it has not superseded: a request whose
   `attach_counter` is lower than the highest counter it has accepted under
   that generation refuses `host_registration_stale`; the same counter
   under a different generation refuses `host_registration_conflict`. A
   replay of the identical request (same `request_id`, same canonical
   arguments) returns the recorded attach result and establishes nothing
   new. A second attach with the same counter but a new `request_id` is a
   duplicate and refuses `host_registration_stale`. Refusal is by the
   epoch values, never by timing, ordering of connections or elapsed time.
   How the Host stores the highest accepted counter durably across its own
   restart is OPEN H1; a Host that cannot hold it can only fence within one
   Host lifetime, and the dispatcher's own current-epoch check on every
   later operation remains the fence on its side.
4. On an accepted reply the dispatcher journals `host_attached`. From that
   commit the pair is current: the Host uses it on every later operation,
   and the dispatcher refuses every operation carrying another pair. A lost
   or ambiguous reply leaves the pairing unattached under that counter; the
   dispatcher does not resend the same counter and does not treat the old
   pair as current again.

Until the first successful attach under the current generation and
dispatcher lifetime, the Host has no current epoch to present. A
`host_start`, `host_stop` or `host_liveness` that arrives in that window
refuses `host_registration_stale`, effect: none. The Host waits for
`attach`; it does not invent an epoch and does not retry the refused
operation under a new operation id.

### 6.2 `host_start` (Host to dispatcher)

`host_start` asks the dispatcher to perform the physical start of one
execution of one logical session under the session-scoped host admission
permit. It is the gated row of dispatcher §8.1 and §12. Its members below
reuse that row's members unchanged and add the two epoch members of
section 5; the set is closed. Only the pinned Host principal may call it.
The dispatcher alone calls the privileged launcher; the Host has no
launcher authority, no caller chain and no spawn grant.

Closed request members: `v`, `op` = `host_start`, `request_id`,
`logical_session_id`, `execution_id`, `controller_epoch`, `op_id`,
`registration_generation`, `attach_counter`, `state_version`.

Closed `result` members: `logical_session_id`, `execution_id`, `op_id`,
`run_id` (1..64 bytes, same id grammar; the dispatcher execution handle of
§4.4), `record_state` (exactly one of the `run/1` state values `accepted`,
`queued`, `starting`, `running`, `stopping`, `cleaning`, `done`, `failed`,
`cancelled`, `expired`), `state_version` (the version as recorded).
A successful reply means the dispatcher has journaled the operation and
reports the record's current state; it is not proof that a process is
bound, that input may be delivered, or that the start has completed.
Registration and readiness before delivery remain the §12 gate.

The dispatcher is the effect owner of the start:

1. Under its state lock it fixes the canonical arguments and their digest,
   checks both epochs, the permit for `logical_session_id`, the owner's
   session lifecycle authorisation and every §12 successor row, and
   journals the operation intent with its in-flight marker and the
   dispatcher-derived effect identities of §4.5. Nothing downstream is
   called under the lock.
2. It journals the station/1 R03 phases in order: `lease_requested` before
   the first lease acquire of this start, and `start_requested` before the
   physical-start request is sent. Phase `start_requested` exists so that
   recovery can tell whether a request may have been sent. Which writer
   records `lease_requested` once the dispatcher holds the run record is
   part of OPEN S1; the `start_requested` commit before the start request
   leaves the dispatcher is a rule of this file whatever that join decides.
3. It performs the start through the existing launcher effect path with a
   per-physical-start claim and root-receipt interlock (§§4.5, 5.3, 12),
   then revalidates epochs and fences before recording the result.

Idempotence and unknown outcomes:

- A replay with the same (`logical_session_id`, `op_id`) and identical
  canonical arguments, including after a crash of either side, returns the
  recorded result and the record's current state. It creates no second
  claim, effect or process (station/1 §2 replay rule; dispatcher §12).
- The same (`logical_session_id`, `op_id`) with different arguments refuses
  `request_id_conflict`, effect: none. station/1 names the station-side
  equivalent `station_operation_conflict` with the station operation
  service as evaluator; the dispatcher returns its own Appendix A code for
  its own record and does not rename the station code.
- A lost, ambiguous or timed-out reply after `start_requested` is an
  unknown outcome. The dispatcher record stays open and unreleased; in
  station/1 terms the station is in `held:start`. The dispatcher
  reconciles that one operation id: it asks the Host by `query_operation`
  (section 6.4) and reads the receiver evidence that §4.5 and §7.1 already
  define (effect observation, start receipt, process evidence). It never
  sends a second start for that operation id and never mints a replacement
  operation id for the same start. The Host side of a lost reply follows
  §8.3: repeat the same request with the same `request_id`, never a new
  `op_id`.
- A previous execution of the session whose claim is not confirmed absent
  refuses a new start `agent_busy` (dispatcher §12). Unknown is not
  absence; a liveness report, a Host reply, service health and a changed
  boot identifier do not confirm absence (station/1 U4, R09).

### 6.3 `host_stop` (Host to dispatcher)

`host_stop` asks the dispatcher to stop one execution under the same
permit. It is host-requested, durably distinct from owner `cancel`, and
grants no start authority. Only the pinned Host principal may call it, and
only for an execution under its own permit.

Closed request members: `v`, `op` = `host_stop`, `request_id`,
`logical_session_id`, `execution_id`, `controller_epoch`, `op_id`,
`registration_generation`, `attach_counter`, `state_version`. Here `op_id`
is the stop operation's own identity, distinct from the start's `op_id`;
`execution_id` names the target execution.

Closed `result` members: the same closed set as `host_start`. A successful
reply reports the recorded stop operation and the record's current state
(for example `stopping`); it does not assert that any process has ended.

The dispatcher journals the stop intent before its stop effect, then runs
the existing journaled stop path: the E-stop effect of §4.5 and the
account-wide `exec.stop` scope of §§5.3 and 6, continued under the same
effect identity and identical canonical arguments while processes remain.
A completed stop replays without signalling and never signals a later
execution. A replay of the stop (same (`logical_session_id`, `op_id`),
same arguments) returns the recorded result; the same `op_id` with
different arguments refuses `request_id_conflict`.

A stop ends only by an absence acceptance: the dispatcher records the
execution as ended and releases its claim only when the qualified absence
proof behind station/1 R09 is accepted and journaled, never by the Host's
reply, the stop request's acceptance, a process exit report, a missing
liveness report or a timeout. station/1 R09 is that acceptance at the
station record; who writes the station-side R09 once the dispatcher holds
the run record is part of OPEN S1, and the dispatcher-side rule here does
not wait for it: no R09-grade absence evidence, no ended record, no claim
release. An execution that is not under this Host's permit refuses
`run_not_owned`, effect: none.

### 6.4 `query_operation` (dispatcher to Host)

`query_operation` is the dispatcher's read-only reconciliation question to
the Host: what does the Host hold for this one operation id. The name is
defined here. It performs no mutation at either side, admits no work and
needs no prior journal intent; a read that only classifies is not an effect
(station/1 SR5). It is the counterpart, on this seam, of the receiver-owned
`effect.observe` reads of dispatcher §4.5; it is not a new dispatcher
operation and is never offered to any other caller.

Closed request members: `v`, `op` = `query_operation`, `request_id`,
`registration_generation`, `attach_counter`, `logical_session_id`,
`execution_id`, `op_id`, `state_version`. The `state_version` is the
dispatcher's journaled version for that session at the operation.

Closed `result` members: `logical_session_id`, `execution_id`, `op_id`
(echoed; a mismatch discards the reply), and `outcome`, exactly one of the
six receiver states of dispatcher §4.6: `recorded`, `pending`,
`rolled_back`, `not_recorded`, `unavailable`, `contradictory`. No other
result member exists in v1. What the Host can attest under each value on
this seam, what complete coverage means for its `not_recorded`, and how
long it retains operation records here are the Host owner's to publish
(OPEN H3). The Host's existing manager-seam status by operation id is a
Host-side mechanism on a different socket; this file does not assume the
same answers, retention or deadlines carry to this seam.

The dispatcher maps the reply through §4.6 without widening it:

- `recorded` says the Host holds a terminal recorded outcome for that
  operation id. This wire carries no outcome detail inside it, so the
  dispatcher classifies the operation only together with its own recorded
  result, the start receipt and process evidence. `recorded` never by
  itself proves a process was bound or has ended.
- `pending` and `unavailable`, including a timed-out or lost query reply,
  are unknown. The operation stays open and unreleased.
- `rolled_back`, and `not_recorded` with complete coverage, are known
  absent for that Host-side effect only. They authorize no new start, no
  ended record and no claim release. They are one input beside start
  receipts, effect observations and process-absence proof, never a
  substitute for any of them.
- `contradictory` stays contradictory and is surfaced, not resolved by
  retry.

A query repeats safely: it consumes no operation, advances no epoch and
creates no record. It still carries both epochs and refuses stale ones, so
a replaced Host incarnation with a stale registration view cannot answer
questions about current operations. `effect: none` holds for a refused
query exactly as for every refusal of section 8.

### 6.5 `host_liveness` (Host to dispatcher)

A liveness report is information from the Host. It never admits work, never
writes the dispatcher record, never opens delivery, never ends a stop and
never proves presence or absence. Service health alone admits nothing, and
a missing report is not an absence proof and changes nothing. station/1 K4
underlines the point for its own liveness notion: a self-report, heartbeat,
answered message or live helper process is not a liveness proof and is
never merged into a termination verdict.

Closed request members: `v`, `op` = `host_liveness`, `request_id`,
`registration_generation`, `attach_counter`, `sessions`. `sessions` is an
array of 0..64 entries; the bound 64 is defined here for v1 (one report
covers at most 64 logical sessions; a Host holding more sends more
reports). Each entry is a closed object with exactly
`logical_session_id`, `execution_id`, `state_version` and `alive`
(boolean, exact `true` or `false`). `state_version` is the operation epoch
the Host last saw for that session. What `alive` attests on the Host side
(a runner connection, a native process, readiness to take input, or
another internal fact) and how the Host produces it is the Host owner's to
publish (OPEN H4). The dispatcher needs only the claim and its limits, and
this file gives the claim no effect whatever its Host meaning turns out to
be.

Closed `result` members: `reported` (integer 0..64, entries received) and
`discarded_stale` (integer 0..64, entries discarded under the rules below).
The reply acknowledges receipt only.

Handling, per entry:

- an entry whose `state_version` differs from the dispatcher's journaled
  version for that session, or whose (`logical_session_id`, `execution_id`)
  names no record the dispatcher holds, is discarded and counted in
  `discarded_stale`. It creates no record. In particular a report claiming
  `alive` is `true` for a start the dispatcher never journaled starts
  nothing, admits nothing and records no execution;
- a current entry is retained only as operational information beside the
  record (for example for a status display that labels its source as a
  Host report). It does not change `record_state`, any claim, fence or
  quota, and it is not journaled as a state transition.

Report frequency is not set here. station/1 leaves `liveness_interval`
and `silence_deadline` values open (its OQ4), and this file invents no
interval (OPEN S3, OPEN H4). Consequences never attach to silence by
timeout: no stop, no release and no absence follows from a missing report.

## 7. Journal phases, loss and restart

Journal entries are opaque payloads of [journal.md](journal.md); the phase
names and the commit-before order below are this contract's. Phase names
`lease_requested` and `start_requested` are station/1 R03 names cited
exactly. The other names in this table are defined here for the dispatcher
journal. Every entry is committed (fsync, journal contract) before the
step it precedes; an entry that cannot be committed admits no following
step.

| Order | Dispatcher journal entry | Committed before | Source of the name |
| --- | --- | --- | --- |
| 1 | `attach_requested` with the new (`registration_generation`, `attach_counter`) | The `attach` request is written | Defined here |
| 2 | `host_attached` with the accepted pair | The pair is treated as current | Defined here |
| 3 | Start operation intent: canonical arguments, both epochs, effect identities, in-flight marker | Any downstream call for that start | Dispatcher §4.1, §4.5 |
| 4 | `lease_requested` | The first lease acquire of that start | station/1 R03 |
| 5 | `start_requested` | The physical-start request is sent | station/1 R03 |
| 6 | `start_unknown` for that (`logical_session_id`, `op_id`) | The operation is held for reconciliation and any later step reads its state | Defined here; the held classification is station/1 `held:start` and R17 |
| 7 | Stop operation intent (host-requested) with its effect identity | The stop effect is issued | station/1 R08; dispatcher §4.5 |
| 8 | `absence_accepted` citing the absence proof | The execution is recorded ended and its claim released | Defined here for the dispatcher journal; the acceptance is station/1 R09 |
| 9 | Recovery entry: each open operation found and its classification | Any resumed step after a restart | station/1 R10 |
| 10 | Journal fence entry and fence mark | Every later step after a lost, fenced or restored journal | station/1 R23 |

Replay retains every complete valid frame, including a phase whose reply
was never acknowledged ([journal.md](journal.md)): a replayed
`start_requested` with no recorded outcome is an unknown start, not a
start that may be sent again.

Loss and restart follow station/1's columns and the dispatcher's existing
recovery rules. Nothing below creates a second start, an ended record or
a released claim.

| Event | Rule |
| --- | --- |
| Dispatcher restart | This is station/1's "starter crash" column, never a capability report: the cells that carry it for the states this file touches include [`starting` x starter crash] = `starting; R10; no gate`, [`running` x starter crash] = `running; R10; no gate` and [`held:start` x starter crash] = `held:start; R10; no gate`. On restart the dispatcher takes exclusive ownership, replays the journal in two passes, writes the recovery entry (R10) classifying every open operation, and reconciles under §7.1 before forward admission. An operation found at `start_requested` with an unknown outcome stays unknown (`held:start`) and is reconciled by `query_operation` and receiver evidence. Only after classification does the dispatcher attach again, with the next `attach_counter`; the pre-restart registration epoch is dead and refuses stale. |
| Host loss (socket gone, connect refused, peer died) | No new start: no `host_start` can arrive and the dispatcher starts nothing on its own. Open operations stay unknown until reconciled; claims, quotas, fences and delivery closure are retained exactly as §12 states for host failure. Pre-send dispatcher-to-Host failures are `host_unavailable`; a failure after bytes were written is an unknown outcome of that operation. A missing liveness report proves nothing. |
| Host restart or reconnect | The old registration epoch is dead. The dispatcher attaches with the next counter (section 6.1); Host operations carrying the old pair refuse `host_registration_stale`, and the dispatcher's queries are re-issued only as reads. Whatever the Host still holds after its restart is learned again by observation and reports, not assumed: replayed ready Host metadata without live evidence settles nothing (the Host's own stated rule on its manager seam, applied here as a limit, not a borrowed guarantee). |
| Lost, fenced or restored dispatcher journal | This is station/1 R23 and dispatcher §11.1. The dispatcher writes its fence mark, records nothing as ended, and every host operation whose record cannot be cleared refuses `station_session_journal_fenced` (station/1 gate 9 of the start gates; evaluator `DI`). Forward admission stays node-wide fenced until the §11.1 external fence, reverse inventory and operator disposition complete; a restored snapshot certifies no completeness of its own. Registration counters journaled only in the lost journal cannot be proved monotonic, so attach does not resume under the fenced generation: after clearance the operator path installs a new `installation_generation`, and only then may attach proceed. How the clearance that cites R09 is recorded for a slot is part of the station join (OPEN S6). |
| Ambiguous reply, either direction | The same-effect rule of §4.5 and the reply rule of §8.3: repeat the same request with the same `request_id` and the same operation identity, or observe. Never a new `op_id`, never a second physical effect, never a fabricated receipt. |

## 8. Refusals

Every refusal in this contract has exactly one code and effect: none.
"Effect: none" means no record is created or changed, no epoch advances
(except the attach counter journaled before an attach attempt, which stays
consumed by design of section 6.1 and admits nothing), no downstream call
is made, no claim is taken or released and no reply on another connection
is affected. A refusal after an operation's intent is journaled is not in
this table: it is a recorded outcome of that operation under §§4.5 and 4.6.

Codes come from dispatcher Appendix A or station/1 §4.3 wherever one
exists, reused without renaming. Codes marked "defined here" are new
dispatcher codes of this contract, with the dispatcher (`DI`) as evaluator.
A refusal enforced by closing a connection with zero bytes (the transport
contract's disposition for an unpinned peer or an untrusted endpoint)
carries its code as the local recorded reason; no bytes are written to
present it.

| Code | Evaluator and source | When | Effect |
| --- | --- | --- | --- |
| `caller_not_permitted` | Dispatcher, Appendix A | Kernel peer uid is not the pinned Host service uid, or the Host principal pin fails at any recheck point. A peer outside the transport caller pins at all is closed with zero bytes before hello under the transport rules, with this code as the recorded reason. | none |
| `request_invalid` | Dispatcher, Appendix A; also the Host's refusal for the same cause on its socket | Non-canonical bytes; unknown, missing, duplicated or cross-operation member; value out of its bound; wrong `v` or `op`; an operation offered in the wrong direction; an extra member in a reply, including an unpublished Host member on `attach`. | none |
| `host_not_registered` | Dispatcher, defined here | A Host-to-dispatcher operation arrives while no `--host-config` entry is attached to the daemon. No host is registered by any request, so there is no other way out of this state. | none |
| `host_registration_stale` | Dispatcher, defined here (station/1 has no code for a registration epoch) | `registration_generation` does not equal the accepted `installation_generation`; `attach_counter` does not equal the current journaled counter (lower, higher, or the same counter presented again under a new `request_id` after `host_attached`); an operation presenting the epoch of a dead attach; a configured generation lower than the journaled one at load. | none |
| `host_registration_conflict` | Dispatcher, defined here | The same `installation_generation` loaded with different configuration bytes than the journaled digest; the same `attach_counter` presented under a different generation. | none |
| `host_socket_untrusted` | Dispatcher, defined here, local refusal with zero bytes written | On the dispatcher-to-Host direction, before connect or after it: `socket_path` is not a socket, its owner is not `socket_owner_uid`, a parent component is a symlink or has an untrusted owner or mode, the kernel peer uid after connect is not `host.uid`, or the path cannot be represented as a socket address. | none |
| `host_unavailable` | Dispatcher, defined here, local refusal | Connect to the verified Host socket fails, or the connection fails before any request byte of this operation is written. A failure after bytes were written is never this code; it is an unknown outcome. | none |
| `station_state_stale` | station/1 §4.3 (code verbatim; SR3 makes an effect owner's stale refusal surface under this code, OPEN S2) | A new operation carries a `state_version` equal to or lower than the highest committed version journaled for its `logical_session_id`, or an effect owner has already seen a higher epoch for the station. An exact replay is not refused under this code. | none |
| `request_id_conflict` | Dispatcher, Appendix A | Same (`logical_session_id`, `op_id`) as a recorded operation, or same caller-scoped `request_id` on `attach`, with different canonical arguments. | none |
| `run_not_owned` | Dispatcher, Appendix A | `host_stop` (or a result lookup behind it) names an execution that is not under this Host's session permit. | none |
| `agent_busy` | Dispatcher, Appendix A and §12 | `host_start` for a session whose previous execution claim is not confirmed absent. Unknown counts as not absent. | none |
| `capacity:hold` and the other `capacity:*` codes of Appendix A | Dispatcher, §§8.3 and 9 | `host_start` while forward admission is closed for recovery, hold, maintenance, restore or a temporary capacity gate. `host_stop` and reconciliation keep the recovery availability that §8.3 already gives them; this row creates no new closure for them. | none |
| `station_session_journal_fenced` | Dispatcher as `DI`, station/1 §4.3 (code verbatim) | The dispatcher's session journal is lost, fenced or restored and the affected record has no clearance (R23, section 7). | none |

No other refusal code exists in v1. In particular there is no code by which
a liveness report, a Host reply or the passage of time can produce an ended
record, a released claim or an admitted start; those outcomes need the
absence acceptance and admission gates their own sections name.

## 9. Negative cases

Each row is written to be implemented as one later test: the setup fixes a
value, the required result names the code and the checkable absence of
effect, and the valid neighbor differs in that one value and must pass.
These are contract requirements for a later isolated qualification, not
executed evidence. "Journal holds" claims are checked against the
dispatcher's journal and record state after the step; "downstream calls"
means every launcher and Host-socket attempt the harness can observe.

| Case | Setup and mutated input | Required result | Valid neighbor (must be accepted and proceed) |
| --- | --- | --- | --- |
| Configuration member unknown | Valid `--host-config` file with one extra root member `zz` | Startup refuses locally, zero protocol bytes, daemon does not serve | The same file without `zz` loads and serves |
| Configuration member missing | Valid file with `socket_owner_uid` removed | Startup refuses locally, zero protocol bytes | The same file with the member restored loads and serves |
| Configuration not canonical | Valid members but one space byte inserted between two tokens | Startup refuses locally, zero protocol bytes | The same members in exact canonical bytes load and serve |
| Two hosts in one file | `host` replaced by an array of two principal objects | Startup refuses locally for the closed-shape rule of section 4, zero protocol bytes | The same file with the first object alone under `host` loads and serves |
| Generation reused with changed bytes | Generation `G` journaled with its digest; file edited to a new `socket_path`, generation still `G` | Load refuses `host_registration_conflict`, effect: none; journaled generation and digest unchanged | The same edited file with generation `G+1` loads, and attach under `G+1` proceeds |
| Wrong caller uid | Current attach in force. A peer with a pinned transport caller uid other than `host.uid` sends an otherwise valid `host_start` | `caller_not_permitted`, effect: none. No intent journaled, no downstream call | The identical request from peer uid `host.uid` with a valid pin is journaled and proceeds |
| Unpinned caller | Current attach in force. A peer uid in no transport pin connects | Zero-byte close before hello, recorded reason `caller_not_permitted`, effect: none | The pinned Host peer completes hello and its operation proceeds |
| Host principal pin replaced | Between load and the operation, the pinned Host directory is replaced with a different inode | Operation refuses `caller_not_permitted` at the pre-result recheck, effect: none | The original pinned directory unchanged: the same operation proceeds |
| Wrong socket owner | `socket_path` names a real socket whose owner is a different uid than `socket_owner_uid` | Dispatcher-to-Host attempt refuses `host_socket_untrusted` locally; zero request bytes written; no `query_operation` answer is acted on | The socket at the same path owned by `socket_owner_uid` with peer uid `host.uid`: `attach` proceeds |
| Wrong socket peer uid | Socket owner correct, but the accepting process has a peer uid different from `host.uid` | `host_socket_untrusted`, zero request bytes written, effect: none | Accepting process with peer uid `host.uid`: `attach` proceeds |
| Host down before send | No listener at the verified `socket_path` | `host_unavailable` locally; attach counter stays consumed; no operation is admitted under that counter | Listener present and verified: the next attach with the next counter completes |
| Stale registration epoch (low) | Current epoch `(G, 7)`. The Host sends `host_start` with `attach_counter` 6, all else valid | `host_registration_stale`, effect: none. No intent journaled, no downstream call, high-water `state_version` unchanged | The identical request with `attach_counter` 7 is journaled and proceeds |
| Stale registration epoch (high) | Current epoch `(G, 7)`. The Host sends `host_start` with `attach_counter` 8 | `host_registration_stale`, effect: none. A future counter is not adopted and does not advance the current epoch | After a real dispatch attach commits counter 8, the identical request with `attach_counter` 8 proceeds |
| Operation before any attach | No successful attach yet in this dispatcher lifetime. The Host sends `host_start` carrying a pair it chose | `host_registration_stale`, effect: none. The presented pair is not adopted | After the dispatcher attaches, the same `host_start` carrying the attached pair proceeds |
| Duplicate attach | Counter 7 already `host_attached`. A second `attach` arrives with counter 7 and a new `request_id`, immediately or much later | `host_registration_stale`, effect: none at both times; the refusal does not depend on elapsed time or connection order | A replay of the byte-identical first attach request (same `request_id`) returns the recorded attach result; an attach with counter 8 establishes the new epoch |
| Attach reply lost | Counter 7 sent and accepted by the Host; the reply never reaches the dispatcher | Pairing is not current under 7; counter 7 is never resent; no operation is admitted under 7 by either side's later claim | The next attach with counter 8 completes and operations under `(G, 8)` proceed |
| Stale state version (equal) | Session high-water `state_version` is 41 after a committed start. A new `host_start` with a new `op_id` carries `state_version` 41 | `station_state_stale`, effect: none. No claim taken, no downstream call | The identical request with `state_version` 42 is journaled and proceeds |
| Stale state version (lower) | Same session, new `host_stop` carries `state_version` 40 | `station_state_stale`, effect: none. The running execution is not signalled | The identical `host_stop` with `state_version` 42 journals its intent and proceeds |
| Replayed `host_start`, same operation id | The start for (`logical_session_id`, `op_id` = `op-1`) is journaled. The identical canonical request is sent again, including after a restart of either side | The recorded result and current `record_state` are returned. Exactly one claim, one effect set and at most one process exist for `op-1`; journal shows no second `start_requested` for it | A second, genuinely new start uses a new `op_id`, a higher `state_version`, and only after confirmed absence, and proceeds as its own operation |
| Same operation id, different arguments | `op-1` replayed with a changed `execution_id` | `request_id_conflict`, effect: none. The recorded operation is unchanged | The byte-identical replay returns the recorded result (previous row) |
| Reply lost after `start_requested` | Journal holds `start_requested` for `op-1`; the physical start reply is dropped before the dispatcher can classify it | Operation goes to `start_unknown`, claim retained, classification `held:start`. Reconciliation runs `query_operation` for `op-1` plus receipt and process observation. No second start request for `op-1` under any id. A Host retry with the same `request_id` and `op_id` returns the open record | The same start with the reply delivered records its outcome, and `record_state` advances by that evidence alone |
| Dispatcher restart mid-start | Dispatcher stops after `start_requested` for `op-1` is committed and before its outcome is recorded, then restarts | Journal replays; recovery entry (R10) classifies `op-1` unknown; no new start for the session is admitted; the pre-restart epoch refuses `host_registration_stale`. After classification the dispatcher re-attaches with the next counter and reconciles `op-1`; no second `start_requested` is written for it | A restart in which `op-1` had a recorded terminal outcome before the stop replays to that outcome, and a new start with a new `op_id` proceeds if absence is confirmed |
| Host restart mid-start | The Host restarts while `op-1` is at `start_requested` and unknown | Open operation stays unknown and unreleased; old registration epoch refuses stale on both directions; the dispatcher re-attaches with the next counter and asks `query_operation` for `op-1`. No second start | The Host that did not restart answers the same query under the still-current epoch, and reconciliation proceeds on that evidence |
| Journal fenced | Dispatcher journal replaced by a restored copy, fence mark written (R23), clearance not complete. A valid `host_start` arrives | `station_session_journal_fenced`, effect: none. Nothing is recorded ended; claims of possibly-running executions are not released; attach does not resume under the fenced generation | After §11.1 clearance and a newly installed generation, attach completes and the same `host_start` shape is admitted under the new epoch |
| Stop ended by Host reply alone | `host_stop` accepted and the Host reports its side done; account-wide absence proof not accepted | Record stays `stopping`; no `absence_accepted` entry; claim not released; a new start refuses `agent_busy` | The same stop with the qualified absence proof accepted journals `absence_accepted`, records the end and releases the claim; a successor start may then proceed |
| Stop of a foreign execution | `host_stop` names an `execution_id` belonging to a different session's permit | `run_not_owned`, effect: none. That execution is not signalled | `host_stop` naming an execution under this Host's permit journals its intent and proceeds |
| Liveness report claiming a start | Report entry with `alive` `true` for an (`logical_session_id`, `execution_id`) the dispatcher never journaled | Entry discarded (`discarded_stale` counts it). No record created, no claim taken, no input admitted, `record_state` of every real record unchanged | The same report about a journaled running execution at the current `state_version` is counted as reported information, with still no record change at all |
| Liveness silence after a start | Running execution, then no `host_liveness` report arrives for an arbitrary long interval | Nothing changes: no stop, no release, no absence, no successor admitted on the silence | The same execution later reported `alive` `true` at the current version is retained as information only |
| Liveness with stale version | Report entry for the running execution with `state_version` one below the journaled version | Entry discarded as stale; the record is unchanged | The same entry at the journaled version is retained as information |
| Query answered `not_recorded` | `op-1` unknown after `start_requested`; Host answers `query_operation` with `outcome` `not_recorded` and complete coverage | Known absent for the Host-side effect only. The start is not retried, the record is not closed as never-run without the remaining receiver and receipt evidence, and no claim is released on this answer alone | With the start receipt and process evidence also proving the recorded outcome, that outcome is journaled for `op-1` under §4.6 mapping |
| Query answered `unavailable` | Host answers `outcome` `unavailable` (or the query reply is lost) | Unknown. `op-1` stays open and unreleased; repeating the query sends no effect | A later `recorded` answer for `op-1` classifies the Host-side effect under §4.6 |

## 10. Out of v1

The following are later versions. They are stated here so that v1 defines no
part of them, silently or otherwise:

- dynamic registration: any registry in which a Host appears, is discovered
  or registers itself at runtime, and any request-supplied registration
  identity;
- deregistration and maintenance windows: removing the entry at runtime,
  planned drain or suspension of the pairing, and host replacement that
  does not replace the protected entry under a new
  `installation_generation`;
- more than one Session Host per dispatcher, in either direction;
- cross-host operation: any transport between machines, signed envelopes,
  and leases or epochs enforced across machines;
- Host selection by capability, health or load, and failover from one Host
  to another;
- standing-runtime policy itself: session lifecycle authorisation and
  restart budgets, permit issuance, and delivery gates remain the
  dispatcher §12 and §16 question 3 publication work this file references.

Until a later version is published and qualified, each of these is closed:
it refuses by not existing, under the closest refusal of section 8 when a
request attempts to express it, with effect: none.

## 11. OPEN items

Nothing in this section changes sections 1 to 10. An objection to a decided
point, where one is recorded at all, is recorded here and nowhere else.

### 11.1 For the Host owner (swarma-session-host at `b132e552`)

| Id | Question | Needed from the Host owner before this seam can be qualified |
| --- | --- | --- |
| OPEN H1 | Durable attach state on the Host side | How the Host durably stores the highest accepted `attach_counter` per `registration_generation` across its own restart, before it replies to `attach`; and whether the attach reply needs a Host incarnation or Host epoch attestation member beyond the echo of section 6.1. The Host's manager-seam documents retain a "Host epoch" in their own admission object; its name, meaning and availability on this socket are unpublished, so v1 carries none. |
| OPEN H2 | The Host listener | The socket's deployment contract: path ownership and parent-directory protection, socket mode and group so that exactly the dispatcher service uid may connect, listener lifecycle across Host restart, stale-socket handling, and what the Host checks about the dispatcher's peer uid. Peer-credential availability and strength on the target platform must be the Host's published statement; prototype fixtures that settle for a peer pid and uid observation do not settle it. |
| OPEN H3 | `query_operation` answers | For each `outcome` value of section 6.4: what Host-side fact it attests, what complete coverage means for `not_recorded`, how the answer is serialized with the Host's own operation writes, and the retention floor for operation records on this seam. The retention and capacity figures published for the Host's private manager ledger belong to that seam and are not assumed here. |
| OPEN H4 | Liveness content | What `alive` attests (runner connection, native process, input readiness or another internal fact), how it is produced, and at what interval reports are sent. This file gives the value no effect, so the answer can be narrow; it is still the Host's to publish, because a consumer must know what it is displaying. Whether 64 entries per report is the right bound is part of the same answer. |
| OPEN H5 | Controller epoch and the delivery gate | Confirmation that the Host's controller epoch is a monotonic integer inside the section 5 bound, how it advances on Host replacement, and the registration-and-readiness-before-delivery join of dispatcher §12 (runner channel authentication, native-session lock) that an accepted `host_start` result feeds into. That join is outside this wire and remains an activation gate. |
| OPEN H6 | Host client behavior on loss | The Host's own durable intent before it asks for `host_start` (the book's "persists intent with delivery closed"), and its implementation of the same-request repeat after a lost reply, including that it never mints a new `op_id` for the same start and never starts anything itself while the dispatcher answer is unknown. |

### 11.2 For station/1 and joint publication (station/1 at `3463191e`)

station/1 is an unreleased draft at the cited pin. The rows and codes this
file cites are cited at that pin; a later accepted station/1 head requires
a re-pin of section 2 and a re-check of every cited row before this seam is
qualified.

| Id | Question | State at the pin, and what must settle it |
| --- | --- | --- |
| OPEN S1 | The OQ18 join (SR6) | At the pin, SR1-SR3 make the station operation service the single writer of the station state record and of the state version, and SR6 leaves open whether that record or the dispatcher's session record is authoritative. The operator's ruling of 2026-11-16 assigns the standing-run record to the dispatcher and keeps the station description in the workbench registry, read-only to the dispatcher. station/1 must incorporate the ruling in its own revision cycle: which record the dispatcher-held run record is a projection of, or replaces; who performs the compare-and-set that mints the `state_version` a `host_start` carries; through which read the dispatcher learns it (the registry read format is also undefined here); who writes the R03 phase `lease_requested` and the R09 absence acceptance at the station record once the dispatcher holds the run record; and whether `attach` needs a station scope. Until then section 5's high-water rule is fencing at the effect owner only. |
| OPEN S2 | Who evaluates `station_state_stale` on this seam | station/1 §4.3 names the station operation service as evaluator of `station_state_stale`, while SR3 says an effect owner that has seen a higher epoch refuses the request, reports the refusal, and the requester receives `station_state_stale`. This file follows SR3 with the dispatcher as effect owner. station/1 must confirm that the code surfaces to the Host exactly so, including what stale-request detail may be carried and by whom. |
| OPEN S3 | Clock and deadline values | station/1 §4.5 leaves values open: `start_detect` runs on the dispatcher policy `detect` bound, `end_to_absence` is its OQ21, and `liveness_interval` with `silence_deadline` is its OQ4. The dispatcher owner and the station owners must publish the values, together with the finite per-call deadlines of section 3. This file sets none of them; no timeout in v1 may end a record, release a claim or prove absence whatever values are chosen (station/1 U4). |
| OPEN S4 | The validated binding and start gates 8-18 | The form of the validated binding is station/1's OPEN OQ19, and the start gates below the R03 record line (including `station_binding_stale`, `station_lifecycle_not_permitted`, `station_plan_changed`, capacity and launcher gates) are owned there and in dispatcher §§3.4 and 12. This file consumes no binding and relaxes no start gate; joint publication must state how those verdicts surface on the section 6.2 result without renaming station codes. |
| OPEN S5 | Successor authority | The session lifecycle authorisation member, the ordinary restart budget (station/1's OPEN OQ3), permit issuance and withdrawal encodings, and who requests a start after a reboot are dispatcher §12 with §16 question 3. Section 6.2 assumes the dispatcher can evaluate them; their publication is a precondition of enabling `host_start`, and none of it is filled in here. |
| OPEN S6 | Clearance after a journal fence | R23 opens an absence debt and station/1 §11 makes a slot unusable after journal loss until a clearance record citing R09 exists. The encoding of that clearance, its writer, and the confirmation that registration resumes only under a new `installation_generation` (the rule of section 7) belong to the station and dispatcher owners jointly, beside dispatcher §11.1's restore formats. |
| OPEN S7 | Signatures beside peer pins | The decided v1 form authenticates host operations by kernel peer uid and pins alone and signs nothing (section 3). The objection worth recording is here: a same-uid process is indistinguishable from the Host service, and dispatcher §3.3 signs every other mutating request. Whether joint publication requires a request signature by the Host principal in addition to the pins, or accepts the pin-only form for a statically installed same-machine service pair, is for the station, dispatcher and Host owners to answer at acceptance. v1 is written without signatures either way. |
