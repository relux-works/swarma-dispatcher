# demo cut, not production, not a conforming dispatcher

This removable Linux-only experiment adds `swarma-demo-dispatcher` and
`swarma-demo-client`. It does not enable launch in `swarma-dispatcher` or
`swarma-capacity`. No account provisioning, launcher/helper implementation,
installer, sudoers change, grant, capacity reservation or journal is supplied.
Source and fake-child tests do not qualify a real-account deployment. Remove
this binary after the demo; production needs the journal and full authority
contracts, not a promotion of this cut.

## Deliberate departures

| Contract | Departure |
|---|---|
| `spec/dispatcher.md` §4.2 immutable input | One marker only; no `input/1` |
| §4.5 effect IDs and journal; `spec/journal.md` | No journal; no replay or reconciliation |
| `spec/m0-launch.md` committed-start A | Not implemented; marker is a stand-in, not a claim of A |
| §§3.1–3.4 grants, M0 protected policy | None; the caller is a pinned peer UID, which is not authority |
| Capacity reservation | None; sequential by construction |
| R-WATCH, NETBIND, R-TRANSFER | Excluded; workspace preparation belongs to the operator |

## Protected deployment inputs

The only daemon arguments are `--config /absolute/server.json`. There are no
path, command, environment or identity overrides. The separately provisioned
server file is root-owned, single-link, regular, `0440`, effective service GID.
The client file is root:root `0444`. Every ancestor is pinned without symlinks
and is not group/other writable. Snapshot device/inode, size and attributes are
rechecked. Neither process repairs configuration or directories.

Both config objects require exactly `v` (1), `service_uid`, `service_gid`,
`connect_gid`, `endpoint_dev`, `endpoint_ino` and `caller`. `caller` has exactly
`uid,dev,ino,dir`; its directory is UID-owned `0700` and matches the device/inode.
UIDs and group rules match the [capacity transport](transport.md)'s unmanaged
service/operator roles: nonroot, outside 30000..59999, different service/caller
UIDs; the connection group differs from the private service group. Service
real/effective IDs and client real/effective UID must agree with their snapshot.
The client must not belong to the private service group. Both belong to the
connection group. Non-reuse of operator/service UIDs remains a deployment duty.

Server config additionally requires `service_account` (account name), `executor`
(approved ID, not a path), `receipt_root` (absolute) and `state_dir` (absolute).
Server/client byte caps are 16384/4096. JSON objects have exact case-sensitive
members, no duplicate keys, no null fields, depth at most eight and no trailing
document. Ordinary JSON whitespace/key ordering is accepted in this demo.

The fixed `/etc/swarma-demo/roster.json`, root:root `0644`, has exactly
`{"accounts":[{"name":"demo-01","uid":30000,"gid":30000,"generation":"0123456789abcdef0123456789abcdef","owner":"demo-service"}]}`.
This is an inert shape, not an enrollment. There are 1..20 unique names, UIDs and
generations; UID is 30000..30019 inclusive, GID equals UID, generation is 32
lowercase hex, and owner equals protected `service_account`. Any mismatch refuses
startup. Accounts are precreated; this code never creates or looks them up.

`state_dir` already exists, service-owned `0700`. Its protected ancestors and
leaf are checked without repair. The fixed endpoint directory
`/run/swarma-demo-dispatcher` is service-owned `0711`, service GID, with the
configured device/inode. Its socket is `dispatcher.sock`, service-owned,
connection GID, `0660`. A lifetime flock, refusal of existing endpoints and
identity-scoped socket cleanup follow the copied transport mechanism. The lock
is outside the state directory. Stale sockets require manual reconciliation;
there is no automatic takeover. State and receipt storage must be local and
trusted; userspace deadlines cannot bound a hung kernel/filesystem operation.

## Wire and commands

`internal/frame` bounds apply: four-byte big-endian length, 1..131072 bytes.
Every socket receive rejects ancillary data and closes delivered rights.
Before reading even a prefix, the daemon checks `SO_PEERCRED` and the operator
pin. The client checks the service peer, its own pin and endpoint before writing.
Pins are rechecked before results. There are at most 64 global connections and
8 for the one operator, with no pending worker queue.

One hello, one operation, one reply, then close; the client never retries:

```json
{"protocol":"swarma-demo-dispatcher/1"}
```

Both sides send that exact hello. A production hello cannot negotiate it.
Operation forms (objects below are separate examples):

