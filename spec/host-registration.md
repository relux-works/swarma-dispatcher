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

Book chapter 7 says (in "Служба, раннер и единственный писатель",
"Service, runner and the single writer"): "Форматы разрешений и
аутентификацию `host_start` и `host_stop` задаёт спецификация хоста сессий"
("The permit formats and the authentication of `host_start` and `host_stop`
are set by the session-host specification"). This file does not take over
that assignment. It is the DISPATCHER SIDE of the joint publication that
[dispatcher §16 question 3](dispatcher.md#16-open-questions) names: the
pin-only authentication and the dispatcher-side member sets below are
written for that joint publication, and the Host side of it stays with the
Host owner (the OPEN H items of section 11.1). Every sentence below that
prescribes Host-side behaviour (Host acceptance rules, Host-evaluated
refusals, Host client repeats, the notice of section 6.6) is proposed to
the Host owner and covered by an H item; rules for the dispatcher's own
behaviour are this contract's own.

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
| Swarma book, chapter 7, `book/ru/07-zapusk-i-host-sessiy.md` | swarma-docs `006f653dbe914e23a40d37cd2790528ae795151e` | "Верстак" (registry record holds the launch description); "Пробуждение и перенос контекста" (standing sessions raised through the dispatcher; an operator-stopped session stays stopped); "Диспетчер и транзакция запуска" (one identifier per external effect; a lost start reply reconciles the same effect; unknown outcome is not absence); "Служба, раннер и единственный писатель" ("Service, runner and the single writer": the Host persists intent with delivery closed, then asks the dispatcher for `host_start`; only the configured host service account may call that path; the permit formats and the authentication of `host_start`/`host_stop` are set by the session-host specification, which is why this file is only the dispatcher side of the joint publication, see the status note above); "Разрешение на преемника" ("Permission for a successor": `host_stop` goes through a journaled stop with confirmed absence and grants no next start); "Забор, парковка и перезапуск" ("Fence, parking and restart": the Host itself sends no process signals) | Book text. Section names are cited in Russian with an English rendering where this file first uses them. |
| The operator's ruling of 2026-11-16 | Private input to this specification | The ownership table of section 1 and acceptance of this minimal form (static pin in protected configuration plus epoch fencing; dynamic registration later) | Decided. No identifier from it is reproduced here. |
| station/1, `protocol/station.md` | curator-spec `3463191e69fbc8a3c3c992547fec0fa8a68b4b05` | Actor `DI` (dispatcher: holds the session record, owns physical start and stop authority); `SR1`-`SR6`; records `R03` (phase `start_requested`; phase `lease_requested` is cited in sections 6.2 and 7 only to state that it is not a dispatcher entry in v1), `R08`, `R09`, `R10`, `R23`, in each case as the station record that a dispatcher journal entry corresponds to, not as the dispatcher entry itself (section 7); the `held:start` row and U5 ("never sends a second start request"); codes `station_state_stale` and `station_session_journal_fenced`; the "starter crash" event column; clocks `start_detect` and `end_to_absence` | Unreleased draft. Where it is not settled this file says so and opens an item in section 11 instead of deciding for it. `SR6` is still OPEN OQ18 at this pin; the ruling above is the input that answers it for this contract. |
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

Connection model, stated here: one operation per connection in both
directions, as the transport subset does. A connection carries exactly one
request of section 6 and its reply and is then closed; there is no pooling,
multiplexing or second operation on a connection, and no persistent
Host connection whose presence or loss could carry attach state. In the
dispatcher-to-Host direction this rule binds the Host listener and is
proposed to the Host owner (OPEN H2); whether that direction has any hello
or version exchange before the operation at all is likewise OPEN H2, and
no such exchange is defined here. A normal
close after a reply, and a lost or timed-out reply on an operation other
than `attach` itself, do not end the registration epoch; the events that
end it are listed in section 5. This seam is Linux only in v1: the
kernel peer credential it uses is Linux `SO_PEERCRED`, as in
[transport.md](transport.md), and no peer-credential or native
qualification claim is made for any other platform.

| Direction | Connector and listener | Authentication of the caller | Authentication of the receiver before any request byte is written | Operations |
| --- | --- | --- | --- | --- |
| Host to dispatcher | The Host connects to the dispatcher's socket (`dispatch/1`) | Kernel peer uid from `SO_PEERCRED` must be the pinned Host service uid, and the pinned principal directory of section 4 must pass the transport pin checks before hello and again before the result. The Host is a pinned principal of the dispatcher's transport and reaches the dispatcher in no other way. | The Host pins the dispatcher endpoint exactly as a transport client does: directory and socket identity, service owner, group, mode and socket type before connect, re-stated after connect, then the service uid from `SO_PEERCRED`. | `host_start`, `host_stop`, `host_liveness`, `attach_wanted` (sections 6.2, 6.3, 6.5, 6.6) |
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
  delivery or resurrect a cancelled start. The wait graph of dispatcher
  §4.1 therefore gains one edge and stays acyclic: the dispatcher's
  unlocked call worker calls the Host (`attach`, `query_operation`), and
  the Host's unlocked call worker calls the dispatcher, but (proposed to
  the Host owner, OPEN H2) the Host does not issue a synchronous
  Host-to-dispatcher call from inside its `attach` or `query_operation`
  handler; `attach_wanted` in particular is sent on its own connection
  outside any such handler invocation. The symmetric rule binds the
  dispatcher as this contract's own: the dispatcher acknowledges
  `attach_wanted` first and runs the `attach` it triggers afterwards on
  its unlocked call worker, never inside the notice handler (section 6.6).
- Every call has a finite monotonic deadline. On the Host-to-dispatcher
  direction, which runs on `dispatch/1`, the transport deadlines already
  apply unchanged: every stage deadline is `min(stage_start + 2 s,
  session_start + 5 s)` ([transport.md](transport.md), "Resource and
  deadline limits"). On that direction hello and the operation of one
  connection share one `request_id`, and a wrong-ID operation at READ-OP
  closes with no result, exactly as the transport states; a Host repeat
  that keeps the original `request_id` (proposed to the Host owner,
  OPEN H6) therefore carries that id in the hello of the new connection
  as well as in the operation. That per-connection matching and the
  reply echo are the only uses of `request_id` on this seam (section 5). The numeric deadline
  values for the dispatcher-to-Host direction are not defined here
  (OPEN S3). A timeout after request
  bytes were written is an unknown outcome (section 7), never a refusal
  and never a reason to send the effect again under a new identity.

Host peer admission on `dispatch/1` (Linux only), mirroring the "Managed
M0 callers" text of [transport.md](transport.md#managed-m0-callers): when
`--host-config` is attached, the transport snapshots the one Host
principal of section 4 before listening and asserts no overlap with the
server `callers` list or the M0 caller pins (the same uid, or the same
pinned directory identity, in more than one of those sets refuses startup
or attach locally with zero protocol bytes; the Host principal is
therefore neither a server caller nor an M0 caller). The Host pin is one
further pin beyond the transport bound of at most 32 unmanaged plus four
managed pins, defined here as the extension for this gated seam: with
both extensions attached the configuration holds at most those 36 pins
plus this one, and with no `--host-config` the transport bound is
unchanged. For the Host kernel
peer the same accept path applies as for every peer, with the global slot
taken before peer lookup, the transport per-UID limit of eight connections
and global limit of 64, the same pin checks before hello and before the
result, and exactly one operation on the connection; after hello, frames
are routed by a closed body discriminator as M0 frames are routed by their
`kind`: every request body of section 6 carries a `kind` member (section
5, defined here) with exactly the value
`swarma-dispatcher host-request/1`, only frames with that `kind` from the
pinned Host peer go to section 6, a frame with that `kind` from any other
peer admitted for hello refuses `caller_not_permitted`, effect: none
(section 8), and any other frame from
the Host peer is not a section 6 operation and closes without one; with
no `--host-config` attached there is no Host pin, so every section 6
frame from a non-Host peer admitted for hello refuses
`caller_not_permitted`, effect: none, and a peer in no pin
closes with zero bytes before hello exactly as an unlisted managed peer
does. There is no separate unregistered-Host refusal.

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

The member names `host`, `socket_path`, `socket_owner_uid` and
`installation_generation` of this file form are defined here; the
principal object shape reuses the transport members and bounds.

| Member | Type and bound | Meaning |
| --- | --- | --- |
| `v` | integer, exactly `1` | Version of this configuration form. |
| `kind` | string, exactly `swarma-dispatcher host-registration/1` | Discriminator of this file. The string is defined here, in the repository-qualified spelling of dispatcher §3.3 (D-WIRE-NAMES), parallel to `swarma-dispatcher m0-config/2`. Any other value refuses; it never acquires a new meaning by reuse. |
| `host` (defined here) | one principal object with exactly `dev`, `dir`, `ino`, `uid` | The pinned Host principal, in the exact shape and bounds of [transport.md](transport.md): `uid` is the Host service uid, and `dir` with its `dev` and `ino` is that principal's pinned directory; uid and directory identity are one pin. There is one such object, never an array: at most one Session Host per dispatcher in v1. The Host uid is nonroot, differs from the dispatcher service uid and is outside the managed agent range `30000..59999`, under the same deployment rule as every transport caller pin. |
| `socket_path` (defined here) | absolute ASCII path, 1..107 bytes, no empty, `.` or `..` component, no repeated separator, no trailing slash | The Host socket the dispatcher connects to for section 6.1 and 6.4. The path syntax is reused from the transport principal `dir` rule; the length bound is defined here to fit the Linux `sun_path` field (108 bytes including the terminating NUL, so at most 107 path bytes). A longer `socket_path` refuses at load with the local startup failure above and zero protocol bytes; it never loads and then fails every connect, and it is never truncated into a different address. |
| `socket_owner_uid` (defined here) | integer `1..4294967294`, equal to `host.uid` in v1 | The expected owner of the socket file at `socket_path`, checked before connect. Equality with the service uid is a v1 rule of this file; a distinct socket owner is not designed here. |
| `installation_generation` (defined here) | integer `1..9007199254740991` | The generation of this installed entry. It is the first half of the registration epoch (section 5). |

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
separate daemon configuration unit. The digest member set contains one
member per attached protected file and no others, with the member names
`server`, `m0` and `host_registration`. When both `--host-config` and
`--m0-config` are attached, this file defines the extended form for that
unit:
`SHA-256(CCJ-1({"host_registration":<sha256 hex of the host-registration bytes>,"m0":<sha256 hex of the M0 config bytes>,"server":<sha256 hex of server config bytes>}))`.
When `--host-config` is attached without `--m0-config`, the form defined
here has exactly the two members that are attached:
`SHA-256(CCJ-1({"host_registration":<sha256 hex of the host-registration bytes>,"server":<sha256 hex of server config bytes>}))`;
no `m0` member with an empty, zero or default value is invented for a file
that is not attached. Without `--host-config` the existing definitions
are unchanged. The transport-only reference subset, which has no host
operation, is unaffected either way.

The dispatcher validates the Host principal pin with the same checks as a
transport caller pin (`principalPin` in transport terms): at configuration
load, before hello and again before every host operation result on the
Host-to-dispatcher direction, and before every dispatcher-to-Host result is
acted on. The directory is opened through nonsymlink ancestors owned by
root or the Host uid and not group/other-writable; `fstat` must show the
configured device and inode, the Host uid as owner and mode exactly `0700`.
A missing, unreadable or replaced pin refuses; no new or empty directory is
accepted as enrollment. Where that failure is found decides its effect: at
load, before hello or before an operation's intent is journaled it is the
refusal `caller_not_permitted` of section 8, effect: none; at the
pre-result recheck after the intent has been journaled (and a downstream
call may already have been made) it cannot have effect: none, so it is not
a section 8 refusal of the operation — the result is withheld on that
connection, the operation stays recorded exactly as journaled, and a
later conforming replay under a pin that passes returns that record
(section 6.2); no second intent, claim or effect is created by the
withholding. Root-controlled enrollment must not recycle the
configured Host uid while its old pin survives; that non-reuse rule is the
deployment invariant of the transport contract and applies unchanged.

## 5. Common wire members and the two epochs

Every request body in section 6 is one closed CCJ-1 object. Every member
listed for an operation is required; unknown, missing, cross-operation or
out-of-bound members refuse with `request_invalid`, effect: none. No member
is optional in v1 unless its row says so. Container depth is at most 3 (the
outer object, one array or object inside it, one object inside that), which
every shape below fits. That depth is this contract's own: the bodies of
section 6 are a separate body grammar (`host-registration/1`) behind the
same byte framing, descriptor and deadline gates as the transport subset,
exactly as M0 validates its own bodies behind the same framing; they do
not widen the transport subset's own closed wire grammar, whose container
depth stays 2.

Common members and shared bounds:

| Member | Bound | Meaning |
| --- | --- | --- |
| `v` | integer, exactly `1` | Wire version of this contract. |
| `kind` | string, exactly `swarma-dispatcher host-request/1` | Closed routing discriminator of every request body of section 6, defined here (section 3). The string is in the repository-qualified spelling of dispatcher §3.3 (D-WIRE-NAMES), parallel to `swarma-dispatcher m0-request/1`. Any other value is not a section 6 body. Replies carry no `kind`; they use the result envelope below. |
| `op` | one of `attach`, `host_start`, `host_stop`, `query_operation`, `host_liveness`, `attach_wanted` | Operation discriminator. `host_start` and `host_stop` are the existing gated rows of dispatcher §8.1; `attach`, `query_operation`, `host_liveness` and `attach_wanted` are operation names defined here (`attach_wanted` is also proposed to the Host owner, section 6.6). An `op` value sent in the wrong direction refuses `request_invalid`, effect: none. |
| `request_id` | string of 1..64 bytes matching `[A-Za-z0-9._:-]`; generated ids are 32 lowercase hex characters | Correlation only on this seam, in the grammar of [transport.md](transport.md), which states that grammar's id is never a replay identity. It is scoped to the calling principal, echoed in the reply, and matched between hello and operation on one Host-to-dispatcher connection (section 3). It is never looked up across connections, never compared as a canonical argument and never a replay identity; the replay key below is the only replay identity here. A reused `request_id` under another replay key is simply a new correlation for that other operation and maps to no stored result. |
| `registration_generation` (defined here) | integer `1..9007199254740991` | Must equal `installation_generation` of the accepted entry. On `attach` it is the value being established; on every other operation except `attach_wanted`, which carries no registration member at all, it must equal the current journaled generation. |
| `attach_counter` (defined here) | integer `1..9007199254740991` | The dispatcher's journaled attach counter (below). On `attach` it is the new value; on every other operation except `attach_wanted` it must equal the current journaled counter exactly. |
| `state_version` | integer `1..9007199254740991` | The station state version carried as the operation epoch (station/1 SR3, R03). Required on every operation that names a logical session: `host_start`, `host_stop`, `query_operation` and each entry of `host_liveness`. `attach` names no session and carries none; whether station/1 wants a session scope on attach is OPEN S1. |
| `logical_session_id` | string of 1..64 bytes matching `[A-Za-z0-9._:-]` | The logical session, as named in dispatcher §12. The dispatcher finds the session-scoped host admission permit by this value; the permit reference encoding stays unpublished (§16 question 3) and no permit member is added here. |
| `execution_id` | string of 1..64 bytes matching `[A-Za-z0-9._:-]` | The execution claim and launcher/stop receipt identity (dispatcher §4.5). This file bounds its spelling on this wire only; its construction stays as §4.5 leaves it. |
| `controller_epoch` | integer `1..9007199254740991` | The Host's per-session controller fence epoch (dispatcher §12). The integer spelling is defined here for this minimal form; the Host owner must confirm its epoch is a monotonic integer of this range (OPEN H5). |
| `op_id` | string of 1..64 bytes matching `[A-Za-z0-9._:-]` | The idempotent operation identity of one `host_start` or `host_stop`, the `op_id` of dispatcher §12. The triple (`logical_session_id`, `op`, `op_id`), defined here as a refinement of dispatcher §12's (`logical_session_id`, `op_id`) replay relation for this seam, is the durable replay key of that operation. |

Replay comparison set, defined here for `host_start` and `host_stop`: two
requests are the same recorded operation exactly when their replay key
(`logical_session_id`, `op`, `op_id`) is equal and the compared canonical
arguments `execution_id`, `controller_epoch` and `state_version` are
equal. A request whose triple is not recorded but whose
(`logical_session_id`, `op_id`) is recorded under the other `op` (a
`host_stop` reusing a start's `op_id`, or the converse) is not a new
operation and not a replay of that record: it refuses
`request_id_conflict`, effect: none, and no stop is journaled for a start
record or the converse. `registration_generation` and `attach_counter` are not part of
that comparison: they are checked on every request for currency only
(they must be the pair that is current at the moment of the request,
section 6 check order), so a repeat after a re-attach carries the new
current pair and still matches the recorded operation, while bytes that
carry a dead pair refuse `host_registration_stale`, effect: none, before
any replay is considered. `request_id` is not a canonical argument
either and is never looked up or compared across connections (the row
above); a conforming Host repeat keeps the same `op_id` and the same
`request_id` as the original request and changes only the registration
pair to the current one, and a reused `request_id` under another replay
key is simply a new correlation that answers with that other operation's
own record or judgement, never with the first operation's record. Where sections 6 and 9 below say "same canonical
arguments", "identical arguments" or "exact replay" for a `host_start` or
`host_stop`, they mean this comparison set.

Responses reuse the result envelope of dispatcher §8.2 on both directions,
narrowed as stated here:
`{v, request_id, ok: true, result: {...}}` or
`{v, request_id, ok: false, error: {code, message}}`, with `request_id`
echoing the request. This seam narrows §8.2 by omitting its optional
`stage` member: no `stage` exists in v1 on this seam, and an envelope
carrying one is an extra member and refuses as section 8 states.
`message` is human text of 1..128 bytes in the same
ASCII subset and carries no semantics; that 1..128 bound is defined here
(dispatcher §8.2 names the member but sets no bound for it); a receiver that acts on message text
instead of `code` does not conform. The `result` members of each operation
are closed sets defined in section 6, each marked defined here where the
set or a member name is new. No other envelope member exists in v1.

The two epochs are distinct values and neither substitutes for the other:

| Value | Owner | Scope | Rule |
| --- | --- | --- | --- |
| Registration epoch: the pair (`registration_generation`, `attach_counter`) | `installation_generation` is installed with the protected entry; `attach_counter` is journaled by the dispatcher | The one dispatcher-Host pairing | The dispatcher increments `attach_counter` by exactly 1 and commits it to the journal before each attach attempt: at the first attach under a generation, at dispatcher start, and whenever it holds no current pair (section 6.1), with bounded retry whose values are OPEN S3. A counter is never reused, including after a lost attach reply, and a higher generation opens a fresh counter space scoped to that generation (section 6.1). A pair is current only from the `host_attached` commit for that counter until the first of: a dispatcher restart, a newly loaded higher generation, the journaling of a later `attach_requested`, a journal fence, an accepted `attach_wanted`, or a `host_registration_stale` reply from the Host for that pair. Journaling a new `attach_requested` therefore ends the previous pair's currency at once: until the new counter is `host_attached`, no pair is current, and the old pair does not become current again if the attempt fails or its reply is lost. Every operation other than `attach` and `attach_wanted` must carry the exact pair that is current at that moment; any other pair, and any pair presented while no pair is current, refuses `host_registration_stale`, effect: none. A Host-to-dispatcher operation so refused while the dispatcher holds no current pair restarts the bounded attach retry of section 6.1: the refused operation admits nothing and writes nothing beyond the attach's own journal entries that the restarted retry then makes. The registration members are checked for currency only and are never part of a replay comparison (the replay set above). |
| Operation epoch: `state_version` | The station state record. At the station/1 pin SR1-SR3 assign its commits to the station operation service and SR6 leaves the authoritative-record question open (OPEN OQ18); the operator's ruling assigns the standing-run record to the dispatcher. The join is OPEN S1. | One station and its logical session | Every effect request carries the state version of its operation (SR3; for a start also SR4, recorded in R03), and two operations from one version commit at most once (SR2). The dispatcher journals the highest committed `state_version` it has accepted per `logical_session_id`. A new operation must carry a higher value than that journaled high-water mark; an equal or lower value on a new operation refuses `station_state_stale`. A replay in the comparison set defined above (same (`logical_session_id`, `op`, `op_id`), same `execution_id`, `controller_epoch` and `state_version`, carrying whatever registration pair is current at the repeat) is not a new operation and returns the recorded result at its recorded version. After a journal fence, the journaled high-water mark alone does not prove a `host_start` new: section 7 requires proof against a floor kept outside the restored journal, failing which the request refuses (OPEN S6). A value far in the future is not a fence bypass either: accepting it journals it as the high-water mark, so every lower later version refuses stale; whether gaps are admissible at all belongs to the record owner's compare-and-set and is part of OPEN S1. This high-water check is fencing at the effect owner; it does not make the dispatcher the compare-and-set writer of the station record until OPEN S1 is settled in station/1. |

Other epochs already defined by the dispatcher contract are not on this
wire and are never accepted in place of the two above: the daemon-instance
epoch (§4.1, fences supervisor writes), the execution claim epoch and
`controller_epoch` (§12; `controller_epoch` is carried and compared
against the recorded operation values as the next paragraph states, but
it fences delivery at the Host, not registration),
and the account generation (user manager). Dispatcher §12 keeps these
distinct, and so does this file.

On `controller_epoch` in particular: the dispatcher does not evaluate it
itself. It carries the value, binds it to the operation record and its
effects, compares it as part of the replay comparison set and revalidates
that a late result belongs to the recorded operation values
(dispatcher §4.1, §12: the submitted four values are validated against
the record at each effect); it does not decide whether the value is the
Host's current controller epoch or a stale one, because that fence is the
Host's own (dispatcher §12, "Fences without cross-service locks" and
"Registration before delivery"). No refusal code or row in section 8
therefore exists for a stale or unknown `controller_epoch` at the
dispatcher in v1: the same replay key (`logical_session_id`, `op`,
`op_id`) presented with
a different `controller_epoch` is different canonical arguments and
refuses `request_id_conflict`, effect: none, and a new operation is not
refused by the dispatcher on this member alone. A Host that sends a start
under an epoch it has itself superseded breaks its own fence, which is
Host-side behaviour proposed to the Host owner (OPEN H5).

## 6. Operations

Checks run in this order on every operation, and the first failure is the
result; no later check is asked. Every failure before an operation's intent
is journaled is a refusal of section 8 with effect: none.

1. Transport and pin checks of section 3 for the direction.
2. Closed-shape, canonical-byte and bound checks of section 5.
3. Journal fence (Host-to-dispatcher operations only): if the
   dispatcher's session journal is lost or fenced and the slot has no
   clearance (station/1 §4.3 wording), the operation refuses
   `station_session_journal_fenced`, effect: none, before any epoch is
   asked. This step is what makes that code reachable at all in this
   direction, because a fenced journal also kills every registration
   pair. `attach_wanted` is the one exception: it writes nothing but a
   wake-up (section 6.6), so this step does not refuse it, and the fence
   still blocks the `attach` that follows it and every other operation.
   A Host-requested stop is unavailable on this seam for the whole
   fence, `host_stop` included: the record that would make its identity
   sound under dispatcher §8.3 is the lost one. Stops during the fence
   remain only off this seam: operator stop, R-ALLSTOP and the
   station-side stop that CRO (curator-run, station operation) requests
   at once for `held:stop` after R23; `host_stop` resumes only after
   clearance, a new generation and a successful attach (OPEN S4).
   Once a fence has been cleared, this step also runs the
   post-clearance floor check for `host_start` (sections 6.2 and 7),
   before steps 4 and 5: a `host_start` with no surviving record in
   the restored journal whose `op_id` or
   `state_version` cannot be proved new against the floor kept outside
   the restored journal refuses `station_session_journal_fenced`,
   effect: none, here, and is never first judged `station_state_stale`
   at step 5 or `agent_busy`/`capacity:*` at step 6. A key with a
   surviving record is not judged new here; steps 4 and 5 judge it as
   a replay or a conflict in the usual order. The same code
   therefore means both "no clearance yet" and, after clearance, a
   permanent pre-loss refusal; which it means is known only from
   clearance and operator disposition outside this wire, never from
   the code or its `message` (OPEN S6, OPEN H6).
4. Registration epoch: `registration_generation` and `attach_counter`
   are the current pair (on `attach`, the pair being established, judged
   at the Host under the proposed rules of section 6.1; `attach_wanted`
   carries no pair and skips this step). The registration members are
   checked for currency only and then take no part in any replay
   comparison (section 5). With no `--host-config` entry attached there
   is no Host pin, so no Host-to-dispatcher operation reaches this
   step: section 3 already refused it `caller_not_permitted`,
   effect: none, or closed it with zero bytes.
5. Replay lookup, then operation epoch, for `host_start` and
   `host_stop` only. "New" is defined here as: the replay key
   (`logical_session_id`, `op`, `op_id`) is not recorded, and
   (`logical_session_id`, `op_id`) is not recorded under the other
   `op` either. In order: a recorded key with the same compared
   arguments (`execution_id`, `controller_epoch`, `state_version`) is
   a replay and returns the recorded result; a recorded key with any
   different compared argument, `state_version` included, refuses
   `request_id_conflict`, effect: none, and is never
   `station_state_stale`; (`logical_session_id`, `op_id`) recorded
   under the other `op` likewise refuses `request_id_conflict`,
   effect: none; only a new operation in this sense is then judged by
   `state_version` against the journaled high-water mark of its
   logical session. `request_id` takes no part in this lookup. This
   step does not
   apply to `host_liveness` entries (stale entries are discarded one by
   one, never a whole-report refusal, section 6.5), nor to
   `query_operation`, whose `state_version` is the recorded version of
   the operation it asks about and is not judged as a new operation
   (section 6.4), nor to `attach` or `attach_wanted`, which name no
   session.
6. The operation's own checks below, including all admission gates that
   dispatcher §§7.1, 8.3, 9 and 12 already impose. This file adds no
   admission path around them: forward admission stays closed during
   recovery, maintenance, restore and hold exactly as those sections say,
   and an accepted `attach` admits no work by itself. The §12 successor
   rows of `host_start` (owner lifecycle consent absent, unknown permit,
   restart budget exhausted, plan divergence and the other rows of that
   table) refuse only under codes that are not published yet: this file
   maps none of them onto a section 8 code by invention, and `host_start`
   stays disabled until OPEN S4 and S5 publish those codes with their
   evaluators.

### 6.1 `attach` (dispatcher to Host)

`attach` establishes the registration epoch for the pairing. It has no
physical effect. It admits no work.

Closed request members: `v`, `kind`, `op` = `attach`, `request_id`,
`registration_generation`, `attach_counter`.

Closed `result` members, defined here: `registration_generation`,
`attach_counter`. Both
must echo the request exactly; any other value is a failed attach, the
counter stays consumed, and the next attach attempt uses the next counter.
Any Host incarnation or Host epoch attestation in the reply is the Host
owner's to publish (OPEN H1); no such member exists in v1 and an extra
member refuses the reply.

Sequence:

1. The dispatcher attaches at start and whenever it holds no current
   pair. Before an attempt it completes its own startup or recovery
   prerequisites for serving this seam: protected configuration loaded,
   journal replayed, and every open operation classified under section
   7. It increments `attach_counter`, journals the phase
   `attach_requested` with the new pair and commits it, all before any
   request byte is written; that commit ends any previous pair's
   currency at once (section 5). The counter is scoped to the generation
   and its first journaled value under a generation is 1; a higher
   generation opens a fresh counter space, so counter 1 under the new
   generation is its first value and not a reuse of counter 1 under the
   old. A counter that cannot increment inside its bound is never
   wrapped or reused; a new `installation_generation` is required. Each
   attach attempt is a new request with a fresh `request_id`; a failed,
   refused or unanswered attempt is retried only as a further attempt
   with the next counter, within a bounded retry whose attempt count,
   spacing and deadline values are not defined here (OPEN S3). The
   "whenever it holds no current pair" rule has no quiescent exception:
   when that bounded retry is spent the dispatcher still holds no
   pair, and each of these restarts the bounded retry afresh — its
   next start, an accepted `attach_wanted` wake-up, its next need to
   use this seam with no pair current, and, as a dispatcher rule of
   this contract, any Host-to-dispatcher operation refused
   `host_registration_stale` while it holds no current pair. That
   refused operation admits nothing and writes nothing beyond the
   attach's own entries the restarted retry then journals; the
   operation itself is not admitted by the restart and proceeds only
   if presented again under the pair a successful re-attach makes
   current.
2. It verifies the Host socket and peer uid (section 3) and sends `attach`.
   A failure before any byte is written is `host_socket_untrusted` or
   `host_unavailable` (section 8); the journaled counter stays consumed.
3. Host acceptance in this step is Host-side behaviour proposed to the
   Host owner (OPEN H1), with the Host as evaluator of its refusals. The
   Host accepts only a pair it has not superseded: a request whose
   `registration_generation` is lower than the highest generation it has
   accepted refuses `host_registration_stale`, effect: none, and within
   one generation a request whose `attach_counter` is at or below the
   highest counter it has accepted under that generation refuses the
   same code, effect: none, whatever its `request_id`. There is no
   replay exception for `attach` and no recorded attach result is ever
   returned to a replay: a byte-identical replay (same `request_id`),
   a duplicate with a new `request_id` and a replay of an already
   superseded counter all refuse alike under the at-or-below rule,
   because a conforming dispatcher never resends a counter. A higher
   generation than every accepted one opens a fresh counter space at
   the Host as it does at the dispatcher (step 1): counters accepted
   under a lower generation are no evidence against any counter under
   the higher one, and no `host_registration_conflict` arises anywhere
   on this wire (that code is only the dispatcher's load-time refusal
   of section 4). Refusal is by the epoch values, never by timing,
   ordering of connections or elapsed time. A durable
   highest-accepted (generation, counter) record on the Host side,
   stored before the Host replies to the `attach` that advances it,
   is a proposed precondition in OPEN H1: without it the at-or-below
   rule fences only within one Host lifetime and a replayed attach
   after a Host restart with no such record can be accepted at the
   Host, harmless only because the dispatcher's own current-epoch
   check on every later operation remains the fence on its side.
   Keeping the current pair itself durably across a restart stays
   optional and is the remaining durability choice of OPEN H1
   (section 6.6).
4. On an accepted reply the dispatcher journals `host_attached`. From that
   commit the pair is current: the Host uses it on every later operation,
   and the dispatcher refuses every operation carrying another pair. A lost
   or ambiguous reply leaves the pairing unattached under that counter; the
   dispatcher does not resend the same counter and does not treat the old
   pair as current again.

Until the first successful attach under the current generation and
dispatcher lifetime, and in every later window in which no pair is
current, the Host has no current epoch to present. A `host_start`,
`host_stop` or `host_liveness` that arrives in such a window refuses
`host_registration_stale`, effect: none; at the dispatcher that refusal
while it holds no current pair restarts the bounded attach retry
(step 1). As Host behaviour proposed to the Host owner (OPEN H1), a
Host that holds no pair, or whose held pair is refused
`host_registration_stale`, treats that pair as dead and asks with
`attach_wanted`, repeated with the bounded pacing of OPEN S3 until an
`attach` is accepted, and sends that notice only while it holds no
live pair (section 6.6); it does not invent an
epoch and does not retry the refused operation under a new operation id.
Once a pair is current again the same operation is presented under the
same `op_id` (proposed to the Host owner, OPEN H6) re-stamped with the
current pair: an operation that was refused before any intent was
journaled is then judged as the new operation it still is, and an
operation that was already recorded is met by the replay rule of section
5 and returns its record; neither course is a conflict, because the
registration members are no part of the comparison.

### 6.2 `host_start` (Host to dispatcher)

`host_start` asks the dispatcher to perform the physical start of one
execution of one logical session under the session-scoped host admission
permit. It is the gated row of dispatcher §8.1 and §12. Its members below
reuse that row's members unchanged and add `v`, the `kind` discriminator
of section 5 in whose `host-registration/1` body grammar the operation
travels on this socket routed by that `kind`, and the epoch members of
section 5 (`registration_generation`, `attach_counter` and
`state_version`); the set is closed. Only the pinned Host principal may call it.
The dispatcher alone calls the privileged launcher; the Host has no
launcher authority, no caller chain and no spawn grant.

Closed request members: `v`, `kind`, `op` = `host_start`, `request_id`,
`logical_session_id`, `execution_id`, `controller_epoch`, `op_id`,
`registration_generation`, `attach_counter`, `state_version`.

Closed `result` members, this closed set defined here:
`logical_session_id`, `execution_id`, `op_id`,
`run_id` (1..64 bytes, same id grammar; the dispatcher execution handle of
§4.4), `record_state` (exactly one of the `run/1` state values `accepted`,
`queued`, `starting`, `running`, `stopping`, `cleaning`, `done`, `failed`,
`cancelled`, `expired`), `state_version` (the version as recorded). The
member names reuse dispatcher §4.4 and §8.1; no other result member
exists in v1.
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
2. It journals its own `start_requested` phase entry before the
   physical-start request is sent. The name is the station/1 R03 phase
   name and the entry corresponds to that record (section 7); it is not
   the station R03 itself, which at the pin is committed by CRO. Phase
   `start_requested` exists so that recovery can tell whether a request
   may have been sent. The earlier R03 phase `lease_requested` is not a
   dispatcher journal entry in v1: at the pin it precedes the Curator
   home-lease acquire that CRO evaluates at start gate 8 on the lease
   result, and the dispatcher contract has no home-lease acquire; it
   enters this order only if the join of OPEN S1 assigns that acquire
   to the dispatcher, as a write-ahead published by that join. The
   `start_requested` commit before the start request leaves the
   dispatcher is a rule of this file whatever that join decides.
3. It performs the start through the existing launcher effect path with a
   per-physical-start claim and root-receipt interlock (§§4.5, 5.3, 12),
   then revalidates epochs and fences before recording the result.

Idempotence and unknown outcomes:

- A replay in the comparison set of section 5 (same replay key
  (`logical_session_id`, `op` = `host_start`, `op_id`), same
  `execution_id`,
  `controller_epoch` and `state_version`), including after a crash of
  either side and after any re-attach, returns the recorded result and
  the record's current state. The registration pair the replay carries
  must be the pair current at the repeat and is not compared with the
  pair the original request carried; a repeat that still carries a
  dead pair refuses `host_registration_stale`, effect: none, before
  the replay is looked up. It creates no second claim, effect or
  process (station/1 §2 replay rule; dispatcher §12).
- The same replay key with a different
  `execution_id`, `controller_epoch` or `state_version` refuses
  `request_id_conflict`, effect: none; so does the same
  (`logical_session_id`, `op_id`) presented as the other operation
  (section 5). station/1 names the station-side
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
  §8.3, as Host client behaviour proposed to the Host owner (OPEN H6):
  repeat the same operation with the same `op_id` and the same
  `request_id`, never a new `op_id`, carrying the pair that is current
  at the repeat so the replay reaches the record across a re-attach.
- A previous execution of the session whose claim is not confirmed absent
  refuses a new start `agent_busy` (dispatcher §12). Unknown is not
  absence; a liveness report, a Host reply, service health and a changed
  boot identifier do not confirm absence (station/1 U4, R09).
- After a journal fence and its clearance, non-resurrection is proved
  against a floor kept outside the restored journal (dispatcher §11.1),
  judged at section 6 step 3 before steps 4 and 5:
  a `host_start` is accepted as a new operation only where both its
  `op_id` is proved not to be a pre-loss operation and its
  `state_version` is proved higher than every pre-loss version for that
  session, against the station record under OPEN S1 or the operator's
  clearance inventory under OPEN S6. A `host_start` with no surviving
  record whose `op_id` or
  `state_version` cannot be proved new against that floor — including
  where no such floor can be produced at all, where the `op_id` is in
  the inventory but its dispatcher record is lost, and where its
  version is at or below the floor — refuses
  `station_session_journal_fenced`, effect: none, at step 3. It is never judged
  as a new operation against the restored high-water mark alone, so a
  Host repeat of a pre-loss `op_id` re-stamped with the current pair
  cannot start that operation a second time; the lost operation is
  reconciled through clearance and operator disposition, not by
  re-presenting it. Only after that clearance and disposition state
  the pre-loss start dead may the Host mint a new operation for the
  session, as proposed in OPEN H6, with a new `op_id` and a
  `state_version` proved new against the same floor; until then
  repeating the pre-loss `op_id` only repeats that refusal and mints
  nothing. How that disposition reaches the Host is outside this wire
  and part of OPEN S6 and H6; no member for it is defined here.

### 6.3 `host_stop` (Host to dispatcher)

`host_stop` asks the dispatcher to stop one execution under the same
permit. It is host-requested, durably distinct from owner `cancel`, and
grants no start authority. Only the pinned Host principal may call it, and
only for an execution under the permit of the named
`logical_session_id`. During a journal fence it is unavailable on this
seam at section 6 step 3, with stops remaining only off this seam as
that step states; that closure is recorded for the station owners in
OPEN S4.

Closed request members: `v`, `kind`, `op` = `host_stop`, `request_id`,
`logical_session_id`, `execution_id`, `controller_epoch`, `op_id`,
`registration_generation`, `attach_counter`, `state_version`. This member
set is defined here (dispatcher §8.1 states that the `host_stop`
request/reference/authentication member format is unpublished). Here `op_id`
is the stop operation's own identity, distinct from the start's `op_id`;
`execution_id` names the target execution.

Closed `result` members: the same closed set as `host_start`, defined
there. A successful
reply reports the recorded stop operation and the record's current state
(for example `stopping`); it does not assert that any process has ended.

The dispatcher journals the stop intent before its stop effect, then runs
the existing journaled stop path: the E-stop effect of §4.5 and the
account-wide `exec.stop` scope of §§5.3 and 6, continued under the same
effect identity and its journaled effect arguments while processes remain.
A completed stop replays without signalling and never signals a later
execution. A replay of the stop in the comparison set of section 5
(same replay key (`logical_session_id`, `op` = `host_stop`, `op_id`),
same `execution_id`,
`controller_epoch` and `state_version`, carrying the registration pair
current at the repeat) returns the recorded result; the same replay key
with a different compared argument, and a `host_stop` whose
(`logical_session_id`, `op_id`) is a recorded start, refuse
`request_id_conflict`,
effect: none, with no stop journaled, and a repeat carrying a dead pair refuses
`host_registration_stale`, effect: none.

A stop ends only by an absence acceptance: the dispatcher records the
execution as ended and releases its claim only when the qualified absence
proof behind station/1 R09 is accepted and journaled, never by the Host's
reply, the stop request's acceptance, a process exit report, a missing
liveness report or a timeout. station/1 R09 is that acceptance at the
station record; who writes the station-side R09 once the dispatcher holds
the run record is part of OPEN S1, and the dispatcher-side rule here does
not wait for it: no R09-grade absence evidence, no ended record, no claim
release. An execution that is not under the permit of the named
`logical_session_id` refuses `run_not_owned`, effect: none; in
particular an execution under another session's permit refuses even
where (as there is only one Host) that permit also belongs to this
Host.

### 6.4 `query_operation` (dispatcher to Host)

`query_operation` is the dispatcher's read-only reconciliation question to
the Host: what does the Host hold for this one operation id. The name is
defined here. It performs no mutation at either side, admits no work and
needs no prior journal intent; a read that only classifies is not an effect
(station/1 SR5). It is the counterpart, on this seam, of the receiver-owned
`effect.observe` reads of dispatcher §4.5; it is not a new dispatcher
operation and is never offered to any other caller.

Closed request members: `v`, `kind`, `op` = `query_operation`,
`request_id`, `registration_generation`, `attach_counter`,
`logical_session_id`, `execution_id`, `op_id`, `state_version`. Here
`op_id` names the target `host_start` or `host_stop` operation; because
section 5 refuses the same (`logical_session_id`, `op_id`) under the
other `op`, that pair names at most one recorded operation and no
target-operation member is defined here. The
`state_version` is defined here as the operation's recorded
`state_version`: the version journaled in the dispatcher record of the
named (`logical_session_id`, `op_id`) operation itself, not the
session's later high-water mark if further operations have since been
accepted. It binds the question and any reply to that one operation and
is proposed to the Host owner as an equality check against the Host's
record of that operation, or as whatever narrower use the Host owner
publishes; what a mismatch answers is part of OPEN H3, and no
high-water judgement is made on it, so a query for an older operation
is never refused merely because its recorded version is below the
current mark.

Closed `result` members, this closed set defined here:
`logical_session_id`, `execution_id`, `op_id`
(echoed; a mismatch discards the reply), and `outcome` (the member name
as used in this result set is defined here), exactly one of the
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
  operation id. Dispatcher §4.6 maps a receiver's `recorded` to known
  present or known absent only together with the recorded result and
  the current target state, and this wire carries neither inside the
  value; the Host is also not a receiver of the dispatcher's launch
  effect. A `recorded` answer here is therefore classified unknown
  until it is combined with the dispatcher's own recorded result, the
  start receipt and process evidence, and only that combination may
  classify the operation. `recorded` never by itself proves a process
  was bound or has ended, authorizes no classification change and
  releases nothing.
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
creates no record. It still carries the registration pair current at
the query and the operation's recorded `state_version`. That a Host
with no current pair, or presented with a pair that is not the highest
it has accepted, refuses the query `host_registration_stale`, effect:
none, is Host-side behaviour proposed to the Host owner (OPEN H1, H3),
with the Host as evaluator, so a replaced Host incarnation with a stale
registration view cannot answer questions about current operations; at
the dispatcher such a refusal for the pair it believed current ends
that pair's currency and the dispatcher attaches with the next counter
(section 5). `effect: none` holds for a refused query exactly as for
every refusal of section 8.

### 6.5 `host_liveness` (Host to dispatcher)

A liveness report is information from the Host. It never admits work, never
writes the dispatcher record, never opens delivery, never ends a stop and
never proves presence or absence. Service health alone admits nothing, and
a missing report is not an absence proof and changes nothing. station/1 K4
underlines the point for its own liveness notion: a self-report, heartbeat,
answered message or live bridge process is not a liveness proof and is
never merged into a termination verdict.

Closed request members: `v`, `kind`, `op` = `host_liveness`,
`request_id`, `registration_generation`, `attach_counter`, `sessions`
(the member name `sessions` is defined here). `sessions` is an
array of 0..64 entries; the bound 64 is defined here for v1 (one report
covers at most 64 logical sessions; a Host holding more sends more
reports). Each entry is a closed object with exactly
`logical_session_id`, `execution_id`, `state_version` and `alive`
(the member name `alive` is defined here;
boolean, exact `true` or `false`). `state_version` is the operation epoch
the Host last saw for that session. What `alive` attests on the Host side
(a runner connection, a native process, readiness to take input, or
another internal fact) and how the Host produces it is the Host owner's to
publish (OPEN H4). The dispatcher needs only the claim and its limits, and
this file gives the claim no effect whatever its Host meaning turns out to
be.

Closed `result` members, defined here: `reported` (integer 0..64,
entries received) and
`discarded_stale` (integer 0..64, entries discarded under the rules below).
The reply acknowledges receipt only.

Handling is per entry: the whole report is judged only by steps 1 to 4
(pin, shape, fence, and the registration pair on the report itself),
and step 5's new-operation `state_version` check never applies to its
entries, so no entry can make the report as a whole refuse stale.

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

### 6.6 `attach_wanted` (Host to dispatcher)

`attach_wanted` is the one Host-to-dispatcher notice defined here and
proposed to the Host owner (OPEN H1). It is how a Host that holds no
live registration pair — above all a Host that restarted without
keeping the current pair, or whose pair the dispatcher has ended —
tells the dispatcher so, since with one
operation per connection and no Host incarnation on the `attach` reply
there is otherwise nothing that would ever trigger a re-attach. It is
not an operation on any session.

Closed request members: `v`, `kind`, `op` = `attach_wanted`,
`request_id`. The notice carries no epoch and no session: no
`registration_generation`, no `attach_counter`, no `state_version` and
no session, execution or operation member exists on it, and a body that
carries one refuses `request_invalid`, effect: none. Closed `result`
members: none (an empty `result` object); the reply acknowledges the
wake-up only.

Authentication is by the pin only, exactly as the operations table of
section 3 states for this direction (kernel peer uid of the pinned
Host service uid plus the pinned principal directory, checked before
hello and again before the result); nothing in the body authenticates
or selects anything, because there is nothing in it to do so. Steps 3 to
6 of the section 6 check order do not refuse this notice: it admits
nothing, creates and changes no record, advances no epoch, takes and
releases no claim, and writes nothing but a wake-up of the attach logic
of section 6.1 (in particular receipt journals no intent; an `attach`
that follows journals its `attach_requested` as every attach does). Its
acknowledgement proves nothing about any session, process or outcome
and no consequence attaches to its absence: a notice that is never sent
changes nothing, and the dispatcher's attach at start and bounded retry
stand without it.

The dispatcher acts on a notice only after the pre-result pin recheck
passes; a notice that fails that recheck has changed nothing, ended no
pair and delivered no wake-up. For a notice that passes, the dispatcher
acknowledges `attach_wanted` first and runs the attach it triggers
afterwards on its unlocked call worker, never inside the notice handler:
after the acknowledgement is presented it ends the currency of any pair
it still held (section 5) and attaches with its next counter under
section 6.1, on a new dispatcher-to-Host connection. A notice that
arrives while one of its own attach attempts is in flight is coalesced
into that attempt: it is still acknowledged, and it neither ends a pair
a second time nor starts a second concurrent attach. The attach that
follows is subject to
the section 7 fence rule that no attach resumes under a fenced
generation: inside a journal fence the notice is acknowledged and the
wake-up changes nothing until clearance and a new generation let that
attach proceed. The Host
behaviour that sends it is proposed to the Host owner (OPEN H1): a Host
that holds no pair — above all one that restarted without keeping the
current pair durably — sends `attach_wanted` on its own
connection (outside any handler invocation, section 3) once it can
serve its socket and repeats it with bounded pacing (values in
OPEN S3) until an `attach` is accepted, sending it only while it holds
no live pair; a Host whose held pair is refused
`host_registration_stale` treats that pair as dead and follows the same
course. Until an
`attach` gives it a current pair it presents no pair it no longer holds
and refuses dispatcher calls (`attach`
excepted) `host_registration_stale`, effect: none, with the Host as
evaluator. A Host that kept its pair durably across its own restart
keeps using that pair and sends no notice only while no event of
section 5 has ended that pair at the dispatcher; a dispatcher restart
or any later `attach_requested` ends it there whatever the Host stored,
and the Host then learns it from the `host_registration_stale` refusal
of its next operation and follows the dead-pair course above, while the
dispatcher in parallel restarts its bounded attach retry on that same
refusal (section 6.1). Whether the Host keeps the current pair durably
in the first place is the remaining durability choice of OPEN H1; the
durable highest-accepted record is a proposed precondition there
either way.

## 7. Journal phases, loss and restart

Journal entries are opaque payloads of [journal.md](journal.md); the phase
names and the commit-before order below are this contract's. The phase
name `start_requested` is the station/1 R03 name cited exactly, and the
other names in this table are defined here for the dispatcher journal.
The entries in this table are the dispatcher's own write-ahead entries;
none of them is a station/1 record. At the station/1 pin every `Rnn` is
an entry of the station state record, written by CRO in the same
compare-and-set that commits its cell (station/1 §4.1, SR2), and the
write-ahead column for the dispatcher (`DI`) there is narrower than the
record itself: R03 "request and effect identity with the consumed
binding" (quoted whole; this file consumes no binding, which is
OPEN S4), R08 "journaled end of
the session and stop effect", R09 "claim release and the clearance of
the slot, which cites this proof", R23 "its own fence mark". The last
column below therefore names the station record each dispatcher entry
corresponds to and through which of those write-ahead lines, and never
labels a dispatcher entry with a station record id. The earlier R03
phase `lease_requested` appears nowhere in this table, for the reason
stated in section 6.2. Every entry is committed (fsync, journal
contract) before the step it precedes; an entry that cannot be committed
admits no following step.

| Order | Dispatcher journal entry | Committed before | Name, and the station record it corresponds to |
| --- | --- | --- | --- |
| 1 | `attach_requested` with the new (`registration_generation`, `attach_counter`) | The `attach` request is written | Defined here |
| 2 | `host_attached` with the accepted pair | The pair is treated as current | Defined here |
| 3 | Start operation intent: canonical arguments, both epochs, effect identities, in-flight marker | Any downstream call for that start | Dispatcher §4.1, §4.5; the `DI` write-ahead "request and effect identity with the consumed binding" of station/1 R03 (no binding is consumed here; OPEN S4) |
| 4 | `start_requested` | The physical-start request is sent | Phase name of station/1 R03; the entry is the dispatcher's own step-2 write-ahead of section 6.2, not station R03 |
| 5 | `start_unknown` for that (`logical_session_id`, `op_id`) | The operation is held for reconciliation and any later step reads its state | Defined here; the held classification corresponds to station/1 `held:start` and its R17 hold record |
| 6 | Stop operation intent (host-requested) with its effect identity | The stop effect is issued | Dispatcher §4.5; the `DI` write-ahead "journaled end of the session and stop effect" of station/1 R08, which CRO commits at the station record |
| 7 | `absence_accepted` citing the absence proof | The execution is recorded ended and its claim released | Defined here for the dispatcher journal; the acceptance it rests on is station/1 R09, and its claim release is the `DI` write-ahead of R09 that cites the proof |
| 8 | Recovery entry: each open operation found and its classification | Any resumed step after a restart | The dispatcher's own reconciliation of its journal before new work, corresponding to station/1 R10, which CRO commits at the station record |
| 9 | Journal fence entry and fence mark | Every later step after a lost, fenced or restored journal | The dispatcher's own fence mark, the `DI` write-ahead of station/1 R23, which CRO commits at the station record |

Replay retains every complete valid frame, including a phase whose reply
was never acknowledged ([journal.md](journal.md)): a replayed
`start_requested` with no recorded outcome is an unknown start, not a
start that may be sent again.

Loss and restart follow station/1's columns and the dispatcher's existing
recovery rules. Nothing below creates a second start, an ended record or
a released claim.

| Event | Rule |
| --- | --- |
| Dispatcher restart | This is station/1's "starter crash" column, never a capability report: the cells that carry it for the states this file touches include [`starting` x starter crash] = `starting; CRO; R10; no gate`, [`running` x starter crash] = `running; CRO; R10; no gate` and [`held:start` x starter crash] = `held:start; CRO; R10; no gate`. On restart the dispatcher takes exclusive ownership, replays the journal in two passes, writes its recovery entry (order 8 above, corresponding to station R10) classifying every open operation, and reconciles under §7.1 before forward admission. An operation found at `start_requested` with an unknown outcome stays unknown (`held:start`) and is reconciled by `query_operation` and receiver evidence. An operation found with its start intent journaled (order 3) but no `start_requested` is closed as not started in that classification: no physical-start request can have been sent, because `start_requested` is committed before any such request leaves (section 6.2), so recovery neither resumes it under its effect identities nor sends anything for it; a replay of that operation returns that recorded not-started result and nothing resumes it under a replacement identity for the same start. Only after classification does the dispatcher attach again, with the next `attach_counter`; the pre-restart registration epoch is dead and refuses stale. |
| Host loss (socket gone, connect refused, peer died) | No new start: no `host_start` can arrive and the dispatcher starts nothing on its own. Open operations stay unknown until reconciled; claims, quotas, fences and delivery closure are retained exactly as §12 states for host failure. Pre-send dispatcher-to-Host failures are `host_unavailable`; a failure after bytes were written is an unknown outcome of that operation. A missing liveness report proves nothing. |
| Host restart or reconnect | Two cases, by whether the Host kept the current pair durably (the remaining choice in OPEN H1; a durable highest-accepted record is a proposed precondition there in both cases). A Host that kept its pair durably keeps using that pair, with no re-attach needed or performed and its operations and answers proceeding under it, only while no event of section 5 has ended that pair at the dispatcher; once a dispatcher restart or a later `attach_requested` has ended it there, the stored pair is dead on both sides, the Host's next operation refuses `host_registration_stale` which restarts the dispatcher's bounded attach retry, and (proposed) the Host treats the pair as dead and repeats `attach_wanted` until re-attached (section 6.6). A Host that restarted without keeping the current pair holds no pair: the old pair is dead at the Host, it sends `attach_wanted` (section 6.6), repeated with bounded pacing until an attach is accepted and only while it holds no live pair, and refuses dispatcher calls `host_registration_stale` until attached; the dispatcher still counts the old pair current only until it accepts that notice or receives that refusal for the pair, whereupon it ends the pair's currency and attaches with the next counter (section 6.1), after which the old pair refuses stale on both directions and its queries are re-issued only as reads under the new pair. The restarted Host presents no pair in the window in between, so nothing is admitted on the strength of the old pair in that window either. Whatever the Host still holds after its restart is learned again by observation and reports, not assumed: replayed ready Host metadata without live evidence settles nothing (the Host's own stated rule on its manager seam, applied here as a limit, not a borrowed guarantee). |
| Lost, fenced or restored dispatcher journal | This corresponds to station/1 R23 (the dispatcher's entry is its own fence mark, order 9 above) and is governed by dispatcher §11.1. The dispatcher writes its fence mark, records nothing as ended, no registration pair stays current, and every Host-to-dispatcher operation (`attach_wanted` alone excepted, section 6 step 3) whose record cannot be cleared refuses `station_session_journal_fenced`, effect: none, at that step before any epoch check — a pre-intent refusal in this file, where station/1 evaluates the same verdict at start gate 9, below the R03 record line, as a recorded P4 that leads to `stopping` (recorded in OPEN S1/S4; evaluator `DI` in both). Forward admission stays node-wide fenced until the §11.1 external fence, reverse inventory and operator disposition complete; a restored snapshot certifies no completeness of its own. Registration counters journaled only in the lost journal cannot be proved monotonic, so attach does not resume under the fenced generation: after clearance the operator path installs a new `installation_generation` that must exceed every generation ever accepted at either side, proved against a record kept outside the restored journal, and only then may attach proceed; a restored journal that predates the last installed generation proves nothing about that floor, and where that generation floor lives is stated here only as outside the restored journal, with its encoding left open in OPEN S6. The same loss removes the per-session `state_version` high-water marks and the (`logical_session_id`, `op`, `op_id`) replay keys, so after clearance a `host_start` is judged new only against a floor kept outside the restored journal (section 6.2), at section 6 step 3: one with no surviving record whose `op_id` or `state_version` cannot be proved new against the station record (OPEN S1) or the operator's clearance inventory refuses `station_session_journal_fenced`, effect: none. During the fence `host_stop` is unavailable on this seam (section 6 step 3) and stops run only off this seam — operator stop, R-ALLSTOP and the CRO-requested post-R23 stop — until clearance, the new generation and attach restore this seam. The clearance itself is, at the station/1 pin, the dispatcher's record citing the R09 of the account; its encoding and the encoding of that floor are OPEN S6. |
| Ambiguous reply, either direction | The same-effect rule of §4.5 and the reply rule of §8.3: repeat the same operation with the same `request_id` and the same operation identity (`op_id`) — for `host_start`/`host_stop` carrying the registration pair current at the repeat, which is no part of their comparison — or observe. Never a new `op_id`, never a second physical effect, never a fabricated receipt. `attach` is the stated exception to same-request repetition: it is never resent under the same counter, and its next attempt uses the next counter with a fresh `request_id` (section 6.1). |

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
codes of this contract. Each row names its evaluator where it
evaluates: for codes the Host evaluates on its own socket or under the
proposed rules of sections 6.1, 6.4 and 6.6 the evaluator is the Host,
proposed to the Host owner, and the row says so; everywhere else it is
the dispatcher (`DI`).
A refusal enforced by closing a connection with zero bytes (the transport
contract's disposition for an unpinned peer or an untrusted endpoint)
carries its code as the local recorded reason; no bytes are written to
present it.

| Code | Evaluator and source | When | Effect |
| --- | --- | --- | --- |
| `caller_not_permitted` | Dispatcher, Appendix A | Kernel peer uid is not the pinned Host service uid, or the Host principal pin fails at load, before hello or before the operation's intent is journaled. This includes every section 6 frame from a non-Host peer admitted for hello when no `--host-config` entry is attached (section 3); there is no separate unregistered-Host code. A pin failure found only at the pre-result recheck after intent is not this refusal: the result is withheld and the operation stays recorded (section 4). A peer outside the transport caller pins at all is closed with zero bytes before hello under the transport rules, with this code as the recorded reason. | none |
| `request_invalid` | Dispatcher, Appendix A; also the Host (proposed to the Host owner) for the same cause on its socket | Non-canonical bytes; unknown, missing, duplicated or cross-operation member; value out of its bound; wrong `v` or `op` in a body that reached section 6; an operation offered in the wrong direction; an epoch or session member on `attach_wanted`; an extra member in a reply, including the omitted `stage` member of section 5 and an unpublished Host member on `attach`. A frame whose `kind` is any other value is not a section 6 body at all: from the Host peer it is a zero-result close only (section 3), never this code. | none |
| `host_registration_stale` | Dispatcher, defined here (station/1 has no code for a registration epoch); and the Host, defined here and proposed to the Host owner, where the Host evaluates (below) | At the dispatcher: an operation other than `attach`/`attach_wanted` whose pair is not the pair current at that moment (`registration_generation` not the accepted `installation_generation`, or `attach_counter` lower or higher than the current counter, or any pair while no pair is current, including the pair of a dead attach and a byte-carried dead pair on an otherwise exact replay); a configured generation lower than the journaled one at load. At the Host (proposed, sections 6.1, 6.4, 6.6): an `attach` whose generation is lower than its highest accepted generation or whose counter is at or below its highest accepted counter of that generation, whatever its `request_id` (`attach` has no replay exception and returns no recorded result); a dispatcher call presented to a Host that holds no current pair, or with a pair that is not its highest accepted pair. The same input never maps to two codes: an old pair under a superseded generation is this code alone, at whichever side judges it. At the dispatcher, this refusal while no pair is current also restarts the bounded attach retry (section 6.1); the refused operation itself still has effect: none and writes nothing beyond that attach's own entries. | none |
| `host_registration_conflict` | Dispatcher, defined here | Only the load-time case of section 4: the same `installation_generation` loaded with different configuration bytes than the journaled digest. No cross-generation counter comparison exists anywhere in v1, so this code never arises on the wire in either direction. | none |
| `host_socket_untrusted` | Dispatcher, defined here, local refusal with zero bytes written | On the dispatcher-to-Host direction, before connect or after it: `socket_path` is not a socket, its owner is not `socket_owner_uid`, a parent component is a symlink or has an untrusted owner or mode, or the kernel peer uid after connect is not `host.uid`. A `socket_path` too long for `sun_path` never reaches this check; section 4 refuses it at load. | none |
| `host_unavailable` | Dispatcher, defined here, local refusal | Connect to the verified Host socket fails, or the connection fails before any request byte of this operation is written. A failure after bytes were written is never this code; it is an unknown outcome. | none |
| `station_state_stale` | Dispatcher on this seam, station/1 §4.3 code verbatim; station/1 §4.3 at the pin names CRO as this code's one evaluator, so the attribution on this seam is the stated conflict of OPEN S2 | A new `host_start` or `host_stop` in the sense of section 6 step 5 (replay key not recorded in either operation) carries a `state_version` equal to or lower than the highest committed version journaled for its `logical_session_id`, or an effect owner has already seen a higher epoch for the station. A recorded key presented with a changed `state_version` is `request_id_conflict`, never this code. A replay in the comparison set of section 5 is not refused under this code, and neither is a `host_liveness` entry or a `query_operation` (section 6 steps). | none |
| `request_id_conflict` | Dispatcher, Appendix A | Same replay key (`logical_session_id`, `op`, `op_id`) as a recorded `host_start` or `host_stop` with a different compared canonical argument (`execution_id`, `controller_epoch` or `state_version`), or the same (`logical_session_id`, `op_id`) as a recorded operation presented under the other `op`. Despite this code's name, `request_id` itself is never looked up or compared on this seam and a reused `request_id` under another replay key is not this code. A different registration pair is never by itself this code: the current pair carries the replay to its record and a dead pair is `host_registration_stale`. | none |
| `run_not_owned` | Dispatcher, Appendix A | `host_stop` (or a result lookup behind it) names an execution that is not under the permit of the named `logical_session_id`, including an execution under another session's permit. | none |
| `agent_busy` | Dispatcher, Appendix A and §12 | `host_start` for a session whose previous execution claim is not confirmed absent. Unknown counts as not absent. | none |
| `capacity:hold` and the other `capacity:*` codes of Appendix A | Dispatcher, §§8.3 and 9 | `host_start` while forward admission is closed for recovery, hold, maintenance, restore or a temporary capacity gate. `host_stop` and reconciliation keep the recovery availability that §8.3 already gives them; this row creates no new closure for them. | none |
| `station_session_journal_fenced` | Dispatcher as `DI`, station/1 §4.3 (code verbatim) | For a Host-to-dispatcher operation (`attach_wanted` excepted), at section 6 step 3 right after the pin and shape checks and before the registration check: the dispatcher's session journal is lost or fenced and the slot has no clearance (section 7, corresponding to station/1 R23), `host_stop` included for the whole fence (OPEN S4). Also at step 3, after clearance, a `host_start` with no surviving record whose `op_id` or `state_version` cannot be proved new against the floor kept outside the restored journal (sections 6.2 and 7), before steps 4 to 6 are asked. At station/1 the same verdict is evaluated at start gate 9 below the R03 record line as a recorded P4; in this file it is a pre-intent refusal (OPEN S1/S4). | none |

No other refusal code is defined by this file in v1, and the codes above
are the whole of its refusal surface. Two boundaries of that statement
are stated, not hidden: there is no dispatcher code for a stale
`controller_epoch`, because the dispatcher does not evaluate that epoch
(section 5), and the dispatcher §12 successor refusals of `host_start`
have no published codes yet, are mapped onto no code above, and keep
`host_start` disabled until OPEN S4/S5 publish them (section 6 step 6).
In particular there is no code by which a liveness report, a Host reply
or the passage of time can produce an ended record, a released claim or
an admitted start; those outcomes need the absence acceptance and
admission gates their own sections name.

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
| Socket path too long for `sun_path` | Valid file except `socket_path` is 108 bytes | Startup refuses locally at load, zero protocol bytes, daemon does not serve; it never loads and fails connects as `host_socket_untrusted` | The same file with a 107-byte `socket_path` loads and serves, and `attach` to that socket proceeds |
| Generation reused with changed bytes | Generation `G` journaled with its digest; file edited to a new `socket_path`, generation still `G` | Load refuses `host_registration_conflict`, effect: none; journaled generation and digest unchanged | The same edited file with generation `G+1` loads, and the first attach under `G+1` at its counter 1 proceeds even though counter 1 was already accepted under `G` (a higher generation opens a fresh counter space) |
| Wrong caller uid | Current attach in force. A peer with a pinned transport caller uid other than `host.uid` sends an otherwise valid `host_start` | `caller_not_permitted`, effect: none. No intent journaled, no downstream call | The identical request from peer uid `host.uid` with a valid pin is journaled and proceeds |
| Unpinned caller | Current attach in force. A peer uid in no transport pin connects | Zero-byte close before hello, recorded reason `caller_not_permitted`, effect: none | The pinned Host peer completes hello and its operation proceeds |
| No host configured | No `--host-config` is attached. A server-listed caller admitted for hello sends an otherwise valid `host-request/1` `host_start`; separately, a peer in no pin connects | The admitted caller's frame refuses `caller_not_permitted`, effect: none, with no intent journaled and no downstream call; the unpinned peer closes with zero bytes before hello. No other code answers either input | The same `host_start` frame with the entry attached, sent from the pinned Host peer under the current pair, is journaled and proceeds |
| Notice or liveness from a non-Host peer | Current attach in force. A peer with a pinned transport caller uid other than `host.uid` sends, in turn, an otherwise valid `attach_wanted` and an otherwise valid `host_liveness` with the current pair | Each refuses `caller_not_permitted`, effect: none. No wake-up is delivered, no pair ends, no `attach_requested` is journaled for the notice, and no report entry is retained | The identical notice and report from peer uid `host.uid` with a valid pin: the notice is acknowledged and its attach follows, and the report is acknowledged as information only |
| Wrong `kind` from the Host peer | Current attach in force. The pinned Host peer sends an otherwise section-6-shaped `host_start` body whose `kind` is one other string | Zero-result close only: the frame is not a section 6 body, no `request_invalid` result is presented, and no intent is journaled | The identical body with `kind` exactly `swarma-dispatcher host-request/1` is routed to section 6, journaled and proceeds |
| Host principal pin replaced before intent | Between load and an admitted `host_start`, the replacement of the pinned Host directory with a different inode is found at the before-hello pin check, before any intent is journaled | Operation refuses `caller_not_permitted`, effect: none. No intent journaled, no downstream call | The original pinned directory unchanged: the same operation is journaled and proceeds |
| Host principal pin replaced after intent | The intent of a `host_start` is journaled and its downstream call made; the pin replacement is found at the pre-result recheck | No result is presented on that connection; this is not a refusal of the recorded operation and claims no effect: none for it. Journal shows exactly the one intent and its effect set, the record stays exactly as recorded, and a later conforming replay of that `op_id` under a pin that passes returns that record with no second intent | The pinned directory unchanged throughout: the same operation's result is presented |
| Wrong socket owner | `socket_path` names a real socket whose owner is a different uid than `socket_owner_uid` | Dispatcher-to-Host attempt refuses `host_socket_untrusted` locally; zero request bytes written; no `query_operation` answer is acted on | The socket at the same path owned by `socket_owner_uid` with peer uid `host.uid`: `attach` proceeds |
| Wrong socket peer uid | Socket owner correct, but the accepting process has a peer uid different from `host.uid` | `host_socket_untrusted`, zero request bytes written, effect: none | Accepting process with peer uid `host.uid`: `attach` proceeds |
| Host down before send | No listener at the verified `socket_path` | `host_unavailable` locally; attach counter stays consumed; no operation is admitted under that counter | Listener present and verified: the next attach with the next counter completes |
| Stale registration epoch (low) | Current epoch `(G, 7)`. The Host sends `host_start` with `attach_counter` 6, all else valid | `host_registration_stale`, effect: none. No intent journaled, no downstream call, high-water `state_version` unchanged | The identical request with `attach_counter` 7 is journaled and proceeds |
| Stale registration epoch (high) | Current epoch `(G, 7)`. The Host sends `host_start` with `attach_counter` 8 | `host_registration_stale`, effect: none. A future counter is not adopted and does not advance the current epoch | After a real dispatcher attach commits counter 8, the identical request with `attach_counter` 8 proceeds |
| Operation before any attach | No successful attach yet in this dispatcher lifetime. The Host sends `host_start` carrying a pair it chose | `host_registration_stale`, effect: none. The presented pair is not adopted | After the dispatcher attaches, the same `host_start` carrying the attached pair proceeds |
| Duplicate or replayed attach | Counter 7 is the highest counter the Host has accepted under `G` (`host_attached` at the dispatcher). A further `attach` under `G` arrives at the Host with counter 7 — byte-identical with the same `request_id`, or with a new `request_id`, immediately or much later — and likewise a replay of the already superseded counter 6 | Every one refuses `host_registration_stale` at the Host, effect: none, and no recorded attach result is returned to any of them; the refusal does not depend on elapsed time or connection order | An attach with counter 8 under `G` establishes the new epoch |
| Attach under a superseded generation | The Host's highest accepted generation is `G+1` (highest accepted counter 3). An `attach` arrives under `G` with counter 99, a value never accepted under any generation | `host_registration_stale` at the Host, effect: none. That is the only code this input maps to (never `host_registration_conflict`) | An attach under `G+1` with counter 4 establishes the new epoch; under a newly installed `G+2`, its counter 1 likewise proceeds |
| Attach reply lost | Counter 7 sent and accepted by the Host; the reply never reaches the dispatcher | Pairing is not current under 7; counter 7 is never resent; no operation is admitted under 7 by either side's later claim | The next attach with counter 8 completes and operations under `(G, 8)` proceed |
| Stale state version (equal) | Session high-water `state_version` is 41 after a committed start, and the previous execution's absence is accepted (station/1 R09), so no `agent_busy` is in play. A new `host_start` with a new `op_id` carries `state_version` 41 | `station_state_stale`, effect: none. No claim taken, no downstream call | The identical request with `state_version` 42 is journaled and proceeds |
| Stale state version (lower) | Session high-water `state_version` is 41 and execution `E` is running under the permit of the named `logical_session_id`. A new `host_stop` with a new stop `op_id` for `E` carries `state_version` 40 | `station_state_stale`, effect: none. Execution `E` is not signalled and no stop intent is journaled | The identical `host_stop` with `state_version` 42 journals its intent and proceeds |
| Replayed `host_start`, same operation id | The start for replay key (`logical_session_id`, `host_start`, `op-1`) is journaled under epoch `(G, 7)`. After a re-attach has made `(G, 8)` current (including after a restart of either side that kept its records), the Host repeats the operation with the same `op_id` and same `request_id`, the same `execution_id`, `controller_epoch` and `state_version`, and pair `(G, 8)` | The recorded result and current `record_state` are returned. Exactly one claim, one effect set and at most one process exist for `op-1`; journal shows no second `start_requested` for it, and no conflict is raised over the changed pair | A second, genuinely new start uses a new `op_id`, a higher `state_version`, and only after confirmed absence, and proceeds as its own operation |
| Stop reusing a start's operation id | A start `op-1` for execution `E1` is recorded at `state_version` 41 under the current pair. A `host_stop` arrives with `op_id` `op-1`, target `E1`, the same `controller_epoch` and `state_version` 41 | `request_id_conflict`, effect: none. The start's recorded result is not returned as a stop reply, no stop intent is journaled and `E1` is not signalled | The same stop with its own new stop `op_id` and `state_version` 42 journals its intent and proceeds; and the start replay itself, sent as `host_start`, returns the start record |
| Reused `request_id` under another key | Start `op-1` is recorded and was sent with `request_id` `r-1`. A genuinely new start `op-2` (new replay key, higher `state_version`, previous absence accepted) is sent reusing `request_id` `r-1`, its hello on the new connection also carrying `r-1` | `op-2` is judged as its own new operation and journaled, with `r-1` as correlation only; `op-1`'s recorded result is not returned for it and no `request_id_conflict` is raised over the reused id | The same `op-2` with a fresh `request_id` is likewise journaled as its own operation |
| Recorded key, changed state version | `op-1` is recorded at `state_version` 41 and the session high-water mark is 41. `op-1` is repeated under the current pair with `state_version` 40, all other compared arguments equal | `request_id_conflict`, effect: none, never `station_state_stale`. The recorded operation is unchanged | The identical repeat at the recorded `state_version` 41 returns the recorded result |
| Capacity closed on `host_start` | Current attach in force, a new valid `host_start` at a higher `state_version` with previous absence accepted, and forward admission closed by a capacity gate (for example hold) at section 6 step 6 | The applicable `capacity:*` code (here `capacity:hold`), effect: none. No intent journaled, no claim taken, no downstream call | The identical request once that capacity gate opens is journaled and proceeds |
| Replayed `host_start` carrying a dead pair | The same repeat as the previous row, but carrying the dead pair `(G, 7)` while `(G, 8)` is current | `host_registration_stale`, effect: none, before the replay is looked up. The recorded operation is unchanged and no result for it is presented on those bytes | The same repeat carrying the current pair `(G, 8)` returns the recorded result (previous row) |
| Same operation id, different arguments | `op-1` replayed under the current pair with a changed `execution_id` | `request_id_conflict`, effect: none. The recorded operation is unchanged | The replay in the comparison set (same `execution_id`) returns the recorded result (previous rows) |
| Same operation id, different controller epoch | `op-1` replayed under the current pair with a changed `controller_epoch`, all other compared arguments equal | `request_id_conflict`, effect: none. This is a comparison failure, not a dispatcher judgement that the epoch is stale (section 5); the recorded operation is unchanged | The replay with the recorded `controller_epoch` returns the recorded result |
| Reply lost after `start_requested` | Journal holds `start_requested` for `op-1`; the physical start reply is dropped before the dispatcher can classify it | Operation goes to `start_unknown`, claim retained, classification `held:start`. Reconciliation runs `query_operation` for `op-1` plus receipt and process observation. No second start request for `op-1` under any id. A Host repeat with the same `request_id` and `op_id`, carrying the pair current at the repeat (after any re-attach), returns the open record | The same start with the reply delivered records its outcome, and `record_state` advances by that evidence alone |
| Dispatcher restart mid-start | Dispatcher stops after `start_requested` for `op-1` is committed under `(G, 7)` and before its outcome is recorded, then restarts | Journal replays; its recovery entry (corresponding to station/1 R10) classifies `op-1` unknown; no new start for the session is admitted; the pre-restart epoch refuses `host_registration_stale`. After classification the dispatcher re-attaches with the next counter and reconciles `op-1`; a Host repeat of `op-1` under the new current pair returns the open record; no second `start_requested` is written for it | A restart in which `op-1` had a recorded terminal outcome before the stop replays to that outcome, and a new start with a new `op_id` proceeds if absence is confirmed |
| Host restart mid-start, current pair not kept | The Host restarts without keeping the current pair (its durable highest-accepted record, where the H1 precondition is met, remains) while `op-1` is at `start_requested` and unknown. It sends `attach_wanted`; a `query_operation` the dispatcher issues for `op-1` under the pair it still believes current reaches the restarted Host before the re-attach completes | Open operation stays unknown and unreleased. The restarted Host refuses that query `host_registration_stale`, effect: none; the dispatcher ends the old pair's currency, re-attaches with the next counter and re-asks `query_operation` for `op-1` under the new pair, as a read only. From the new `attach_requested` on, the old pair refuses stale on both directions. No second start | The Host that kept its pair durably across the same restart, with no section 5 event having ended it at the dispatcher (or did not restart), answers the same query under the still-current pair, and reconciliation proceeds on that evidence with no re-attach |
| Durable Host returns after the retry bound | Pair `(G, 7)` was current. The dispatcher restarts (ending `(G, 7)` at the dispatcher) while the Host is down; its attaches fail `host_unavailable` until the bounded retry is spent, so it holds no pair and no operation is open. The Host then returns holding its durably kept `(G, 7)` and sends its first `host_start` under that pair, without any `attach_wanted` | That first operation refuses `host_registration_stale`, effect: none for the operation, and that refusal while the dispatcher holds no current pair restarts the bounded attach retry; a re-attach makes a new pair current. No intent for the refused operation is journaled and nothing is written beyond the attach's own entries | The same `host_start`, repeated under the newly attached current pair with the same `op_id`, is then judged as the new operation it still is, journaled and proceeds |
| Journal fenced | Dispatcher journal replaced by a restored copy, the dispatcher's fence mark written (corresponding to station/1 R23), clearance not complete, and no registration pair current because the restore killed it. A `host_start` arrives that would be valid but for the fence (including one carrying the dead pre-restore pair) | `station_session_journal_fenced`, effect: none, at step 3 before the registration check that would otherwise answer `host_registration_stale`. Nothing is recorded ended; claims of possibly-running executions are not released; attach does not resume under the fenced generation | After §11.1 clearance and a newly installed generation, attach completes and the same shape of `host_start`, with an `op_id` and `state_version` proved new against the floor outside the restored journal (next row), is admitted under the new epoch |
| Journal fenced: pre-loss operation re-presented after clearance | Before the loss a start for `op-1` at `state_version` 41 was journaled only in the lost journal. The external floor (clearance inventory) lists `op-1` and floor 41 for the session; clearance completes, a new generation is installed and attached, and the previous execution's absence is accepted (station/1 R09). The Host repeats `op-1` with the same request identity, `state_version` 41 and the new current pair (and mutatis mutandis: any `host_start` with no surviving record whose `op_id` or version the floor cannot prove new) | `station_session_journal_fenced`, effect: none, at step 3 before steps 4 to 6, never `station_state_stale`. No intent journaled, no second `start_requested` for `op-1`, no claim taken; the restored high-water mark does not make the repeat a new operation | A genuinely new `op-2` with `state_version` 42, both proved new against the same external floor, is journaled and proceeds |
| `attach_wanted` carrying a session | The Host has restarted without keeping the current pair and the dispatcher has not yet re-attached. The Host peer sends `attach_wanted` with one extra member `logical_session_id` | `request_invalid`, effect: none. No wake-up is delivered and no `attach_requested` is journaled on those bytes; downstream Host-socket attempts show no attach caused by them | The same notice without that member is acknowledged with an empty result first, and the dispatcher then ends any pair it still held and attaches with the next counter on its unlocked call worker |
| `attach_wanted` during a fence | Dispatcher journal fenced, clearance not complete, no pair current. The pinned Host peer sends a valid `attach_wanted` | The notice is acknowledged with an empty result and changes nothing: no `attach_requested` is journaled, no attach is attempted under the fenced generation, and downstream Host-socket attempts show no attach caused by it | The same notice after §11.1 clearance and a newly installed generation is acknowledged and the dispatcher attaches under the new generation |
| Stop ended by Host reply alone | `host_stop` accepted and the Host reports its side done; account-wide absence proof not accepted | Record stays `stopping`; no `absence_accepted` entry; claim not released; a new start refuses `agent_busy` | The same stop with the qualified absence proof accepted journals `absence_accepted`, records the end and releases the claim; a successor start may then proceed |
| Stop of a foreign execution | `host_stop` names an `execution_id` belonging to a different session's permit than the named `logical_session_id` (the other session's permit also belongs to this, the one, Host) | `run_not_owned`, effect: none. That execution is not signalled | `host_stop` naming an execution under the permit of the named `logical_session_id` journals its intent and proceeds |
| Liveness report claiming a start | Report entry with `alive` `true` for an (`logical_session_id`, `execution_id`) the dispatcher never journaled | Entry discarded (`discarded_stale` counts it). No record created, no claim taken, no input admitted, `record_state` of every real record unchanged | The same report about a journaled running execution at the current `state_version` is counted as reported information, with still no record change at all |
| Liveness silence after a start | Running execution, then no `host_liveness` report arrives for an arbitrary long interval | Nothing changes: no stop, no release, no absence, no successor admitted on the silence | The same execution later reported `alive` `true` at the current version is retained as information only |
| Liveness with stale version | Report entry for the running execution with `state_version` one below the journaled version | Entry discarded as stale; the record is unchanged | The same entry at the journaled version is retained as information |
| Query answered `not_recorded` | `op-1` unknown after `start_requested`; Host answers `query_operation` with `outcome` `not_recorded` and complete coverage | Known absent for the Host-side effect only. The start is not retried, the record is not closed as never-run without the remaining receiver and receipt evidence, and no claim is released on this answer alone | With the start receipt and process evidence also proving the recorded outcome, that outcome is journaled for `op-1` under §4.6 mapping |
| Query answered `unavailable` | Host answers `outcome` `unavailable` (or the query reply is lost) | Unknown. `op-1` stays open and unreleased; repeating the query sends no effect | A later `recorded` answer for `op-1`, combined with the dispatcher's recorded result, the start receipt and process evidence, classifies the operation under §4.6 |
| Query answered `recorded` alone | `op-1` unknown after `start_requested`; Host answers `outcome` `recorded`, and no start receipt or process evidence is available beside it | Still unknown. `op-1` stays open and unreleased, `record_state` is unchanged, and the answer is not mapped to known present or known absent: this wire carries neither the recorded result nor a current target state | The same answer with the start receipt and process evidence proving the outcome journals that outcome for `op-1` under §4.6 mapping |

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
| OPEN H1 | Durable attach state on the Host side, and `attach_wanted` | Proposed to the Host owner, with every Host behaviour in sections 6.1, 6.4 and 6.6 proposed to that owner. As a proposed **precondition** of qualifying this seam: a durable highest-accepted (generation, counter) record — how the Host durably stores the highest accepted generation and `attach_counter` per generation across its own restart, before it replies to the `attach` that advances it — without which a replayed attach after a Host restart can be accepted at the Host and only the dispatcher's current-epoch check fences it (section 6.1). Keeping the current pair itself durably across a restart stays **optional**: if it does, it keeps using that pair and sends no notice only while no section 5 event has ended the pair at the dispatcher; if it does not, or its held pair is refused `host_registration_stale`, it treats that pair as dead and sends the `attach_wanted` notice defined here and proposed here (section 6.6; authenticated by the pin only, no epoch, no session), repeated with bounded pacing (OPEN S3) until an attach is accepted and only while it holds no live pair, and refuses dispatcher calls (`attach` excepted) `host_registration_stale`, effect: none, itself as evaluator, until attached. Also open: whether the attach reply needs a Host incarnation or Host epoch attestation member beyond the echo of section 6.1. The Host's manager-seam documents retain a "Host epoch" in their own admission object; its name, meaning and availability on this socket are unpublished, so v1 carries none. |
| OPEN H2 | The Host listener | The socket's deployment contract: path ownership and parent-directory protection, socket mode and group so that exactly the dispatcher service uid may connect, listener lifecycle across Host restart, stale-socket handling, and what the Host checks about the dispatcher's peer uid, including confirmation of the section 3 rules (proposed there) that the Host issues no synchronous Host-to-dispatcher call from inside its `attach` or `query_operation` handler and that the Host listener serves one operation per connection in the dispatcher-to-Host direction (section 3), and whether that direction has any hello or version exchange before the operation at all, and if so its shape (no such exchange is defined here and no member for it is invented here). Peer-credential availability and strength on the target platform must be the Host's published statement (this seam is Linux only in v1, section 3); prototype fixtures that settle for a peer pid and uid observation do not settle it. |
| OPEN H3 | `query_operation` answers | For each `outcome` value of section 6.4: what Host-side fact it attests (for `recorded` in particular, what if anything it can prove about a physical start the dispatcher owns, beyond being combined at the dispatcher with its own recorded result, the start receipt and process evidence, since until then this file classifies it unknown), what complete coverage means for `not_recorded`, how the answer is serialized with the Host's own operation writes, whether the Host checks the query's `state_version` (defined here as the operation's recorded version) for equality against its own record of that operation and what a mismatch answers, its refusal of a query that carries no current pair (`host_registration_stale`, proposed in section 6.4, Host as evaluator), and the retention floor for operation records on this seam. The retention and capacity figures published for the Host's private manager ledger belong to that seam and are not assumed here. |
| OPEN H4 | Liveness content | What `alive` attests (runner connection, native process, input readiness or another internal fact), how it is produced, and at what interval reports are sent. This file gives the value no effect, so the answer can be narrow; it is still the Host's to publish, because a consumer must know what it is displaying. Whether 64 entries per report is the right bound is part of the same answer. |
| OPEN H5 | Controller epoch and the delivery gate | Confirmation that the Host's controller epoch is a monotonic integer inside the section 5 bound, how it advances on Host replacement, and the registration-and-readiness-before-delivery join of dispatcher §12 (runner channel authentication, native-session lock) that an accepted `host_start` result feeds into. That join is outside this wire and remains an activation gate. |
| OPEN H6 | Host client behavior on loss | The Host's own durable intent before it asks for `host_start` (the book's "persists intent with delivery closed"), and its implementation of the same-operation repeat after a lost reply, after a refusal before intent and after a re-attach: it keeps the same `op_id` and the same `request_id` (carried in the hello of the new connection as well as the operation, section 3; correlation only, never a replay lookup, section 5), carries the registration pair current at the repeat (which the dispatcher checks for currency only and never compares, section 5), never mints a new `op_id` for the same start and never starts anything itself while the dispatcher answer is unknown. The one stated exception to never minting is after a journal fence: only once clearance and operator disposition, reaching the Host outside this wire (OPEN S6), state the pre-loss start dead may it treat that start as dead and mint a new operation with a new `op_id` and a `state_version` proved new against the external floor (sections 6.2 and 7); until then a repeated pre-loss `op_id` only repeats `station_session_journal_fenced`, effect: none. How that disposition is conveyed to the Host is the Host and operator owners' to publish; no member for it is defined here. |

### 11.2 For station/1 and joint publication (station/1 at `3463191e`)

station/1 is an unreleased draft at the cited pin. The rows and codes this
file cites are cited at that pin; a later accepted station/1 head requires
a re-pin of section 2 and a re-check of every cited row before this seam is
qualified.

| Id | Question | State at the pin, and what must settle it |
| --- | --- | --- |
| OPEN S1 | The OQ18 join (SR6) | At the pin, SR1-SR3 make the station operation service the single writer of the station state record and of the state version, and SR6 leaves open whether that record or the dispatcher's session record is authoritative. The operator's ruling of 2026-11-16 assigns the standing-run record to the dispatcher and keeps the station description in the workbench registry, read-only to the dispatcher. station/1 must incorporate the ruling in its own revision cycle: which record the dispatcher-held run record is a projection of, or replaces; who performs the compare-and-set that mints the `state_version` a `host_start` carries; through which read the dispatcher learns it (the registry read format is also undefined here); who commits, at the station record, R03 (including whether its phase `lease_requested` ever becomes a dispatcher write-ahead, which section 6.2 keeps out of the dispatcher's order), R08, R09, R10 and R23 once the dispatcher holds the run record (at the pin CRO commits every one of them and the dispatcher's entries of section 7 only correspond to them through the narrower `DI` write-ahead lines); whether `attach` needs a station scope; and the gate-placement difference also recorded in OPEN S4, that this file refuses the gate 9 journal verdict before intent where station/1 records it as P4 below the R03 line. For that reconciliation the station owners must see the exact pin text this seam's Host-to-dispatcher `host_start` sits beside: station/1 §6.1, "the physical start has one path in every case, CRO to the dispatcher to the launcher", and SR3, "a service performs an effect for a station only under an operation that CRO recorded first", softened only by §13's ownership row for start and stop, "CRO and the session host ask". Until then section 5's high-water rule is fencing at the effect owner only. |
| OPEN S2 | Who evaluates `station_state_stale` on this seam: a stated conflict | This is a conflict for the station owners to decide, not a confirmation this file asks for. At the pin, station/1 §4.3 gives every code exactly one evaluating service and makes `station_state_stale` CRO's, carrying "the effect owner's own stale-request detail", with CRO returning an evaluator's code before a record and the code arriving in P4 after one; at that pin the arrangement is settled the other way from this file. This file follows SR3's path (an effect owner that has seen a higher epoch refuses and the requester receives `station_state_stale`) with the dispatcher itself surfacing the code to the Host on this seam, because no CRO is on this wire. The station owners must decide one of: amend the evaluator or routing of the code for this seam, route the dispatcher's stale refusal through CRO, or give the dispatcher its own detail code to be carried under CRO's code; what stale-request detail may be carried and by whom belongs to the same decision. Until it is taken, section 8 records the dispatcher as evaluator on this seam expressly against the pin, and nothing here renames the code. |
| OPEN S3 | Clock, deadline and attach-retry values | station/1 §4.5 leaves values open: `start_detect` runs on the dispatcher policy `detect` bound, `end_to_absence` is its OQ21, and `liveness_interval` with `silence_deadline` is its OQ4. The dispatcher owner and the station owners must publish the values, together with the bounded attach retry of section 6.1 (attempt count, pacing between attempts, and the overall bound, each attempt consuming the next counter), the bounded pacing of the proposed repeated `attach_wanted` (sections 6.1 and 6.6), and the finite per-call deadlines for the dispatcher-to-Host direction only; the Host-to-dispatcher direction already carries the transport 2 s stage and 5 s session deadlines (section 3) and this item does not reopen them. This file sets none of the open values; no timeout in v1 may end a record, release a claim or prove absence whatever values are chosen (station/1 U4). |
| OPEN S4 | The validated binding and start gates 8-18 | The form of the validated binding is station/1's OPEN OQ19, and the start gates below the R03 record line (including `station_binding_stale`, `station_lifecycle_not_permitted`, `station_plan_changed`, capacity and launcher gates) are owned there and in dispatcher §§3.4 and 12. This file consumes no binding and relaxes no start gate; joint publication must state how those verdicts surface on the section 6.2 result without renaming station codes, and must publish the refusal codes of the dispatcher §12 successor rows, which have none yet, since `host_start` stays disabled until then (with OPEN S5, section 6 step 6). It must also settle a stated placement difference: gates 9 (session journal), 11 (lifecycle) and 14 (capacity) are at station/1 `DI` verdicts below the R03 record line that arrive as a recorded P4 and lead to `stopping`, whereas this file makes `station_session_journal_fenced` (step 3), the lifecycle/successor verdicts and its `capacity:*`/`agent_busy` answers pre-intent refusals with effect: none; which placement a qualified seam implements is for the station and dispatcher owners to decide jointly, without renaming any code. Beside gate 9 the same joint decision must reconcile a stop difference this file keeps closed: here a Host-requested stop is unavailable for the whole journal fence at step 3 (operator stop, R-ALLSTOP and the CRO-requested post-R23 stop remain off this seam, sections 6, 6.3 and 7), whereas station/1 G4 states "a stop effect is never refused after its record" and uses `station_session_journal_fenced` only for provision G1 and start gate 9; the station owners must reconcile that sentence with this seam's fence closure, whose reason is that the record making stop identity sound is the lost one. |
| OPEN S5 | Successor authority | The session lifecycle authorisation member, the ordinary restart budget (station/1's OPEN OQ3), permit issuance and withdrawal encodings, who requests a start after a reboot, and the refusal codes with evaluators for every dispatcher §12 successor row (which the station codes of gates 10-14 overlap but do not by themselves publish for this wire) are dispatcher §12 with §16 question 3. Section 6.2 assumes the dispatcher can evaluate them; their publication, codes included, is a precondition of enabling `host_start`, and none of it is filled in here. |
| OPEN S6 | Clearance after a journal fence | R23 opens an absence debt and station/1 §11 makes a slot unusable after journal loss until a clearance record citing R09 exists. The writer of that clearance is already settled at the pin — station/1 §11 makes it the dispatcher's record that cites the R09 of that account — and is not reopened here. What remains open is narrow: the encoding of the clearance record and of the non-resurrection floor it rests on (the per-session pre-loss `op_id` inventory and `state_version` floor of sections 6.2 and 7, kept outside the restored journal in the station record under S1 or the operator's clearance inventory, against which every post-clearance `host_start` with no surviving record must be proved new at section 6 step 3, failing which it refuses `station_session_journal_fenced`, effect: none); the encoding and location, outside the restored journal, of the generation floor that the new `installation_generation` installed after clearance must exceed — every generation ever accepted at either side (section 7), which neither a restored journal predating the last installed generation nor its load checks can prove; how clearance and the operator disposition that states a pre-loss start dead reach the Host so it may mint a new operation under OPEN H6; and the confirmation that registration resumes only under that new generation, beside dispatcher §11.1's restore formats. |
| OPEN S7 | Signatures beside peer pins | The decided v1 form authenticates host operations by kernel peer uid and pins alone and signs nothing (section 3). The objection worth recording is here: a same-uid process is indistinguishable from the Host service, and dispatcher §3.3 signs every other mutating request. Whether joint publication requires a request signature by the Host principal in addition to the pins, or accepts the pin-only form for a statically installed same-machine service pair, is for the station, dispatcher and Host owners to answer at acceptance. v1 is written without signatures either way. |
