# DRAFT M0 executor entry /1

This is one closed **proposal**, pending independent review and joint
user-manager, credential-broker and catalog/policy owners exact-content selection. It publishes no
runtime, installed executor, selected Result channel or activation authority.
The full [dispatcher design](dispatcher.md) and [M0 gates](m0-launch.md#7-publication-review-and-activation-gates)
remain intact. The input is not a generic command, workspace or environment API.

## 1. Exact owning sources and admission boundary

Manager [helper §7][manager] at `5db5b8bbeba8af7ed6962c87586ef48a0cd5b0b3`
owns approved executor path/digest/verified handle, durable root receipt, verified
supplementary-group removal then real/effective/saved GID and UID drop, locks,
FD3, account-wide stop and absence. Only caller-owned descriptors 0–2 and complete
immutable read-only FD3 reach executor code. No executable, directory, lock,
receipt, service, broker or root handle survives into it. Existing 0–2 are the
caller's pipes/terminal; this proposal adds no manager output channel. Decoded
plan <=48 KiB, base64 <=64 KiB, whole launch request <=80 KiB. Staging, interruption
and cleanup under the generation lock require published finite platform bounds;
no unqualified pipe capacity, path/stdin fallback or extra launcher wire member.

Broker `0b29c086608d808903b9abf286497d941297a078` has an experimental Linux
[command dispatch][main], [CLI parser][options], [runner][runner] and
[protected client][client]. The older [v0 §6][v0] command paragraph still says
“not yet implemented”; it is not current implementation-absence evidence.
Required production observers remain unavailable: ordinary execution refuses
69 `lease_harness_unqualified` with no lease and no admitted app-server; owner
remains stopped. Protected qualification measurements occur **during**
[`protectedAuthority.Resolve` through `productionQualifiedSet`][authority] →
[`static.Config.Qualified`][qualified] → [`qualification.Repository.Read`][qualification-read].
With readable protected root and lock and neither `in-progress` nor `pending-activation`, Read
invokes [`Observe` (native `Version` first)][observe] before reading `qualified.json`.
Absent or malformed `qualified.json` therefore does not imply that no native
qualification measurement occurred. The later runner `versionProbe` follows
successful protected qualification; it is a separate step before lease admission.
These source facts authorize no target execution or probe here; native/OS,
typed-plan/authority and Result gates remain closed. Source availability is not
a qualified deployed/installable package.
`internal/runner` is not a public Go API; the proposed adapter uses the existing
command, never imports that internal package.

The approved adapter MUST be a native executable, never a shebang script:
manager's retained-handle exec does not pin an interpreter or make its script
descriptor readable after close-on-exec. Broker policy must likewise pin a native
Codex binary, not an npm/node wrapper. The second exec of the absolute pinned
broker command, its supervisor, Codex app-server and every transitive shell/tool
exec remain subject to manager R-EXEC's OS allowlist and complete invocation
qualification (entrypoint, arguments, environment and interpreted configuration).
Every tool invocation must exclude worker-writable executable/interpreted code
and configuration and agent re-exec under R-EXEC; no transitive exemption is given.
PATH alone enforces nothing. This proposed task is one bounded text-in/text-out
turn; D requires no checkout, workspace or task-tool invocation. That does not
establish an empty transitive exec list: the actual minimum is still an owning
inventory/allowlist/qualification gate, including explicit protected interpreter
entries and exclusions for compilers, package managers and agent binaries.

## 2. Closed input grammar

Proposed discriminator: `kind="swarma-dispatcher m0-executor-entry/1"`, `v=1`.
Every member below is required, non-null and unique after JSON name unescaping;
every object is closed. No other union, optional field, array or opaque body is
admitted. FD3 contains exactly one UTF-8 JSON object, no BOM or trailing document.
Use the existing normative [JSON/CCJ-1 restrictions](m0-launch.md#2-closed-proposed-forms-and-signing):
reject invalid UTF-8, lone surrogates, duplicate/escaped duplicate names,
noninteger tokens (including `1.0`/`1e0`), `-0`, and unsafe integers. Whitespace
and member order may vary within the encoded bound; no Unicode normalization.
Canonical content identity uses CCJ-1 without final LF, never a pretty-printed
fixture's bytes. Depth <=8 is an intentional stricter subset of upstream <=16.

Notation: `ID` is exactly 32 lowercase hexadecimal ASCII bytes; `HASH` is exactly
`sha256:` plus 64 lowercase hexadecimal ASCII bytes. `SERIAL` is an integer in
1..9007199254740991; `UID` is 30000..59999 (shape only, never range admission).
The key is exactly the existing canonical `ed25519:` + 43-character unpadded
base64url public-key spelling, including the final-bit restriction in the
[signing-plan schema](../schemas/m0-signing-plan-1.schema.json).

| Object | Exactly these members and value types |
|---|---|
| Entry | `v:1`, `kind` as above, `request:Request`, `execution:Execution`, `catalog:HASH`, `qualification:HASH`, `timing:Timing`, `input:Input` |
| Request | `v:1`, `kind:"swarma-dispatcher m0-request/1"`, `request_id:ID`, `receiver:ID`, `policy:Policy`, `association:Association`, `worker:Worker`, `plan:Plan`, `op:"dispatch"` |
| Policy | `id:ID`, `revision:SERIAL` |
| Association | `id:ID`, `revision:SERIAL`, `uid:UID`, `generation:ID`, `key` as above, `registration_number:SERIAL`, `master_epoch:SERIAL` |
| Worker | `uid:UID`, `generation:ID` |
| Plan | `worker_plan:HASH`, `profile:HASH`, `harness:HASH`, `exec_profile:HASH` |
| Execution | `effect_id:ID`, `execution_id` matching ASCII `EX-` followed by exactly 32 lowercase hexadecimal bytes, `controller_epoch:SERIAL` |
| Timing | `deadline_ms:SERIAL` absolute deadline in the exact qualified tuple's coordinate, `slack_ms:SERIAL` positive duration in milliseconds; see §5 |
| Input | `account:Selector`, `profile:Selector`, `model:CatalogString`, `reasoning_effort:CatalogString`, `placement:"station-one-shot-v1"`, `prompt:Prompt` |

The proposed execution-ID spelling is an intentionally restricted adapter subset,
not a change to manager's unpublished complete identifier grammar. Owners must
select compatibility before use. `Selector` is 1..64 ASCII bytes: first `[a-z0-9]`,
then `[a-z0-9_-]`. `CatalogString` is 1..128 printable ASCII bytes U+0021..U+007E,
first byte not `-`; equality with the exact qualified catalog is mandatory.
These shape restrictions neither invent an effort enum nor admit every matching
string. `Prompt` is 1..24576 actual decoded UTF-8 bytes; U+0000 is refused.
Whitespace and newlines within Prompt are preserved. No prompt file/home lookup,
credential content, resume, provider fallback or implicit additional input exists.

Hard parser/work ceilings: encoded FD3 <=49152 bytes, canonical entire entry
<=49152 bytes, total decoded string values <=32768 UTF-8 bytes (excluding member
names), exactly 9 objects/43 members, no arrays, <=512 lexical tokens including
member names and punctuation. Each member name <=32 ASCII bytes; every non-prompt
string <=128 bytes. Check raw bytes before allocation; scan at most limit+1 only
to detect excess. Reuse one bounded parse and one bounded CCJ traversal, each
linear in <=49152 bytes/512 tokens; no recursive dependency expansion, retries,
network resolution, catalog fetching or directory tree walk. Reserve <=256 KiB
aggregate owned adapter parse/canonical/prompt/argv buffers, including
all prompt copies. These parser budgets are separate from D's protected
snapshot budgets in §3; the adapter resolves no
protected metadata. Complete platform/library/syscall work and wall-time
qualification remain gates. These limits
cannot borrow the runner's 1 MiB prompt allowance or widen the manager request.
JSON escapes count toward encoded/canonical bounds: 24576 decoded bytes do not
guarantee fit. Refuse rather than truncate or normalize. No Schema is supplied:
a parsed-object shape checker could not prove lexical or UTF-8 byte restrictions.

## 3. Content selection and semantic authority

Dispatcher D performs protected equality **once before handoff**, against the
independently authenticated durable original R: association/kernel caller,
receiver, worker UID/generation, current policy/association/registration/epochs,
all plan pins and creating-dispatcher association. D also checks original
effect/receipt/claim/execution and controller epoch, complete catalog/profile/
harness/exec-policy/qualification/installation pins, and timing provenance and
equality (§5). These checks allocate no new execution. `request` in FD3 is an
already-bound audit copy of the admitted unsigned dispatch, not a new signature
or worker authority assertion. The adapter is worker target code: it does not
read protected originals/catalog/current policy or resolve metadata by path,
IPC or extra handle. Worker parsing or hash agreement cannot establish those joins.

For this proposal, `request.plan.worker_plan = HASH(CCJ-1(input))`. This defines
one new closed worker-input body, pending catalog-owner selection. Other plan
pins retain their existing meanings. The entry identity is separately
`HASH(CCJ-1(entire entry))`, bound to the manager-admitted FD3 digest in protected
original launch evidence. Manager admits the complete immutable FD3 by that
already-bound digest; it is not a new policy evaluator. The entry identity is
not recursively its own worker-plan hash. The protected catalog must select
this exact input body and the complete typed
profile/harness/exec-policy bodies, authenticated immutable origins, publication,
installation, ownership, currentness and equality rules. This leaf does **not**
publish those bodies or admit digest-matching opaque enabled payloads. Until
those joins exist, even the positive specimen is inadmissible for execution.

The four bounded, preselected protected snapshots belong to **D**, before handoff:
original request/effect/receipt/claim; current policy/association/generation/time; complete
selected catalog/profile/harness/exec-policy/qualification; installation and
placement/containment tuple. D budgets <=4 resolutions, each body <=49152 bytes,
<=256 KiB aggregate metadata storage, with finite lock/read/currentness latency
still requiring qualification. No such resolution occurs in the worker. D binds
immutable installed mapping inputs (§4) and the qualified clock coordinate (§5)
to this finite tuple; neither E/H nor clock access hides another protected read.
No self-minted success field, missing evidence,
failed read, stale snapshot, or restored old reader/writer is authority. The full
typed representation and aggregation within the proposed four-resolution budget
must be jointly published; an oversized or unavailable snapshot closes use.

## 4. Deterministic proposed command and stdin mapping

The finite protected installation tuple checked by D supplies `E`, the absolute
approved broker executable and digest/qualified exec binding, and `H`, the
manager-verified station home. The proposed adapter artifact contains immutable
literal E/H and clock-profile selection; D binds that exact artifact and those
values to installation/profile/qualification before handoff. Manager supplies
its existing fixed HOME environment agreeing with H, not another metadata handle.
The adapter uses those immutable installed literals and unchanged environment;
it performs no protected lookup of E/H, policy, qualification or clock metadata.
Publication, immutability/currentness through exec and safe absolute exec binding
remain owning installation/qualification gates, not facts established here.
These values are not caller fields or PATH lookups. Proposed fixed derivation:
`J=H + "/tmp/m0/" + execution.execution_id`, `L=J + "/home"`,
`W=H + "/work"`, `O=J + "/last-message.txt"`. Paths are absolute UTF-8,
NUL-free and <=1024 bytes each including E/H/J/L/W/O; no alternate spelling,
symlink component, reuse of an unresolved J, or fallback directory is admitted.
The worker, after verified drop, prepares its work/tmp/J/L content under a
published bounded ownership/mode and no-follow contract (launch home 0700).
D-owned approval/intake/root is distinct from worker-created content. Manager
creates no generic workspace. No repository, archive, home traversal or tool
input is imported. These are candidate derivations, **not** proof that same-UID
races, filesystem/config authority, permissions or protected Result access are
resolved. Selection must qualify the exact filesystem/identity tuple, stale
writers/readers/restore fences and safe verified-handle exec mechanism, or refuse.

For one admitted tuple, argv is exactly these 19 elements, in this order, with
separate option/value elements and no shell, `=` spelling, positional tail,
duplicate option, injected flag, prompt-file, resume or credential/config flag:

```text
[E, "run-codex", "--account", input.account,
 "--profile", input.profile, "--launch-home", L,
 "--model", input.model, "--reasoning-effort", input.reasoning_effort,
 "--output-last-message", O, "--prompt-stdin", "--cwd", W,
 "--timeout", decimal(T)]
```

**Claude runner variant (PROPOSED, not launchable).** The runner command
(element 2) is an immutable literal of the adapter artifact, bound to the
installation tuple together with E and H: either `run-codex` or `run-claude`.
Nothing in the entry, the environment or the arguments selects it, and
`run-claude` never falls back to `run-codex`. A Claude installation is a
different artifact and tuple from a Codex one.

The `run-claude` argv below is **PROPOSED**, pending the credential-broker
contract for the Claude runner (broker slice S3a) at a named commit. No such
commit is pinned in this spec: the only broker pin is the `run-codex` revision
in §1. **Normative now:** until that pin is recorded in this spec, an artifact
whose tuple names `run-claude` is refused before exec with adapter status 78,
with no pipe write and no exec. Like every 78 in this spec, it is attributable
only per §5: 78 only with trustworthy original fate evidence of no runner
start, otherwise UNKNOWN with the original debt. The argv text below, the
status requirement and a `run-claude` mapping witness are amended at that pin;
until then this text follows no unnamed or future broker revision. The
proposed shape is the same 19 elements in the same order, with only element 2
changed:

```text
[E, "run-claude", "--account", input.account,
 "--profile", input.profile, "--launch-home", L,
 "--model", input.model, "--reasoning-effort", input.reasoning_effort,
 "--output-last-message", O, "--prompt-stdin", "--cwd", W,
 "--timeout", decimal(T)]
```

The same bounds, derivations (J/L/W/O, T), environment, FD3 handling and stdin
pipe are proposed unchanged. The adapter adds no Claude-specific flag,
environment variable or file; in particular it never sets `ANTHROPIC_BASE_URL`,
`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, a settings path or a config
directory. `input.reasoning_effort` passes through unchanged as a
`CatalogString`; an unsupported value is the runner's refusal, never an adapter
remap. The broker owns the exact `run-claude` option set and its post-exec
statuses. Qualification is per tuple: a `run-claude` tuple needs its own broker
qualification and native negatives, and Codex evidence does not carry over.

**run-claude admission gate.** Independently of the argv pin, a `run-claude`
tuple is inadmissible until the pinned broker contract and native evidence
show all of the following:

1. The vendor process and every descendant hold no vendor-valid credential in
   argv, in the environment, or in any file under L, W or J or at the settings
   path (every settings or config path the vendor reads).
2. The vendor's only credential is a placeholder bound to this execution,
   generation and lease. The gateway refuses it for any other run: another
   execution, generation or lease, and after stop, expiry or release.
3. The gateway endpoint and the settings file are not worker-writable and not
   worker-redirectable: no worker-prepared setting, environment variable or
   helper selects or changes them. A settings file inside worker-prepared
   content does not satisfy this item.

The gateway endpoint and the placeholder are runner-internal and never adapter
inputs. Codex external token delivery ([v0 §6][v0]) is **not** the model for
`run-claude` and is no evidence for this gate. Missing evidence for any item
refuses qualification and admission; passing the adapter-side rules above
proves none of them.

**Runner status set per tuple.** For every admitted tuple, the pinned runner's
status set MUST exclude the adapter refusal status 78. §5 shows this only for
the pinned `run-codex` set. No `run-claude` status set is pinned; a tuple
whose runner status set is unpinned or includes 78 is inadmissible, because 78
would then be ambiguous between an adapter pre-exec refusal and a runner
post-exec exit.

**Denied-literal set.** The adapter cannot decide whether a value "looks like"
a credential, so the refusal rule is literal. The installation's denied-literal
set is a deployment input, bound to the adapter artifact and the installation
tuple like E and H. This spec names its REQUIRED members and their meaning,
never their values:

- the **Keymaker marker**: the literal that identifies Keymaker custody
  material or its endpoint;
- the **denied path prefix**: the absolute path prefix of the protected
  credential area that no worker argument or variable may name;
- the **origin socket marker**: the literal that identifies the credential
  origin socket.

A missing or empty REQUIRED member refuses. For both runner variants, the
bytewise occurrence of any member in any argv element or in any environment
name or value is an adapter pre-exec refusal with no exec (78 only with
trustworthy original fate evidence of no runner start, otherwise UNKNOWN with
the original debt). Argv values are otherwise only the closed entry fields
under D's catalog equality (§2, §3), and the environment is only the closed
six names below: an environment name outside those six is likewise a refusal,
never a silent clear. A deployment may add members to the denied-literal set
but never remove a REQUIRED one.

Actual argv UTF-8 storage, sum of each element's byte length plus one NUL, is
<=8192 bytes; each element <=1024 bytes, and OS exec representation/pointer/env
limits must independently qualify. No caller environment map: use only the
manager's fixed HOME, USER, LOGNAME, PATH, LANG and TMPDIR inside the home;
the adapter passes it unchanged to the runner and adds no CODEX_HOME, model,
endpoint or credential variable. It remains qualified for runner conflict checks;
never silently clear a reserved conflicting variable to gain admission. Broker
reads protected `/etc/swarma-credential-broker/config.json` with its current
same-user R2 reader. Dedicated-service unreadability refuses; no public copy,
weakened mode, `--config`, environment override or invented privileged read.

The broker [independently constructs its vendor environment][preflight]: HOME and CODEX_HOME
equal L, PATH is `/usr/bin:/bin`, LANG and LC_ALL are `C.UTF-8`; TMPDIR and other
incoming variables are not forwarded. Published [D-PATH](dispatcher.md#53-execution)
and [pinned helper D-PATH][d-path] instead compose the protected tool directory,
admitted user base PATH and protected dispatcher wrapper directory. There is no
public selection of PATH(b). This leaf **proposes** narrowing M0's vendor PATH
to `/usr/bin:/bin`, excluding both protected directories, pending owning content
selection of the full profile/plan relation and D's protected equality. This is
distinct from the helper bootstrap environment, which the adapter passes
unchanged; it introduces no adapter PATH rewrite, broker flag or format. Protected
environment conformance and OS exec enforcement are separate gates: the kernel
does not refuse a PATH change. Qualify actual forbidden exec attempts (absolute
paths, writable aliases/copies and unentered interpreters), independently of
environment equality. The proposed narrowing grants no R-EXEC permission,
published complete profile, OS allowlist or native credit.

FD3 is the immutable plan, **never runner stdin**. The native adapter parses it
completely, retains only bounded decoded values needed for mapping/audit, and
closes the plan kernel object and EVERY duplicate **before creating the stdin
pipe**. Never reopen it by path.

**Pre-filled stdin pipe (no feeder).** M0 has no feeder process or thread. After
closing the plan, the adapter creates one fresh pipe with `pipe2(O_CLOEXEC)`,
reads its capacity with `F_GETPIPE_SZ` and requires capacity >= the exact decoded
Prompt byte length (at most 24576): capacity equal to the length is accepted,
capacity one byte below the length is refused. It never raises capacity with
`F_SETPIPE_SZ`: a pipe shrunk by the per-user pipe-buffer soft limit must
refuse, not adapt. It sets the write end `O_NONBLOCK` with `F_SETFL` and writes
the exact decoded Prompt UTF-8 bytes in one whole write, then closes the write
end, so the runner reads exactly those bytes and then EOF. The read end stays
open in the adapter across the write, so the write can neither fail with
`EPIPE` nor raise `SIGPIPE`. No added LF, normalization, truncation, home
input, second prompt or lease retry is allowed. A blocking write is never used,
so no pre-exec step waits for a reader.

The refusal set is closed. A failure of any of these steps is an adapter
pre-exec refusal with status 78 and no exec: `pipe2`; `F_GETPIPE_SZ`, including
capacity below the length; `F_SETFL`; the write; `close`; `dup2`; and the
identity and access-mode check below. The write fails when it returns an error
(`EAGAIN` included) or fewer bytes than the Prompt length. It is retried on
`EINTR` ONLY when zero bytes were written; any partial write is a refusal and
nothing is retried after it. Each of these refusals is attributable only per
§5: 78 only with trustworthy original fate evidence of no runner start,
otherwise UNKNOWN with the original debt.

The adapter installs the pipe read end as 0 with `dup2` and closes both
original pipe descriptors. Every descriptor other than 0–2 is closed or
close-on-exec. Before exec the adapter verifies, by kernel object identity AND
access mode, that only 0–2 lack `FD_CLOEXEC`, that fd 0 is the read-only end of
this pipe, and that no other inherited descriptor names the pipe or the plan
object. Identity alone cannot tell the two ends of a pipe apart, so the
access-mode check is what shows that no pipe write end
survives, and directly `execve(E, argv, unchanged_environment)`: the **same
process becomes the pinned runner**, with no surviving adapter supervisor. Only 0–2
reach the runner. The adapter's userland prompt copy ends with the replaced
address space; the unread bytes live only in the pipe buffer, which is released
when the runner and its descendants close the read end. Because nothing runs
beside the runner, there is no feeder cancellation, join, reaping or
prompt-buffer disposal to qualify after exec; the runner remains within the
original generation's whole-account stop/absence scope. This mechanism is Linux
only; macOS parity remains open and has no feeder fallback. Pipe-capacity,
order and descriptor-identity qualification remain native gates; missing
evidence closes use.

Runner-internal qualified guard/vendor descriptors are broker-owned, not extra
inherited adapter descriptors. Caller stdout/stderr remain existing observation
paths. The plan object and ALL its duplicates must be absent in the runner
(and downstream), verified by
kernel object identity (for example device/inode or qualified open-file-description
comparison), not descriptor number or adapter logs. Broker-created internal
descriptors may later reuse number 3; that alone is no violation.
The current runner does not inspect/refuse an inherited plan descriptor: a native negative must
observe accidental plan-object/duplicate inheritance and fail the adapter/profile qualification,
not claim the runner implements a refusal.
Unknown launch/prompt/lease effect is reconciled on the original execution;
restart cannot perform a second physical start or repeat the prompt.


A separate current broker handle gate remains OPEN. At the pinned revision,
[containment_linux.go lines 62–64][containment] starts app-server through
`/proc/self/fd/3` with `ExtraFiles=[executable]`: app-server holds the pinned
Codex executable without CLOEXEC. Tool inheritance/re-exec is a source-identified
risk, not a native observation here. A path/mount allowlist alone cannot prohibit
re-exec of the same already-allowed agent file through an inherited handle.
Credential-broker must fix that transfer and provide native trace/regression
proof before M0 use. The positive control starts app-server once from the pinned
digest; negatives must prevent model tools holding/reopening the agent object or
re-executing it through inherited descriptors, `/proc/.../fd`, `/proc/.../exe`,
`fexecve` or `execveat`/`AT_EMPTY_PATH`. Evidence must name the actual enforcement
and object identity; do not infer this property from PATH or path allowlisting.
This is not a blanket exemption for broker-internal descriptors. No fix, trace,
qualification or waiver is supplied by this entry proposal.

## 5. Original clock, stop and result boundaries

The adapter is target/executor code. Selected A's receiver-owned durable C must
be after root receipt/verified drop and **before ANY target code, including
adapter/loader/constructor code**. A pre-C decoder is not permitted by this
proposal. Serialized C/Rv/expiry,
equality refusal, original spent claim/debit, qualified late S/residual and
all-resumer closure remain [M0 §3](m0-launch.md#3-protected-installation-currentness-and-ordering).
The owning mechanism is unpublished/unqualified; userspace validation cannot
replace it. Do not add policy evaluation/argv fields to launcher.

FD3 carries the original absolute deadline D as `timing.deadline_ms` and selected
positive qualified slack S as `timing.slack_ms`, each integer 1..9007199254740991
milliseconds. D (the Dispatcher) checks both values' equality/provenance against
the original protected execution and exact current qualified installation tuple
before handoff. They are not worker choices, input-hash authority, or retry clocks.
The two-member closed Timing object is the smallest proposed bound representation;
it adds no opaque clock context or protected worker lookup. Its common time
coordinate is selected by the exact pinned qualified tuple's immutable installed
clock profile. Adapter reads `now_ms` only from that qualified clock, an integer
0..9007199254740991 in the same coordinate. No universal epoch, watermark,
confidence interval or trusted clock mechanism is supplied by these numbers.
Qualification must establish original-deadline conversion/equality, conservative
millisecond representation, confidence/currentness, finite syscall/preparation/
stop bounds, and slack covering remaining handoff, cleanup, storage/publication
and independent stop/absence. Unsupported conversion, stale/restored or wrong
clock, unbound D/S or missing finite bounds refuses; worker code reads no protected
clock metadata. This is a proposed format, not a qualified clock mechanism.

No fresh 300 s timeout: propose
`T=min(240, floor((deadline_ms-now_ms-slack_ms)/1000))`, require T>=1.
Use checked integer arithmetic; overflow refuses. Re-read only the qualified
clock at exec and recompute this cap downward; never reset D/S or round upward.
Decimal T is an integer 1..240, exactly representable by the current float-seconds
CLI conversion. D binds the earliest original applicable completion deadline;
independent original authority/phase expiry can still shorten/refuse admission
and stop it, without a hidden adapter policy read. Synthetic D=290000 ms,
now=20000 ms, S=10000 ms gives T=240; a 900 ms remainder refuses.
This arithmetic is a stricter proposal, not qualification of any actual S.
Runner timeout bounds initialization+turn only; cleanup is separate. No flag,
stdout deadline, context or timer proves bounded regular-file read/write/fsync,
metadata latency, process absence, or the global D deadline. Missing finite
storage/stop slack qualification closes use, never invents a constant safe S.

Preserve original observation <=30 s, detection-to-stop <=270 s and intent/incident/
expiry-to-completion <=300 s, earliest shorter bound or real original P7 wins.
Intent 0/detection 20 requires completion 290, real P7 deadline 280 requires 280;
291 is late. Receipt, Rv, ACK, retries, nested preparation and recovery reset none.
Original bootstrap t_B+120/B0–B5 and proposed 2 MiB work/32 dependency/512 KiB
retained inventory budgets remain [separate unchanged gates](m0-bootstrap-budget.md).

The pinned `run-codex` runner's statuses 0, 1, 64, 69, 75, 124, 130 and
[phase precedence][runner] remain
runner-owned after successful exec; there is no wrapper remap. This leaf proposes
reserved **adapter pre-exec refusal 78**, disjoint from that pinned set, pending
owning changed-surface review; it is not an upstream status extension.
Disjointness is required per tuple (§4) and is shown here for no other runner. A known
attributable adapter refusal emits 78 only after confirmed no runner start (exec not
attempted or failed). It preserves the original spent
claim/debit and any preparation/cleanup debt; it does not assert universal no-effect.
Foreign adapter errors/timeouts are never relabelled runner-owned 64/69/75/124/130.

Trustworthy attribution would require installed original fate evidence bound to
execution, generation, process incarnation and approved adapter artifact/digest,
observing the exclusive pre-exec phase, failed/not-attempted exec, exact exit 78
and no runner start. The observer and finite mechanism
are unsupported/unqualified here. Numeric 78 alone authenticates neither actor,
phase nor absence and cannot replace that evidence. Exec success replaces the
adapter with the runner, whose statuses retain their own meaning. Missing,
unattributable or mixed observations, raw signals, unknown exec/start fate
or lost completion are **UNKNOWN completion**: retain all original publication,
Result, effect, cleanup, lease/token and stop debt. Never infer no-effect,
release, absence, replay/admission or Result/read authority from status alone.

Successful final atomic rename fixes runner terminal 0 despite racing/later stop;
D must not rewrite that fact to 75/130. D disposition/deadline failure is separate.
Only matching completed turn plus confirmed cleanup/release permits 0600 atomic
replacement. A known runner refusal before publication preserves old O, or leaves
a previously absent O absent. Successful `output.root.Rename` is the irreversible
publication point ([output commit][output], [runner publication][run]): it replaces
old O or creates O for a fresh J. D can lose reliable completion observation
afterward, so unknown completion can coexist with **new O**;
it does not imply preservation of old output or absence of publication. Runner
terminal 0 after successful rename is distinct from D's observed completion and
disposition. Bytes or mode 0600 grant no D read/consumption authority, new success
or failure disposition, or fresh admission. Retain original publication/Result/
effect debt on unknown completion. Known staging is removed on prepublication
failure; unknown cleanup retains its original debt. The [adjacent inert boundary
witnesses](../fixtures/m0-executor-entry/boundary.witnesses.json) cover both old
and previously absent O; they establish no runtime behavior or Result authority.
Cleanup shares 5 s, process confirmation <=3.5 s, release <=remaining 2 s; no claimed
bound on all syscalls/guard lifecycle follows. Fresh connection/kernel identity
and actual protected policy rechecks remain broker-owned. D never requests,
renews/releases leases or becomes broker observer. Runner success supplies no
D durable receipt, account-wide absence or token-revocation proof. Manager's
account-wide absence and independently installed stop stay authoritative.

Result channel/sink/encoding/authentication/ACL/atomic durability, byte/frame/
aggregate/state/retention/overflow budgets and old-writer/reader/restore fences
are jointly **unselected**. Existing stdio is a capture candidate, not already
authenticated durable Result. O is a proposed worker output location, not D read
authority: worker mode 0600 alone grants none. No arbitrary sink/extra FD, raw vendor
stderr/frames/errors, prompt/secret logging or token hashes. Preserve sanitized
assistant deltas; they are nonterminal. This leaf selects no capture quota; any
future candidate quota must reserve full storage before admission and retain
unknown original outcome/effects/claim/custody/stop/lease/token debt on overflow,
disconnect or delivery failure. No quarantine/wipe/retire/rebind/purge permission.
G2B/planned removal vs security-window/EIC and actual policy horizon(b)/issuance
coverage/custody remain unchanged; no v0 security/cancel predicate or NEW-lease hook.

## 6. Inert conformance and activation

[Specimens and forbidden controls](../fixtures/m0-executor-entry/README.md) are
inert documentary obligations, not a parser, validator or native test harness.
Production entry coverage is **0/8 obligation groups**, with no production call
sites: lexical/shape; finite byte/work; protected identity/provenance; argv/stdin;
handle/drop/FD; original clocks/stop/replay; qualification/config; Result/debt.
Native cases are PENDING. Shape success proves no authorization, original time,
permissions, descriptor immutability, process absence or Result authenticity.

Independent review, joint user-manager + credential-broker review and catalog/policy
owner exact-content selection precede adapter code; owning typed catalog/install/profile,
FD3/C/platform qualification and authenticated Result joins precede activation.
Required hosted checks and signed delivery remain delivery gates.
No local runtime/build/project test or live broker/credential operation is
required or claimed for this documentary slice.

[manager]: https://github.com/relux-works/swarma-user-manager/blob/5db5b8bbeba8af7ed6962c87586ef48a0cd5b0b3/spec/helper.md#7-the-launcher-swarma-user-launcher
[d-path]: https://github.com/relux-works/swarma-user-manager/blob/5db5b8bbeba8af7ed6962c87586ef48a0cd5b0b3/spec/helper.md#L481
[runner]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/docs/runner.md
[client]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/docs/broker-client.md
[options]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/internal/runner/options.go
[main]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/cmd/swarma-credential-broker/main.go
[preflight]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/internal/runner/preflight_linux.go
[v0]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/spec/v0-codex-refresher.md#6-external-token-delivery-and-one-shot-runner

[containment]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/internal/runner/containment_linux.go#L62
[authority]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/internal/runner/authority_linux.go
[qualified]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/internal/policy/qualification_linux.go
[qualification-read]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/internal/qualification/storage_linux.go
[observe]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/internal/qualification/observer_linux.go
[output]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/internal/runner/output.go
[run]: https://github.com/relux-works/swarma-credential-broker/blob/0b29c086608d808903b9abf286497d941297a078/internal/runner/run.go
