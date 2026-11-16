# DRAFT executor-entry documentary specimens

The [closed proposal](../../spec/m0-executor-entry.md) governs this inert family.
`entry.specimen.json` is shape-positive, **not** authenticated or qualified
execution authority. Synthetic identities, catalog/model/effort and pins grant
nothing. Matching independently authenticated durable originals and a complete
current protected tuple are hypothetical controls, never supplied by these files.
Production coverage remains **0/8**; no runtime, build, project test, native
execution, probe, rename or product validator was run or supplied.

Dispatcher D performs protected equality once before handoff, including original
request/effect/claim/execution, current policy/association/registration/epochs,
full catalog/profile/qualification/installation and timing provenance. The four
bounded snapshots and metadata budget belong to D. Manager admits immutable FD3
by its already-bound entry digest; worker adapter carries an audit copy and
performs no protected path/IPC/extra-handle read. Immutable installed E/H and
clock-profile inputs must be bound to the exact finite published/qualified tuple.
Those publication/currentness and full typed authority joins remain OPEN.

Entry now has **9 closed objects / 43 members**, including the two-integer Timing
object: absolute `deadline_ms` and positive duration `slack_ms`, each
1..9007199254740991 milliseconds. Exact qualified tuple selection must establish
the common clock coordinate, original-deadline conversion/equality, conservative
representation, confidence/currentness and finite syscall/stop/slack bounds.
The adapter obtains only `now_ms` from that qualified clock; no worker timing
choice, protected lookup or retry reset. This proposed format supplies no real
clock mechanism or universal clock constant.

`mapping.witness.json` records immutable synthetic path examples, unchanged
19-element argv (392 bytes including NULs), exact 28-byte stdin then EOF, and
content identities for the ASCII specimen. CCJ-1(input) is unchanged at 186 bytes,
so worker_plan is unchanged. Adding bound timing changes CCJ-1(entry) from 1445
to 1494 bytes and its entry digest; the specimen has 173 lexical tokens, below
the unchanged 512 ceiling. The mapping records the exact new hash. Pretty JSON
bytes are not canonical identity. Synthetic D=290000 ms, now=20000 ms, S=10000 ms
gives T=240. A 1000 ms remainder gives T=1; 900 ms refuses, never rounds upward.
Prompt LF is preserved exactly; a 24576-byte prompt fits only if whole encoded/
canonical bounds still fit. Added LF, 24577 bytes, escape expansion over 49152
bytes, missing/tampered D/S or unqualified clock are negative neighbors.

