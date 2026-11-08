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
| Demo launch call contract §6, evidence loss | An intact `.lost` records loss without closing admission. A torn loss record closes admission at startup. An unpersisted loss closes admission immediately in memory and attempts a durable latch, retried on requests. |
| Demo launch call contract §6, lost reply | A lost stop reply does not add a latch; see the stop-reply behavior under [Torn records and restart continuation](#torn-records-and-restart-continuation). |
| Demo launch call contract §6 and demo acceptance AC14, crash/reboot | If every attempt has a durable stopped record and no latch exists, restart permits the next generation. The demo does not latch every crash. |
| Deployment qualification | TestEntrypointBoundaryWiring is an identifier gate only. The positive Daemon path and token-preserving edits to the runtime check result, startup caller pin result and client runtime check require a disposable VM run. Endpoint device/inode and the protected roster parse result have separate non-root behavioral witnesses. |
| Capacity reservation | None; sequential by construction |
| R-WATCH, NETBIND, R-TRANSFER | Excluded; workspace preparation belongs to the operator |

## Protected deployment inputs

Before opening configuration, state or sockets, the daemon walks `/proc/self/fd`
and applies `FD_CLOEXEC` to every descriptor above 2, except its already
close-on-exec directory handle. This seals descriptors inherited from its
supervisor without closing them; stdio and runtime descriptors remain usable.
An open/read failure on `/proc/self/fd` refuses startup. New daemon descriptors
are also opened close-on-exec. The client does not perform this process-wide
sealing.

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
(`swarma-demo-executor` or `swarma-demo-codex-executor`, not a path),
`receipt_root` (absolute) and `state_dir` (absolute).
Server/client byte caps are 16384/4096. JSON objects have exact case-sensitive
members, no duplicate keys, no null fields, depth at most eight and no trailing
document. Ordinary JSON whitespace/key ordering is accepted in this demo.

The fixed `/etc/swarma-demo/roster.json`, root:root `0644`, is the helper's
`swarma-user-manager demo-roster/1` document, at most 48 KiB. Every object is
closed. Top-level members are exactly `format,source_revision,boot_id,operator,
dispatcher,executor,runtime,accounts`. The dispatcher follows the helper schema
with two differences: `source_revision` is any 40 lowercase hex characters,
without pinning a helper revision; `operator` is `{name,uid,gid}` with an account
name and integer IDs, binding `uid` to protected `caller.uid` instead of a
compiled operator identity. `operator.gid` is any JSON integer and is unused by
the dispatcher.

`boot_id` is a lowercase UUID. `dispatcher` has exactly
`name,uid,gid,principal,generation`: name and principal are
`swarma-demo-dispatcher`, UID and GID are 1101. Protected `service_account` and
`service_uid` must match principal and UID. `executor` has exactly `id,path,sha256`:
either frozen executor ID above, its corresponding
`/usr/local/libexec/swarma/<id>` path, and a 64-lowercase-hex digest. Its ID must
equal protected `executor`. `runtime` has exactly four booleans: `admitted`,
`inventory_qualified`, `creation_exclusion_qualified`, `interpreter_qualified`.
These are shape-checked only; neither boot ID nor runtime booleans establish
qualification in this dispatcher. Before starting the daemon, confirm that the
helper accepts the roster on the current boot; otherwise the first launch
spends that account's generation and latches admission.

There are exactly three account rows, named `swarma-demo-01`, `swarma-demo-02`,
`swarma-demo-03`, each once. Each has exactly `name,uid,gid,generation,
owning_dispatcher,home,workspace,copy_digest`. UIDs are unique in 30000..30019,
GID equals UID, owner equals the dispatcher principal, home is
`/srv/swarma-demo/home/<name>`, workspace is `/srv/swarma-demo/work/<name>`, and
copy digest is 64 lowercase hex. All generations, including the dispatcher's,
are unique 32 lowercase hex characters and never all zero. Dispatcher name is
neither an account nor operator name; dispatcher UID differs from operator UID;
neither dispatcher UID/GID nor operator UID is in 30000..30019. Frozen names,
IDs and paths have no configuration override. Any mismatch returns
`config_invalid` before startup effects. Only account name, UID, GID and
generation are retained. Accounts are precreated; this code never creates or
looks them up.

An inert complete example (placeholder digests and generations, no enrollment;
the helper refuses this example on purpose):

```json
{
  "format": "swarma-user-manager demo-roster/1",
  "source_revision": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "boot_id": "12345678-1234-1234-1234-123456789abc",
  "operator": {"name":"operator","uid":1000,"gid":1000},
  "dispatcher": {"name":"swarma-demo-dispatcher","uid":1101,"gid":1101,"principal":"swarma-demo-dispatcher","generation":"dddddddddddddddddddddddddddddddd"},
  "executor": {"id":"swarma-demo-executor","path":"/usr/local/libexec/swarma/swarma-demo-executor","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
  "runtime": {"admitted":false,"inventory_qualified":false,"creation_exclusion_qualified":false,"interpreter_qualified":false},
  "accounts": [
    {"name":"swarma-demo-01","uid":30000,"gid":30000,"generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","owning_dispatcher":"swarma-demo-dispatcher","home":"/srv/swarma-demo/home/swarma-demo-01","workspace":"/srv/swarma-demo/work/swarma-demo-01","copy_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
    {"name":"swarma-demo-02","uid":30001,"gid":30001,"generation":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","owning_dispatcher":"swarma-demo-dispatcher","home":"/srv/swarma-demo/home/swarma-demo-02","workspace":"/srv/swarma-demo/work/swarma-demo-02","copy_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
    {"name":"swarma-demo-03","uid":30002,"gid":30002,"generation":"cccccccccccccccccccccccccccccccc","owning_dispatcher":"swarma-demo-dispatcher","home":"/srv/swarma-demo/home/swarma-demo-03","workspace":"/srv/swarma-demo/work/swarma-demo-03","copy_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
  ]
}
```

`state_dir` already exists, service-owned `0700`. Its protected ancestors and
leaf are checked without repair. The fixed endpoint directory
`/run/swarma-demo-dispatcher` is service-owned `0711`, service GID, with the
configured device/inode. Its socket is `dispatcher.sock`, service-owned,
connection GID, `0660`. A lifetime flock, refusal of existing endpoints and
identity-scoped socket cleanup follow the copied transport mechanism. The lock
is outside the state directory. Stale sockets require manual reconciliation;
there is no automatic takeover. After SIGKILL, the stale socket must be removed by the operator before restart; this program does not remove it on startup. State and receipt storage must be local and
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

Request IDs match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, including in persisted
attempt and stop intent records; account names match
`[a-z][a-z0-9-]{0,31}`. Launch requires standard canonical base64, encoded at most
64 KiB, decoded at most 48 KiB, including an empty plan. Decoded bytes are never
parsed. Stop grace is optional, defaults to 10, integer 1..30. No caller-selected
UID, path, argv, environment or grant is accepted. Unknown operations, including
`capacity`, return `operation_unknown`; malformed shapes return `request_invalid`.

Client syntax is `swarma-demo-client --config /absolute/client.json launch|stop|status`
with the corresponding complete operation object redirected on stdin. Intake is
at most one frame's payload, with a five-second deadline for pipes. Polling never
changes the inherited open description's flags, including on SIGINT/SIGTERM.
Stdin has one consumer; concurrent readers are unsupported. An ordinary
redirected regular file is read with the byte bound on the trusted local filesystem.
The client prints one JSON reply. Local/protocol errors exit 1; a received reply
exits 0 even for refusal/unknown/unresolved. Inspect `state`, never infer absence
from the CLI exit status. Unsupported platforms fail truthfully.

Connection intake/write stages have two-second deadlines under a 55-second
session ceiling. The client allows the operation reply the remaining session
budget; launch receipt observation is at most 10 seconds, whole stop at most 45
seconds per call. A late or restarted stop continuation has a fresh bounded call
wait, but never reports timely. `timely` requires that this process created and
synced the intent before issuing the helper and met the original in-process
deadline; it is decided before syncing the stopped record.
Partial progress never resets a stage deadline.

## Child boundary and observations

The only child argv are `/usr/bin/sudo -n` followed by exactly one of
`/usr/local/libexec/swarma/swarma-user-launcher` and
`/usr/local/libexec/swarma/swarma-user-manager`. There are no further arguments,
no shell, an empty environment and cwd `/`. Sudo establishes its own caller
metadata. The child inherits daemon umask `0177`: the launcher and helper must
set their own umask before creating their required directories and receipts.
Stdin receives exactly one JSON document, then closes, within five
seconds and the operation deadline.

Launcher stdin has exactly `v,op,request_id,generation,execution_id,epoch,executor,plan`,
with `v=1`, `op="launch.start"`, `epoch=1`, at most 80 KiB. The dispatcher generates
`EX-` plus 32 random lowercase hex characters before writing the attempt marker.
Helper stdin has exactly `v,op,request_id,args`, with `v=1`, `op="exec.stop"`, args
exactly `generation,execution_id,grace_seconds`, at most 16 KiB.

Launch stdout/stderr each go to a separately created `0600` log,
`output-<generation>-stdout.log` / `-stderr.log`, capped at 1 MiB. Reading continues
past the cap. First excess creates the corresponding `.truncated` record with
`true`. Capture errors, including errors after `receipt-seen`, create the
corresponding `.lost` record with the JSON string `"output_lost"`, fsync file and directory,
and continue draining/discarding after log-write errors. A pipe read error or
read-deadline setup error instead returns and closes the read end; the executor
may receive EPIPE. `demo.status.output_loss` reports
`stdout:output_lost` or `stderr:output_lost`, including after restart. If the
evidence itself cannot be saved/read, live status reports `loss_unpersisted` or
`evidence_unavailable` for that stream; persistence cannot be promised on failed
storage. Failure to create `.lost` closes admission with `loss_unpersisted`
immediately in memory, attempts the durable latch, and retries that latch on
subsequent requests. Live status retains `loss_unpersisted` for the stream.
Logs never reopen for writing. Pipes use bounded polling waits; child
exit is reaped using nonblocking wait. There is at most one drainer pair per
occupied child slot, bounded by the three-account roster. Within one daemon process, a new generation also
waits for the prior sudo handle and pipes to finish. Restart loses custody of
those handles and pipes: a valid stopped fact can permit a later generation
without waiting for the old launcher process. An unstopped attempt still closes
admission at startup. No code signals a child or
account process. Shutdown closes pipes and releases live handles; executor
survival is not promised, and orphan reaping belongs to the service supervisor.

The receipt path is `<receipt_root>/<generation>/<execution_id>.json`, checked
without following any links, root:root regular single-link `0644`, at most 4096
bytes. Exact members are `execution_id,epoch,pid,start_time,boot_id,session_id`.
Execution ID must match; epoch is 1; PID/session ID and Linux start ticks are
positive unsigned integers; boot ID is a lowercase UUID. Generation is bound by
the protected directory, not an extra receipt member. Observed child exit triggers
one final receipt read inside the original deadline. A valid receipt already on
disk wins over exit; exit with no receipt remains unknown and closes admission.
Read errors are not absence, and a receipt read after the deadline is not promoted.

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
when valid, including with a nonzero exit. Reply `timing` is `timely`, `missed` or `unknown`, independently of
absence. Helper stdout/stderr capture is in memory, capped at 16 KiB each; stderr is not
persisted. The result code is the retained failure observation. Excess stdout
is an invalid result. Exit zero without a document is never absence.

## State and failure behavior

Creates use `O_CREAT|O_EXCL|O_WRONLY|O_NOFOLLOW|O_CLOEXEC`, `0600`, then file and
directory fsync. No state file is deleted, renamed or truncated. An attempt
contains the launch `request_id`, `request_digest` (SHA-256 of the operation bytes), `execution_id`, and
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
returns `stop_conflict`. At most one helper per generation may be outstanding **within one daemon process**.
Successful terminal responses persist in `stopped-<generation>` with exactly
`execution_id,timing,closed,code` (`code` is empty or `latch_uncertain`); the same request replays that observation without
another helper or a new latch, including after restart. An intact original intent
must exist and match to replay a terminal response. This format has no migration from earlier experimental
records; incompatible records refuse startup without repair.

If the original deadline has passed, retry still invokes the helper with the
original request ID and arguments and reports `missed`. After restart that
monotonic deadline is unavailable: retry still invokes the helper, reports
`unknown`, and keeps admission closed. Each continuation is bounded by 45 seconds,
never overlaps a still-live helper or its pipes tracked by that process, and never replenishes the
original deadline. Safe narrowing therefore continues despite late or unknown
time. A late valid zero-remaining result persists `stopped` and reports `absent`
with its non-timely timing. This is not a deliberate contract departure.

| Event | Response / code | Preserved effects |
|---|---|---|
| Cannot open/read `/proc/self/fd` at startup | Daemon returns the enumeration error and exits; no wire reply | No configuration, state or endpoint opened |
| Bad request / unknown account / unknown operation | `refused`: `request_invalid` / `account_unknown` / `operation_unknown` | No marker or child |
| Existing attempt | `refused/attempt_exists` | Original marker; no child |
| Closed / concurrent / previous unresolved | `refused/admission_closed`, `launch_in_flight`, `attempt_unstopped` | No new child |
| Previous sudo or pipes still active | `refused/child_unreaped` or `output_pending` | Original observation only |
| Marker create/sync uncertain | `refused/marker_uncertain`, or `refused/latch_uncertain` if the latch also fails | Never call sudo; close admission in memory; keep all partial files |
| Log setup / delivery failure | `unknown/log_unavailable` or `launcher_io` | Attempt and closed latch |
| Receipt timeout / mismatch / exit without receipt | `unknown/receipt_timeout`, `receipt_mismatch`, `launcher_exit` | Attempt and closed latch; no signal |
| Torn attempt without recoverable execution ID | `refused/attempt_identity_unknown` | Generation remains spent; do not invent helper arguments |
| Stop ID equals this generation’s launch ID | `refused/request_id_conflict` | No intent, no helper, no state change |
| Shutdown before launch admission | `refused/shutdown` | No marker, child or new latch |
| No attempt to stop | `refused/attempt_missing` | Never invent absence |
| Stop argument conflict / active stop | `refused/stop_conflict` or `stop_in_flight` | Original intent and deadline |
| Stop intent durability uncertain | Continue helper with timing `unknown`; include `latch_uncertain` if latch persistence fails | Close admission; this process retains original arguments. After restart missing and torn intents are unknown; never persist guessed arguments |
| Stopped-record uncertainty | `unresolved/stopped_uncertain` | Close admission; keep partial files; never claim absence |
| Nonzero exit / malformed result / remaining processes | `unresolved/helper_exit`, `helper_result_invalid`, `processes_remain` | Closed; no stopped record |
| Helper `ok:false` | `unresolved`, helper code | Closed; no stopped record |
| Stop call timeout | `unresolved/stop_timeout` | Closed; original intent; no stopped record; later safe narrowing allowed |
| Late or restarted continuation | Helper result (`absent` or `unresolved`) with `missed` or `unknown` | Original request/arguments/deadline; closed remains closed |
| Verified absence | `absent` | Exclusive `stopped-<generation>` binds execution; late absence leaves admission closed |
| Latch persistence fails | `latch_uncertain` where applicable | In-memory closed; unresolved attempt or stopped record carrying `closed:true` preserves the fail-closed restart obligation |
| Crash/reboot with unstopped attempt | Startup attempts `closed`, but remains available even if persistence fails | Admission closed in memory; attempt carries the obligation; stop remains reachable |
| Unknown entry, parseable corrupt/orphan state, invalid stopped fact without intact intent, wrong log type/mode, parseable wrong truncation/loss content | Startup refuses | No repair |
| Torn known state record (zero length or invalid JSON) | Startup remains available with admission closed; `status.torn` lists names | No repair, no invented stopped fact; stop continues where execution identity is known |
| Protected garbage or empty `closed` content | Startup remains closed | The protected inode is the latch; content is not parsed; stop remains available |
| Internal launcher/helper request bound | `unknown/launcher_request_limit` or `unresolved/helper_request_limit` | Closed; attempt/intent retained; unreachable under current valid input bounds |
| Helper delivery/start failure | `unresolved/helper_io` | Closed; original intent; no stopped record |
| Shutdown during a wait | `unknown/shutdown` or `unresolved/shutdown` | Closed; no claim of absence; no signal |
| Detected launch reply failure after child call | No deliverable reply; latch reason `launch_reply_lost` | Closed; attempt retained |
| Capture error after receipt reply | Status `output_loss`, with durable `.lost` where storage permits | Log-write errors keep draining; pipe-read errors close the read end; no retrospective readiness claim |

A durable `closed` inode is irreversible. While closed in memory without a durable
latch, every request first retries the exclusive latch create once, keeping the
first reason retained in memory. Status reports
`code:"latch_uncertain"` until persistence succeeds. A pre-name marker failure
combined with a failed latch can leave no evidence at all: restart can reopen
admission in that case. No launcher was called. A second, narrow case is a failed
launch reply detected after a concurrent stop already verified timely absence:
if the launch_reply_lost latch also fails, restart can reopen. A third case is
output loss after a timely stop, when both `.lost` and `closed` cannot be written:
the loss closes admission only in memory, and restart can reopen. In all three cases no
agent child remains (none was launched, or absence was verified); no generation
effect is duplicated. A retained attempt or closed stopped fact otherwise carries the
restart obligation even when the latch cannot be saved. Stop temporarily excludes forward admission before
calling the helper; a failed/unresolved stop persists the latch. A successful
first stop allows the next roster generation after prior child resources finish.
This reads the sequential-admission rule literally; it does not clear a
previously written latch. `demo.status` returns `state:"facts"`, `closed`, and
preserved `attempt`/`stopped`, `torn` and `output_loss` where present, never current process
liveness. A launch request ID may repeat across accounts; a stop ID is scoped to
one account. A stop ID must differ from that generation’s launch ID. After `receipt_timeout`, a helper can report `remaining:0` while the
launcher's sudo child is still alive. Stop then records `absent`, but admission
stays closed. The helper owner must qualify its latest-receipt and process
ownership rules; this dispatcher never signals that sudo child to stop the agent.

### Torn records and restart continuation

A zero-length or invalid-JSON `attempt-`, `stop-`, `stopped-`, `.truncated` or
`.lost` record is torn. Startup preserves the file, closes admission and lists
its name in `demo.status.torn`. A torn stopped record is never evidence of
absence; a torn attempt still spends its generation. Parseable wrong members or
fields still refuse startup. Protected `closed` content is always a latch,
including garbage. Ownership/type/mode/link violations and size-limit violations
remain startup refusals. No recovery path overwrites, removes or renames a file.

Failure to persist a stop intent does not prevent the helper call. This process
retains its original arguments, but timing is `unknown` unless the intent was
created and synced before the helper call and the original deadline was met.
An intent-create latch error is reported as `latch_uncertain`, including on absence.

Every attempt loaded at startup without an intact intent, missing or torn, has
unknown original arguments and time. Never create a new intent for it and never
start a fresh effect deadline. Forward each valid guess to the helper, which owns
request-ID handling; only the per-call wait budget is fresh. Failed/refused or
unresolved guesses do not pin arguments after the tracked child and both pipes
finish. At most one helper remains in flight in this process. An `absent` guess
is replayed for the same arguments in-process, and changed arguments then get
`stop_conflict`. An intact intent instead retains its arguments across restart,
with timing `unknown` until a recorded terminal result is replayed.

A parseable stopped record with missing or torn intent is accepted only with
`closed:true` and non-`timely` timing. It is a closed fact, not a replayable request.
Admission stays closed and later stops still call the helper; the occupied stopped
name produces `stopped_uncertain`. Every absence requires a newly durable stopped
record; no occupied or torn record is repaired. A torn attempt with no intact
intent has no execution ID, so its stop returns `attempt_identity_unknown` while
status and other accounts' stops remain available.

Restart does not wait out an earlier helper. The helper **must safely handle a
same-effect `exec.stop` while an earlier invocation remains alive**, including
identical request IDs. An intact stop intent binds the request ID and grace to
that generation: a different ID or grace returns `stop_conflict`. The dispatcher
also rejects the generation's launch ID as a stop ID with `request_id_conflict`,
and a stop ID already used for another account with `stop_conflict`. The helper instead checks its
generation-scoped record and reports `request_id_conflict` for changed request
bytes. These existing scope differences are not changed by the shared ID format;
IDs do not authorize new effects or changed arguments. A helper descendant must close inherited
stdout/stderr: retaining a pipe produces `stop_timeout`, followed in-process by
`stop_in_flight` until the pipe closes. These are requirements for the separate
helper owner, not behavior qualified by the fake child.

Cancellation before the launch marker is a refusal without effects. Cancellation
after the marker can still issue one launcher; its reply is `unknown/shutdown`
and admission is latched. Cancellation observed during receipt observation returns `unknown/shutdown`, including the
durable latch reason; it is not evidence that a live child exited.

The helper fields `stopped_at`, `signalled` and `killed` are validated but not
passed through to replies or stopped records. A lost stop reply does not add a
latch: the durable result and exact-argument replay preserve safety.

## Test evidence boundary

Go tests use non-root fixture paths and an unexported command seam to launch the
actual test child process. They exercise stdin EOF, pipe backpressure, receipt
observations and exit results without root or sudo. They prove dispatcher
behavior only. The production transport regression is an actual internal socket
exchange; it is not CLI deployment qualification. Protected snapshot and receipt controls use actual current-UID files through
unexported expected-owner seams, plus public-entrypoint refusals of unprotected
config. CLI subprocess tests cover piped, file, oversize and stalled stdin.
Root-owned positive deployment, distinct service/operator UIDs and real sudo
remain unqualified. Fixtures require non-group-writable ancestors.
A bounded source checker includes known forbidden mutation plants; it is not an
interprocedural filesystem proof. Hosted Linux execution and independent review
are required before interpreting these test sources as passing evidence.