```json
{"op":"demo.launch","request_id":"r1","account":"demo-01","plan":"AAE="}
{"op":"demo.stop","request_id":"s1","account":"demo-01","grace_seconds":10}
{"op":"demo.status","account":"demo-01"}
```

IDs are 1..64 ASCII `[A-Za-z0-9._:-]` bytes; account names match
`[a-z][a-z0-9-]{0,31}`. Launch requires standard canonical base64, encoded at most
64 KiB, decoded at most 48 KiB, including an empty plan. Decoded bytes are never
parsed. Stop grace is optional, defaults to 10, integer 1..30. No caller-selected
UID, path, argv, environment or grant is accepted. Unknown operations, including
`capacity`, return `operation_unknown`; malformed shapes return `request_invalid`.

Client syntax is `swarma-demo-client --config /absolute/client.json launch|stop|status`
with the corresponding complete operation object redirected on stdin. Intake is
at most one frame's payload, with a five-second deadline for pipes. An ordinary
redirected regular file is read with the byte bound on the trusted local filesystem.
The client prints one JSON reply. Local/protocol errors exit 1; a received reply
exits 0 even for refusal/unknown/unresolved. Inspect `state`, never infer absence
from the CLI exit status. Unsupported platforms fail truthfully.

Connection intake/write stages have two-second deadlines under a 55-second
session ceiling. The client allows the operation reply the remaining session
budget; launch receipt observation is at most 10 seconds, whole stop at most 45
seconds. Partial progress never resets a stage deadline.

## Child boundary and observations

The only child argv are `/usr/bin/sudo -n` followed by exactly one of
`/usr/local/libexec/swarma/swarma-user-launcher` and
`/usr/local/libexec/swarma/swarma-user-manager`. There are no further arguments,
no shell, an empty environment and cwd `/`. Sudo establishes its own caller
metadata. Stdin receives exactly one JSON document, then closes, within five
seconds and the operation deadline.

Launcher stdin has exactly `v,op,request_id,generation,execution_id,epoch,executor,plan`,
with `v=1`, `op="launch.start"`, `epoch=1`, at most 80 KiB. The dispatcher generates
`EX-` plus 32 random lowercase hex characters before writing the attempt marker.
Helper stdin has exactly `v,op,request_id,args`, with `v=1`, `op="exec.stop"`, args
exactly `generation,execution_id,grace_seconds`, at most 16 KiB.

Launch stdout/stderr each go to a separately created `0600` log,
`output-<generation>-stdout.log` / `-stderr.log`, capped at 1 MiB. Reading continues
past the cap. First excess creates the corresponding `.truncated` record with
`true`. Logs never reopen for writing. Pipes use bounded polling waits; child
exit is reaped using nonblocking wait. There is at most one drainer pair per
occupied child slot, bounded by the 20-account roster. A new generation also
waits for the prior sudo handle and pipes to finish. No code signals a child or
account process. Shutdown closes pipes and releases live handles; executor
survival is not promised, and orphan reaping belongs to the service supervisor.

The receipt path is `<receipt_root>/<generation>/<execution_id>.json`, checked
without following any links, root:root regular single-link `0644`, at most 4096
bytes. Exact members are `execution_id,epoch,pid,start_time,boot_id,session_id`.
Execution ID must match; epoch is 1; PID/session ID and Linux start ticks are
positive unsigned integers; boot ID is a lowercase UUID. Generation is bound by
the protected directory, not an extra receipt member.

Launch replies are `{"state":"receipt-seen","receipt":{...},"closed":false}`
or `{"state":"unknown","code":"...","closed":true}`. A receipt does not prove
exec, readiness or absence. There is no `started` response.

Helper success is the existing nested shape:

```json
{"request_id":"s1","ok":true,"result":{"execution_id":"EX-0123456789abcdef0123456789abcdef","stopped_at":"2026-01-01T00:00:00Z","receipt":{"signalled":1,"killed":0,"remaining":0}},"state":"active"}
```

Failure is exactly `request_id,ok:false,error:{code,message}`. Stop verifies the
request and execution IDs, exact fields, timestamp and unsigned counters. Only
`ok:true` with `remaining:0` and a zero child exit produces `state:"absent"`;
everything else produces `state:"unresolved"`, preserving the helper error code
when valid. Reply `timing` is `timely`, `missed` or `unknown`, independently of
absence. Helper stdout/stderr retention is capped at 16 KiB each; excess stdout
is an invalid result. Exit zero without a document is never absence.

