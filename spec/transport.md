# Reference transport subset: `dispatch/1`

This proposed Linux-only reference contract is transport negotiation plus the existing `capacity` read, always refused closed.
It implements no launch, auth/signing plan, helper ledger, journal, accounting, recovery controls, durable effects or activation.
All full-dispatcher gates remain; no shell, operation substitution or alternate launch fallback exists.
[Dispatcher §§8.1–8.3, 10 and 11](dispatcher.md#8-operations-results-and-authorisation) remain authoritative subject to their explicit subset clarifications.
The broker grounding of §8.3 is Unix stream framing, kernel connect-time peer credentials, pre-write server verification,
protected endpoints, no descriptor transport, hello versions and a 2 s administrative read deadline. The complete subset wire rules follow;
no unavailable broker grammar is imported implicitly. Framing alone neither authenticates nor enables opaque JSON or unpublished operations.
The opaque codec landed in PR #10 at `dcd4821e388384899aaeef8d91dab9d4311c11f8`; that provenance is not socket-adapter qualification.
In particular, its generic `frame.Read` is not an ancillary gate: socket prefix/body reads must use the descriptor-aware mechanism below.
EOF classification and payload/wire limits below are requirements, not assertions that codec fixes have landed.
Linux-only is an implementation limit; no macOS peer-credential or native qualification claim follows.

## Protected configuration and unmanaged principals

Both programs require exactly one explicit absolute `--config` file path; no environment, NSS, account-name lookup, discovery,
compiled value defaults, endpoint override, partial configuration or reload supplies authority. Load once before listen/connect.
The endpoint is exactly `/opt/swarma/run/dispatcher/dispatcher.sock`; its directory is outside the data tree. No data-tree access is needed.
Open configuration through pinned root-owned ancestor directories not writable by group/others, rejecting every symlink.
The file is regular, root-owned, single-link; server mode is `0440`, group equal to its effective GID;
client mode is `0444`, group root. Server configuration's group is reserved to the service account by deployment, never callers.
Read at most limit+1 bytes, then refuse oversize; verify unchanged device/inode/size with `fstat` and path recheck before accepting the snapshot.
No process changes configuration, directory, ancestor or pre-existing lock ownership/modes into compliance.

Configuration JSON uses canonical ASCII bytes: no whitespace outside strings, escapes, duplicate/unsorted keys, BOM or trailing bytes;
strings contain only `0x20..0x7e` excluding quote/backslash, numbers are shortest unsigned decimal integers (zero only where the field permits it).
Validate bytes before/during a bounded scan, never by normalization. Container depth counts outermost as 1; maximum config depth is 3.
Every key below is required and no others are allowed; `v` is integer `1`. Numeric UID/GID values are `1..4294967294`;
`dev`/`ino` and endpoint pins are `1..9007199254740991`, rejecting filesystems whose identities cannot be represented exactly.
A principal object has exactly `dev`, `dir`, `ino`, `uid`. `dir` is an absolute ASCII path, 1..512 bytes, no empty, `.` or `..`
components, repeated separators or trailing slash; `/` is not a principal directory. Principal UID and owning-directory identity are one pin.

| Source | Closed root members | Maximum bytes |
| --- | --- | ---: |
| Server | `callers`, `connect_gid`, `endpoint_dev`, `endpoint_ino`, `service_gid`, `service_uid`, `v` | 16384 |
| Client | `caller`, `connect_gid`, `endpoint_dev`, `endpoint_ino`, `service_gid`, `service_uid`, `v` | 4096 |

`callers` is a strictly ascending-by-UID array of 1..32 principal objects, at most one per UID; membership grants only the existing capacity-read role.
Each configuration byte cap is an independent limit: individually maximal entries need not fit together and oversize snapshots refuse.
`caller` is one principal object for the invoking client, never the server allowlist. Server-only configuration is not required or disclosed to clients.
The endpoint directory's device/inode are `endpoint_dev`/`endpoint_ino`. Its owner/group must equal `service_uid`/`service_gid`.
Server real/effective UID/GID must equal `service_uid`/`service_gid`; the client's real/effective UID must equal `caller.uid`.
`connect_gid` must be the process's effective or supplementary GID at both ends; the server can assign that group to its own socket.
It must differ from `service_gid`, whose membership is private to the service.
Service UID is nonroot and outside the managed range `30000..59999`; each caller UID is also outside that range, nonroot and distinct from service UID.
Caller membership in the private service GID is forbidden by deployment (the client also checks its own groups); no NSS membership test is implied.
Missing/unreadable/empty/malformed/oversize configuration, duplicates, unknown members, invalid ranges/pins, wrong owner/mode/group or disagreement
with runtime identity refuses startup/connect with local failure and zero protocol bytes; server/client configuration is never repaired or merged.
Peer UID and endpoint checks also enforce disagreement between separately provisioned snapshots; there is no agreement negotiation or downgrade.

Only unmanaged principals are admitted by the reference subset. Managed `(uid, generation)` identities require the helper's current active ledger and are otherwise excluded, with the sole named exception **Managed M0 callers** below.
For each pin, open the directory `O_PATH|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC` through nonsymlink ancestors owned by root or that pin's UID,
not group/other-writable. `fstat` must show the configured device/inode, owner UID and exact mode `0700`.
Server validates every caller pin at configuration load, the selected pin before hello and again before the operation result;
client validates its own pin at load and before connecting. Missing/unreadable/replaced pins refuse; no empty/new directory is accepted as enrollment.
Keep directory descriptors pinned for the exchange and recheck the path still names that identity at each validation point.
Root-controlled enrollment must not recycle a configured unmanaged or service UID: stop/drain the daemon and callers, withdraw old configurations,
remove the old enrollment and replace its directory identity before reusing a UID; re-enrollment needs fresh root-provisioned pins at both ends.
Directory checks prevent a replacement from inheriting an old pin. They cannot detect root reassigning a UID while deliberately retaining its old
pinned directory/configuration; non-reuse is a deployment invariant, not a fabricated ledger check. If it is not assured, do not deploy this subset.
JSON UIDs, names, PID claims and group membership never establish the protocol principal. Same-UID processes remain the same principal.

## Managed M0 callers

The separate protected file selected by `--m0-config`, not `server.json`, holds
`callers`: 1..4 closed `dev`, `dir`, `ino`, `uid` principal objects, strictly
ascending by UID in `30000..59999`, none the service UID. Its canonical closed
configuration is `callers`, `enabled`, `entry`, `kind`, `recovery_attested`,
`state_dir`, `supported_local_storage`, `v`, with `kind` equal to
`swarma-dispatcher m0-config/2` and `v=2`; configuration /1 now refuses rather
than acquiring new meaning. Missing, empty, oversized, unordered, duplicate,
out-of-range or malformed caller lists refuse attach with a named `callers`
refusal. The configuration byte cap remains 4096, depth is at most 3, and the
bounded canonical parser permits at most 256 tokens. The transport snapshots
extension-supplied principals before listening, asserts no overlap with server
callers and validates every managed pin using the same `principalPin` checks;
a failed pin refuses startup. Server configuration retains its seven members
and unmanaged-only caller list.

For a managed kernel peer: (1) with no extension attached, or (2) absent from
its protected list, admission is exactly the base zero-byte close, releasing
the global slot without reserving a UID slot or reading hello; (3) when listed,
the same pin checks, hello, per-UID limit of eight and global limit of 64 apply.
After hello, only frames with kind `swarma-dispatcher m0-request/1` go to the M0
extension; capacity and every other reference-subset operation close without an
operation result. The pin is rechecked before hello and the M0 result. An
unmanaged server-listed caller retains capacity behavior and may still reach
the extension with an M0 frame, where M0 refuses a non-association kernel UID.
The current active generation check remains in M0 (`generationCurrent`), never
in allowlist membership. There are at most 32 unmanaged plus four managed pins.
The gated [host-registration/1](host-registration.md) seam of the full
dispatcher defines one further Host pin when its `--host-config` is attached
(at most those 36 pins plus that one); that seam is not part of this reference
subset, which admits no host operation, and with no host configuration
attached the bound above is unchanged.
No new client grammar, flags, authority, enrollment, policy or generation
fallback is introduced; default seams still keep M0 closed and nothing is
activated by this exception. The reference-subset operation/result grammar and
response-size examples below remain reference-only; M0 validates its own bodies
and results behind the same framing, descriptor and deadline gates.

The installation digest of the daemon's protected configuration is defined over
both files as `SHA-256(CCJ-1({"m0":<sha256 hex of the M0 config bytes>,"server":<sha256 hex of server.json bytes>}))`,
with exactly those two member names; the separate daemon configuration unit
implements it, not this transport unit.

## Endpoint ownership, startup and shutdown

The endpoint directory must already exist, service-owned with `service_gid`, exact mode `0711`, and match the configuration pin;
ancestors are root/service-owned and not group/other-writable, without symlinks. Pin them; refuse any substituted component.
Acquire a separate lifetime singleton lock before inspecting the endpoint. The file is exactly `lifetime.lock` in that directory,
opened `O_RDWR|O_CREAT|O_NOFOLLOW|O_CLOEXEC`, creation mode `0600`; a well-formed existing file is reused, never unlinked or repaired.
`fstat` and path recheck must show the same regular file, service owner and group, exact `0600`, one link.
Use BSD `flock(LOCK_EX|LOCK_NB)` on that open file description, never POSIX byte-range locks; contention refuses startup.
The daemon retains this descriptor through all cleanup and releases it only at exit; future state/receiver locks are distinct.
Startup then refuses any existing endpoint: stale/live socket, regular file, hard-link alias or symlink. Never probe-and-steal, unlink or adopt one.
Bind a newly created nonblocking `SOCK_STREAM|SOCK_CLOEXEC` listener through the pinned directory with initial permissions exactly `0600` (startup umask `0177`, before concurrent work).
Record the created path's device/inode immediately; set its GID to `connect_gid` first and its mode to `0660` second, then verify owner/GID/mode/socket type
and unchanged device/inode via the pinned directory before listen/accept. Wrong group/permission or path changes refuse, never repair a foreign path.
If any post-bind startup step fails, close the listener and unlink only that recorded unchanged socket under the lifetime lock, then exit failure.
If identity cannot be proved or cleanup fails, leave it untouched and report failure; do not claim successful cleanup or restartability.

Client setup pins the directory and endpoint's device/inode, owner `service_uid`, GID `connect_gid`, mode `0660` and socket type before connect;
connect to that pinned directory's endpoint, then re-stat directory and socket identity after connect, before any write.
Both ends obtain Linux `SO_PEERCRED` from the connected socket. Client requires the configured service UID before sending even hello;
server requires a configured caller UID and its current directory pin before reading hello or reflecting any ID. Unavailable credentials close.
Directory pinning, pre/post identity checks and kernel UID checks all apply; none is replaced by successful DAC connect.

SIGTERM or SIGINT enters shutdown once, whether starting or serving; repeated SIGTERM/SIGINT neither restart nor shorten it.
Other signals are not graceful requests. Internal fatal startup/accept/ownership failures take the same owned-cleanup path and exit failure.
Normal SIGTERM/SIGINT exits success only if cleanup succeeds. Stop accepts, cancel workers and force active sockets' deadlines to now;
use nonblocking poll-integrated reads, and `shutdown(SHUT_RDWR)` before close where needed to wake receivers. Never rely on raw blocking close alone.
Within a 1 s monotonic shutdown budget release connection slots/buffers and perform identity-checked cleanup of only the recorded owned socket,
then release the lifetime lock. Do not retry cleanup past that budget; local failure leaves residuals for operator inspection.
If startup never created an endpoint, skip unlink and release only resources/ownership actually acquired.
This is a userspace scheduling/I/O budget, not proof of a bound on a stuck kernel or filesystem call. Native timing qualification remains required.
A missing/replaced endpoint is left untouched and reported as an ownership/cleanup failure. No unrelated process or directory is stopped/deleted.

Unclean death (SIGKILL, crash, power loss) may leave the socket: only kernel lock release is relied on. This accepted subset limitation forbids
supervisor automatic restart/restart loops or automatic stale removal. It is not §11 journal recovery or the full recovery-control service.
Operator cleanup is a filesystem procedure, not a dispatcher operation: with the validated directory pin and stable verified lock file, acquire
the same nonblocking exclusive flock; lock contention means refuse. Require independently recorded service-created socket device/inode
and matching directory identity, from startup evidence or explicit operator inspection. Require owner `service_uid`, socket type and recorded device/inode;
the only allowed GID/mode states are pre-chgrp (`service_gid`, no mode bits outside `0600`), post-chgrp/pre-chmod (`connect_gid`, no mode bits outside `0600`),
or published (`connect_gid`, exact `0660`). Any other owner/GID/mode/type refuses. Recheck these attributes and identity immediately
before unlinking only that endpoint under the lock. A nonblocking connect probe with a non-resetting 2 s monotonic deadline under that lock must return `ECONNREFUSED`;
successful connect means live (close without sending), and any other error is uncertainty: refuse cleanup. Recheck identity after the probe and before unlink.
If identity is unknown/different, or the endpoint is live, refuse this procedure; no foreign deletion.
An unheld cooperative lock alone is not proof that an arbitrary socket is ours. Never unlink the lock file, stop another process or replace a live socket.
Release the lock after cleanup; then a separately started daemon may bind fresh. No operator cleanup capability is implemented by this wire subset.

## Framing and per-receive descriptor ownership

A frame has a separate 4-byte unsigned big-endian payload length followed by exactly that many payload bytes: length `1..131072` inclusive.
The maximum wire frame is `131076` bytes; the cap excludes the prefix. Reject zero/131073 before allocating the payload buffer.
EOF with zero prefix bytes is clean connection EOF (but client failure if a response was expected); EOF after 1..3 prefix bytes is truncated prefix.
EOF after a valid prefix but before all payload bytes, including zero payload bytes, is truncated payload, never clean EOF or complete frame.
Any truncated frame, read/write error or timeout closes, with no result for that incomplete message and no resynchronization.
Partial writes must complete within the original write deadline or fail; no new frame is started after a failed write.

Every receive on either side, including the first prefix byte, uses nonblocking `recvmsg` with `MSG_CMSG_CLOEXEC` and a 4096-byte ancillary buffer.
The receive buffer is owned/reset for that call; inspect only returned control bytes and flags, never controls carried over from a previous call.
Receives request at most the bytes remaining in the current prefix or payload, with no read-ahead. Inspect/dispose ancillary data before JSON,
EOF or receive-error handling; every delivered SCM_RIGHTS descriptor belongs to this receive until closed, including on error/truncation paths.
Any control data, `MSG_CTRUNC`, `MSG_TRUNC` or flags other than a returned `MSG_CMSG_CLOEXEC` bit refuses:
close every delivered right, discard payload, close connection. Zero flags without controls is the ordinary data path.
Never use, forward or retain rights. Correct parsing/disposal of complete returned controls and kernel disposal of truncated undelivered rights
must be qualified on isolated Linux, including receive errors. A normalizing payload decoder or generic codec read cannot bypass this gate.
Neither end enables ancillary-generating options such as SO_PASSCRED or timestamp reception; SO_PEERCRED is queried separately.
All product-created sockets, directory/config/lock descriptors are atomically close-on-exec. No socket activation, passed listener or child execution.
Deployment starts the process with only standard input/output/error at exec; those are never IPC transport. No descriptor-number enumeration/closure
attempts to repair deployment. Go runtime-owned descriptors are legitimate and must remain open; product IPC uses only its own newly created sockets.
Honest endpoints never pass/inherit an established connection. This prohibition is not server-detectable: a connector can hand its socket elsewhere
and SO_PEERCRED still names the connector. Authority attaches to that connection capability; this subset does not claim handoff detection.

## Closed wire JSON and state machines

Wire bodies are exact UTF-8 CCJ-1 canonical bytes in this ASCII-only subset. A byte-level bounded scan before/during recognition is mandatory;
parse-normalize-then-check is insufficient. Allowed string content is `0x20..0x7e` except quote/backslash; quotes only delimit strings.
No escapes, BOM, other Unicode, whitespace outside strings, duplicate/nonascending keys, trailing bytes, nulls or noncanonical spellings are accepted.
Objects have unique lexically ascending ASCII keys, arrays preserve order, booleans are exact `true`/`false`, the only numeric token is `1`.
Outer container depth is 1, nested container depth 2; every allowed shape below fits 2. Anything deeper or an incomplete/extra-member shape closes.
`request_id` is a connection correlation string of 1..64 bytes matching `[A-Za-z0-9._:-]`; client-generated IDs are 32 random lowercase hex characters
(the intersection with M0). This transport grammar does not widen M0's signed request IDs or publish future launch IDs.
Hello and operation use the same ID only for this connection: neither hello nor this read/refusal is journaled, retained or a replay identity.
A repeated capacity read is recomputed as the same hold response, never a stored-record or durable-effect claim.

The following are the five valid exchange/result templates; substitute only the validated client ID for `r1`:

1. Client hello: `{"request_id":"r1","versions":[1]}`.
2. Server hello: `{"ok":true,"request_id":"r1","result":{"version":1},"v":1}`.
3. Client capacity: `{"op":"capacity","request_id":"r1"}`.
4. Server capacity: `{"error":{"code":"capacity:hold","message":"admission closed"},"ok":false,"request_id":"r1","v":1}`.
5. Server unsupported-request refusal: `{"error":{"code":"request_invalid","message":"unsupported request"},"ok":false,"request_id":"r1","v":1}`.

There is no successful capacity payload or complementary availability branch: this entire subset has no accounting and **always** returns shape 4.
Hello shape 2 reuses the result envelope for connection negotiation, not a `dispatch-op/1` operation or §8.2 record result; it asserts version only.
Only `[1]` negotiates. Unknown/empty/mixed/duplicate versions, extra hello members or any other first frame close without a reply.
In the post-hello stage only, a canonical object with exactly the two string members `op` and matching valid `request_id`, and `op` other than `capacity`,
receives shape 5. It is a rejection recognizer, not an enabled operation grammar. Other invalid requests close with no result.
No other error/result, optional `stage`, UID/auth/plan/member or forward operation is accepted or reflected. Revalidate the caller pin before either result.

Server states: AUTHENTICATE → READ-HELLO → WRITE-HELLO → READ-OP → WRITE-HOLD or WRITE-INVALID → CLOSED.
Any stage failure closes immediately, no reply except shapes 2/4/5 at their specified stages. Operation-before-hello closes; a second hello or wrong-ID
operation at READ-OP closes. After one operation result close without reading a second operation/hello/trailing frame.
Client states: VERIFY-CONNECT → WRITE-HELLO → READ-HELLO → WRITE-CAPACITY → READ-HOLD → CLOSED.
Client accepts exactly shape 2 with its ID at READ-HELLO (even a valid error envelope there fails), and exactly shape 4 with its ID at READ-HOLD;
shape 5 is only for invalid external clients, never a valid capacity result. Any malformed/wrong-ID/wrong-stage response fails without fallback.
Client MUST NOT pipeline or write the operation before validating hello. Server cannot distinguish pipelining from prompt sequential arrival and claims no detection.
One connection, one hello and at most one operation: no pooling, multiplexing or further reads. Reference client makes one attempt and no automatic retry;
ambiguous failure is local failure without effect evidence. A separately invoked read may use the same correlation ID; never substitute an operation/signature.

## Resource and deadline limits

These finite choices are transport resources, never launch/quota authority or a standing-runtime policy.
Use listen backlog parameter 16. Accept loop has at most one transient newly accepted descriptor beyond the 64 active global slots;
try a global slot first (full: close without peer lookup/read), then SO_PEERCRED/allowlist/pin validation, then a per-UID slot (8 maximum).
Only after both slots start connection work/goroutines or allocate frame buffers. Admission checks stay in the bounded accept path;
configuration has at most 32 unmanaged pins plus four managed M0 pins when attached. Failure closes and releases acquired slots; no pending worker queue. Persistent accept errors initiate shutdown.
Each connection has one payload buffer at most 131072 bytes, a separate 4-byte prefix, a 4096-byte ancillary buffer and a 512-byte outbound buffer
including its prefix. Maximum specified response payload is 165 bytes (64-byte ID), wire 169. Release hello payload before reading operation.
These explicit buffers total 135684 bytes/connection, 8683776 for 64; stacks, bounded records/config and kernel overhead are separate, not a total RSS promise.
Set SO_RCVBUF and SO_SNDBUF request to 65536 on listener, client and every accepted socket before use; verify on each. Refuse setup unless effective reported values are
positive and at most 131072 each (Linux doubling is included). No force-buffer options or kernel tuning. Kernel metadata/backlog are outside userspace
buffer arithmetic; backlog parameter and reported socket-buffer limits are transport settings to qualify, not proof of total kernel-memory usage.

All time is monotonic. Server session starts when accept returns; client session starts when connect is initiated (after local validation).
Every connect/read/write stage deadline is `min(stage_start + 2 s, session_start + 5 s)`; do not overwrite the session cap with a later stage deadline.
A read stage covers the entire prefix plus payload; a write covers its entire frame. Partial progress, EINTR or EAGAIN never resets either clock.
Use nonblocking sockets and readiness waits integrated with the runtime poller or equivalent cancellation/deadline-aware polling;
raw blocking recvmsg is forbidden. Deadline/shutdown cancellation must wake a waiting read/write; close alone is not the wakeup mechanism.
EOF/timeouts/slot exhaustion are not protocol error envelopes. Release all descriptors/slots/buffers on every close path.

## Adjacent qualification cases — requirements, not executed evidence

| Surface | Positive control | Required forbidden outcome/control |
| --- | --- | --- |
| Config | Both separate complete protected snapshots; 1/32 caller entries; exact size caps | Empty/33 entries, duplicates, extra/missing keys, max+1 bytes, wrong owner/group/mode, symlink, partial read/replacement, impossible numeric/path/depth values: refuse before serving/writing |
| Principal | Enrolled unmanaged UID and original directory; recheck before hello/result | Root/service/managed UID outside the named M0 exception, forged JSON UID, replaced/missing/wrong-owned pin, stale enrollment after UID reuse: no result; non-reuse deployment invariant independently checked |
| Service | Correct pinned endpoint and service UID | Wrong/unavailable credentials or identity/GID/mode change around connect: zero client protocol bytes |
| Hello/state | Exact ordered hello/capacity/hold | Unknown/empty/mixed/duplicate version; op-before-hello, second hello, wrong ID, error at client hello stage: close; no second operation processed |
| JSON/IDs | Canonical literals, ID lengths 1/64, generated 32-hex IDs | Empty/65-byte ID, duplicate/escaped-alias key, whitespace/order/Unicode/BOM/null/number/extra member/depth>2/trailing byte: close for intended invalid input |
| Capacity/rejection | Every valid read always exact hold; post-hello unsupported two-string object gets exact request_invalid | No initialized-empty success branch, counters, alternate operation, stored replay identity, launch/auth grammar or effects |
| Length/EOF | Lengths 1/131072 pass framing gate; complete payload then EOF is distinct | 0/131073 rejected before body allocation; EOF prefix lengths 1..3 and payload zero/partial after valid prefix are truncation, never clean EOF |
| Slots/buffers | 64th global, 8th UID; multiple UIDs within totals; 131076-byte wire framing | 65th global/9th UID closes before worker/body allocation, all partial-failure slots released; bounded reported socket buffers and no read-ahead |
| Deadlines | Whole frames fit stages/session; EINTR/EAGAIN within remaining time | Partial prefix/body/slow drip/stalled write expire; individually fast stages exceeding total 5 s expire; no automatic client reconnect |
| Endpoint/lock | Fresh protected socket; valid existing lock reused; clean normal restart | Symlink/alias/wrong mode/group, lock contention, stale/live endpoint, bind/chgrp/chmod failure/replacement: refuse without foreign unlink; bad preexisting lock/parent not repaired |
| Crash/operator | Recorded exact residual identity in each of the three allowed GID/mode states removed under acquired verified lock and refused-connect probe, including bound-never-listening residuals | Held lock/live/foreign/unknown/replaced endpoint or other GID/mode: cleanup refuses; SIGKILL residual cannot auto-restart; no lock-file deletion |
| Startup/shutdown | Post-bind failure cleans only own endpoint; SIGINT/SIGTERM once | Repeated signals idempotent; reader/writer wake and release within userspace budget; replacement untouched; cleanup failure reported, no false success |
| Ancillary | Plain data, fresh per-receive controls | Rights on prefix/body, multiple rights, truncation, controls with EOF/error: close all delivered rights before discard; no leaks/carry-over or kernel-generating options |
| Deployment/residual | Only std FDs at exec; runtime poller FDs retained; newly created listener | Inherited listener never adopted; no FD-number sweep; out-of-band connection handoff cannot be detected and is not claimed prevented |

All socket, filesystem-race, descriptor-disposal and native timing cases remain UNRUN until isolated Linux implementation qualification.