Published [D-PATH](../../spec/dispatcher.md#53-execution) and the
[pinned manager helper](https://github.com/relux-works/swarma-user-manager/blob/5db5b8bbeba8af7ed6962c87586ef48a0cd5b0b3/spec/helper.md#L481)
include protected tool and dispatcher wrapper directories around the admitted
user base PATH. Vendor `/usr/bin:/bin` is only a **proposed M0 narrowing pending
owner selection**, excluding those protected directories. It is distinct from
the unchanged helper bootstrap environment. No adapter PATH rewrite, broker flag,
R-EXEC permission or kernel PATH enforcement is claimed.

`boundary.witnesses.json` pairs known pre-exec refusal 78, successful exec with
runner-owned 0/69, known failed exec and unknown exec/status observations.
The native adapter directly execve-replaces itself with the pinned command;
there is no surviving adapter supervisor. Close FD3's kernel object and every
duplicate before creating the stdin pipe. The runner must lack that object;
only 0–2 reach runner. There is no feeder: the adapter writes the exact prompt
once, non-blocking, into a fresh `pipe2(O_CLOEXEC)` pipe whose `F_GETPIPE_SZ`
capacity covers it (capacity equal to the length is accepted, one byte below is
refused) and closes the write end, without raising capacity. A failure of
`pipe2`, `F_GETPIPE_SZ`, `F_SETFL`, the write, `close`, `dup2` or the identity
and access-mode check is an adapter pre-exec refusal: 78 only with trustworthy
original fate evidence of no runner start, otherwise UNKNOWN with the original
debt. Pipe-capacity, order and
descriptor-identity qualification remain UNQUALIFIED, including whole-account
absence after exec. No second prompt/start
or lease replay is permitted after unknown effect.

Status 78 is a proposed reserved adapter pre-exec refusal, pending owning review,
disjoint from pinned `run-codex` runner statuses 0, 1, 64, 69, 75, 124, 130;
every admitted tuple's runner status set must exclude 78. Only trustworthy
original fate evidence binding actor/process/artifact/execution/generation,
exclusive pre-exec phase, and no runner start could
support that attribution. The mechanism is unsupported here. Numeric 78 alone,
collision, mixed/raw signal or lost observation means UNKNOWN completion and
retains all original publication/Result/effect/cleanup/lease/token/stop debt.
Successful exec becomes runner with unchanged statuses. No status grants Result,
read, absence, release, debt discharge or fresh admission authority.

The original qualification-order witnesses remain: readable protected root/lock
with no in-progress/pending-activation permits Observe (Version first) before
qualified.json. Absent/malformed records do not prove no measurement. Successful
protected qualification is only the conditional neighbor for later versionProbe;
refusal 69 admits no lease/app-server. No probe is authorized here.

Publication witnesses retain old O and absent O neighbors for known failure
before publication, observed success, and rename succeeded/completion unobserved.
Rename replaces/creates O and fixes runner terminal 0 despite later stop; D's
observation/disposition is separate. Unknown retains original publication/Result/
effect and cleanup debt. Bytes/mode 0600 grant no read/consumption, disposition
or fresh admission. No Result protocol, ACL, quota or wipe authority is added.

`forbidden-controls.json` preserves the original 42 controls and the two
qualification/publication additions in relative order, with 22 additional
controls (66 total). Descriptive spacing and actor-specific expectations were
revised; these strings are not preserved-byte claims. The pipe controls are one
mutation each (`stdin-pipe-blocking-write`, `stdin-pipe-partial-write`,
`stdin-pipe-capacity-raise`, `stdin-pipe-write-end-survives`, `stdin-feeder`,
`stdin-pipe-capacity-short`), and `prompt-decoded-size` pins 24577 decoded
bytes against the accepted 24576.

The `run-claude` runner variant is PROPOSED and not launchable:
`runner-variant-unpinned` refuses every such artifact before exec until the
credential-broker contract is pinned in the spec. `runner-variant-selection`
and `claude-credential-in-adapter` are adapter-side controls;
`claude-vendor-valid-credential`, `claude-placeholder-not-run-bound` and
`claude-worker-settings` are the admission-gate controls, and
`status-collision-run-claude` requires a runner status set without 78. Their
valid neighbors are hypothetical until that pin.

Each control lists in `neighbors` at least one valid neighbor witness by id;
the witnesses are the `valid_neighbors` cases in `boundary.witnesses.json`.
`matching-control` is the unchanged matching hypothetical control;
lexical changes apply to raw bytes, protected identity changes hold originals
fixed. A repository test checks only that control ids are unique, that every
neighbor id resolves and that the total stated here equals the file. That
check is documentary consistency evidence, not conformance or production
coverage. Native substitutions require a qualified tuple, never supplied here.
These are review vectors, not executable tests: valid JSON is no conformance
verdict. Local static JSON/hash/arithmetic/link/diff checks and bounded instrument
plants provide documentary consistency evidence only.

Selected A still requires receiver C before ANY adapter/loader/constructor code.
Native/no-shebang, FD kernel-object/all-copy isolation, full per-tool R-EXEC
invocation and writable-code/agent-reexec gates remain OPEN. The current broker
agent executable handle inheritance/re-exec issue requires an owning fix and
native trace. PATH or a path allowlist is no shortcut; broker-owned descriptors
may reuse number 3, which alone is no plan leak or evidence of safety. Original
clocks/replay/stop and all Result/debt/retention/restore joins remain unchanged.