## State and failure behavior

Creates use `O_CREAT|O_EXCL|O_WRONLY|O_NOFOLLOW|O_CLOEXEC`, `0600`, then file and
directory fsync. No state file is deleted, renamed or truncated. An attempt
contains `request_digest` (SHA-256 of the operation bytes), `execution_id`, and
UTC `time`, persisted before any child. All evidence times are UTC; live
operation durations use monotonic clocks.

Launch requires `closed` absent, all previous attempts stopped and no launch or
stop in flight. The same generation always refuses a second launch. Stop can
operate on an unresolved attempt while admission is closed. During a launch
call it returns `launch_in_flight`, so it cannot overtake a not-yet-issued child.

`stop-<generation>` is an immutable intent containing the request ID, execution
ID, grace and UTC time. This minimal additional record prevents changed-argument
retries after a daemon restart; it is not a journal or a recovery engine. In one
process, retries retain the first monotonic deadline. A different ID or grace
returns `stop_conflict`. At most one helper per generation may be outstanding.
Successful terminal responses replay in memory without another helper. After
restart the monotonic deadline is unavailable: stop returns
`unresolved/stop_clock_unknown`, timing `unknown`, without inventing a budget or
invoking the helper. Status still exposes any preserved stopped fact. Manual
operator reconciliation is required for this deliberate conservative limit.

| Event | Response / code | Preserved effects |
|---|---|---|
| Bad request / unknown account / unknown operation | `refused`: `request_invalid` / `account_unknown` / `operation_unknown` | No marker or child |
| Existing attempt | `refused/attempt_exists` | Original marker; no child |
| Closed / concurrent / previous unresolved | `refused/admission_closed`, `launch_in_flight`, `attempt_unstopped` | No new child |
| Previous sudo or pipes still active | `refused/child_unreaped` or `output_pending` | Original observation only |
| Marker create/sync uncertain | `refused/marker_uncertain` | Never call sudo; close admission; keep all partial files |
| Log setup / delivery failure | `unknown/log_unavailable` or `launcher_io` | Attempt and closed latch |
| Receipt timeout / mismatch / early exit | `unknown/receipt_timeout`, `receipt_mismatch`, `launcher_exit` | Attempt and closed latch; no signal |
| No attempt to stop | `refused/attempt_missing` | Never invent absence |
| Stop argument conflict / active stop | `refused/stop_conflict` or `stop_in_flight` | Original intent and deadline |
| Stop intent or stopped-record uncertainty | `unresolved/stop_intent_uncertain` or `stopped_uncertain` | Close admission; keep partial files |
| Nonzero exit / malformed result / remaining processes | `unresolved/helper_exit`, `helper_result_invalid`, `processes_remain` | Closed; no stopped record |
| Helper `ok:false` | `unresolved`, helper code | Closed; no stopped record |
| Stop timeout / lost monotonic clock | `unresolved/stop_timeout` or `stop_clock_unknown` | Closed; original intent; missed/unknown timing |
| Verified absence | `absent` | Exclusive `stopped-<generation>` binds execution; late absence leaves admission closed |
| Latch persistence fails | `latch_uncertain` where applicable | In-memory closed; retained attempt forces fail-closed restart |
| Crash/reboot with unstopped attempt | Startup writes `closed` | No recovery or second launch |
| Unknown/corrupt/orphan state entry | Startup refuses | No repair |

`closed` is irreversible. Stop temporarily excludes forward admission before
calling the helper; a failed/unresolved stop persists the latch. A successful
first stop allows the next roster generation after prior child resources finish.
This reads the sequential-admission rule literally; it does not clear a
previously written latch. `demo.status` returns `state:"facts"`, `closed`, and
preserved `attempt`/`stopped` where present, never current process liveness.

## Test evidence boundary

Go tests use non-root fixture paths and an unexported command seam to launch the
actual test child process. They exercise stdin EOF, pipe backpressure, receipt
observations and exit results without root or sudo. They prove dispatcher
behavior only. The production transport regression is an actual internal socket
exchange; it is not CLI deployment qualification. Protected snapshot metadata
controls are synthetic positive attributes paired with actual non-root refusals.
A bounded source checker includes known forbidden mutation plants; it is not an
interprocedural filesystem proof. Hosted Linux execution and independent review
are required before interpreting these test sources as passing evidence.
