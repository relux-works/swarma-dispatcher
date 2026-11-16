# swarma-dispatcher

The launch dispatcher for agent systems. It accepts launch requests from people, orchestrators and tools, decides whether this machine can take the run now, provisions a per-agent OS account and, for trusted agents, its credential binding, starts the agent's executor under that account, tracks the run to its end, and reconciles after crashes.

It works with a privileged account manager and launcher, and an unprivileged credential broker, each kept narrow:

- [swarma-user-manager](https://github.com/relux-works/swarma-user-manager) creates and retires per-agent OS accounts; its launcher starts one approved executor under one agent account;
- [swarma-credential-broker](https://github.com/relux-works/swarma-credential-broker) binds trusted agent accounts and leases the subscription credential their grants allow; untrusted allocations never get subscription bindings or leases.

The one-shot executor is `curator-run` from `curator-agent-launcher`, in a versioned narrow exec mode under the agent account: it verifies and executes a caller-composed plan, never composes or resolves profiles, and has no network access except to the broker. It is the first broker consumer; the board runner and session host ask the dispatcher rather than embedding broker clients. Its intake and the Decision 0021 amendment must be published and qualified before activation. SH2+ long sessions instead use the agent-side `swarma-session-runner`, sharing the launch library; the host service owns session metadata and fences, while the runner owns PTY or app-server transport and lease renewal.

**Status: production launch design in progress; byte-frame/admission libraries, Linux capacity-refusal transport and a separate demo-only launch path have source implementations. Native deployment remains unqualified.**
The [demo cut](spec/demo-launch.md) is **not production and not a conforming dispatcher**.
Its fake-child tests exercise dispatcher behavior only; real-account execution
requires separate qualification. The production binaries remain capacity-only.
The demo reads the helper's closed three-account `demo-roster/1` schema and
binds it to the protected caller, service and executor configuration. Before
starting the daemon, confirm that the helper accepts the roster on the current
boot: the dispatcher checks boot ID and runtime flags for shape only, so a helper
refusal at first launch spends that account's generation and latches admission. Unpersisted
output loss closes admission in memory, and durably when the latch can be written;
client intake preserves shared stdin flags even
when terminated by a signal. These source changes still require Linux execution
and deployment qualification.
[`internal/frame`](internal/frame/) reads and writes a four-byte unsigned
big-endian length followed by 1–131072 payload bytes, per
[§8.3](spec/dispatcher.md#83-socket-dispatch1) and the closed
[byte-frame contract](spec/frame.md). Empty frames are rejected before
payload allocation/read; nonempty bytes remain opaque. This I/O primitive does
not validate JSON, CCJ-1 or object shape, authenticate callers, implement a daemon
or launcher, or establish launch authority. A failed read returns no payload;
callers must discard the stream after a framing error, including a partial write.
Clean EOF occurs only before a new header; EOF within a frame reports truncation.
[`internal/admission`](internal/admission/) supplies bounded request deduplication,
independent allocation/execution quotas, cancellation and epoch/version-fenced
completion. Unknown outcomes retain their claims. A trusted adapter must supply
authenticated principals, current gates and independently verified evidence;
the reducer supplies neither durable storage nor process-absence verification.
[`internal/journal`](internal/journal/) implements the opaque local
[durable-journal contract](spec/journal.md), with explicit initialization,
two-pass established recovery, pinned ownership, immutable inputs and record
replacement. It has no daemon integration or semantic transition schema.
Sync failure requires process exit and an externally enforced recovery hold;
Linux execution and independent acceptance remain pending. See the
[package obligations](internal/journal/doc.go).
No qualified production launch service is supplied. The specification (the `swarma-dispatcher`
Unix-socket service on `dispatch/1` from slice 0; later queueing, events, long waits
and integrations behind capability gates on the same core and transport) is
[`spec/dispatcher.md`](spec/dispatcher.md); diagrams are in [`diagrams/`](diagrams/).

The specification describes the full launch design. Current dependency subsets do not make it available: user-manager station v0 excludes launcher, general list, `effect.observe` and `exec.stop`, with operator-only per-act export-waiver retirement; broker standalone v0 uses a static operator UID allowlist without signed grants/delegation/revocation/bindings or dispatcher integration. An `active` station account is not launchable. The [minimal non-federated full protected-launch target](spec/dispatcher.md#21-full-protected-launch-target-and-dependency-gates) names the missing joins and owners; it does not select the immediate milestone. A proposed reduced launch/lease milestone using v0 credential leasing remains separately unavailable without an explicit owner-approved, jointly published and qualified limited caller launch-authority/currentness policy or real broker slice-0 grant/revocation integration. The policy remains unaccepted; full target grant/freshness/binding gates remain intact. Linux is first in work order; macOS parity requires native qualification before activation. No unqualified platform or standalone allowlist opens dispatcher admission.

A separate [DRAFT M0 existing-worker candidate](spec/m0-launch.md) proposes one protected limited policy, closed dispatch/owned-cancel forms, durable one-launch replay and original M0 stop obligations using activated broker-v0 leasing without binding. Its M0 selected-A design uses committed C after verified privilege drop with qualified residual/stop bounds inside original deadlines; strict native S remains the full-acceptance direction B. Planned removal/expiry is not a P7 trigger; the [classification, original clocks and closed security-response gate](spec/m0-launch.md#5-planned-policy-removal-security-response-and-retained-cleanup) retain separate broker material, lease and token effects, following the [landed manager selection](https://github.com/relux-works/swarma-user-manager/blob/5db5b8bbeba8af7ed6962c87586ef48a0cd5b0b3/docs/design/m0-start-invalidation.md#clocks-and-completion). Runtime enforcement is PENDING. Its [schemas and synthetic fixtures](fixtures/m0-launch/README.md) are for joint review; authority installation, receiver selection, first-caller/bootstrap, exact tuple qualification and owner activation remain closed. The full grant-bearing design is unchanged.

Purpose-6 station-caller custody and protected dispatcher association supersede the historical home-generated/operator-enrolled bootstrap path. Purpose allocation, protected typed plans, both signing adapters, association/proof/replay/rotation and exact native qualification remain gates for **all** station-caller paths, including operator-enrolled bootstrap and recursive delegation. Private keys stay in Keymaker; the separate operator service root retains its own authority. Registry federation/publication is not required for the non-federated worker target, and purpose-2 registrar enrollment cannot enroll purpose 6. See [caller gates](spec/dispatcher.md#33-caller-keys-and-request-signatures), [contract counterexamples](spec/dispatcher.md#151-bounded-documentation-contract-counterexamples) and the [unresolved index](UNRESOLVED_QUESTIONS.md), which points to §16.

A [dispatcher bootstrap-budget contribution](spec/m0-bootstrap-budget.md) proposes
finite endpoint/load ceilings against pinned Keymaker B0–B5. The complete joint
workload and separate pre-B0 worker-status setup budget remain unqualified;
consumer/independent review and owner acceptance precede implementation.

A separate [DRAFT closed executor-entry proposal](spec/m0-executor-entry.md)
defines one finite FD3 input and deterministic existing `run-codex` argv/UTF-8
stdin mapping, with [inert specimens](fixtures/m0-executor-entry/README.md).
D-side protected equality, bound deadline/slack and direct native exec with a
capacity-checked pre-filled stdin pipe (no feeder) are proposed only. A
`run-claude` runner variant is PROPOSED text only: D refuses to bind it and the
adapter refuses it before exec until the pin of its credential-broker contract
is recorded in the executor-entry spec. PATH narrowing and adapter
refusal status 78 await owning selection. Typed catalog/installation, native
qualification and authenticated Result joins remain gates; no adapter runtime
is supplied.

## Principles

- The dispatcher runs under its own unprivileged service account; root stays in the helper and the launcher.
- Operator authority belongs to the dedicated `swarma-operator` account, which never hosts harness sessions and is the broker operator/account owner and user manager operator. The main path signs exact operator acts on the phone with a hardware-backed, presence-gated operator device key after content display and biometric confirmation; SSH over the tailnet enters a forced operator command with key-only login and no shell. The terminal fallback uses password-authenticated `sudo` into `swarma-operator`, no `NOPASSWD` and `timestamp_timeout=0`. Widening acts confirm exact content; narrowing acts need authentication only, with one biometric confirmation for phone revocation (§3.2).
- `team/1`, standing retirement/re-creation, explicit home retention, maintenance stop authority, standing re-grants, export waiver, soft-row acceptance, emergency-stop recovery and trusted-circle admission are widening acts requiring authentication and exact-content confirmation. Every operator act uses signed `swarma-operator` records via the phone or terminal path and is refused from inside agent sessions, including narrowing acts. Mandatory read-only `swarma-observer` admits dispatcher state reads (status, grants, accounts, bindings) without confirmation. Operator command/signature and additional read formats remain unpublished; key enrolment, rotation and phone-loss recovery remain open, with terminal re-enrolment proposed (§§3.2, 8.1, 16).
- A root request ID is scoped to the caller and kept for correlation; create, bind, launch, stop, unbind and retire each use a distinct durable effect ID (§4.5). Repeating a request returns the same records; retrying an effect reuses its ID.
- A caller gets only what its own grant allows; the dispatcher narrows it further for the agent.
- Capacity closures refuse initially; later queue capability adds durable queueing. Quota and authority failures always refuse and never queue. Prepare, dequeue and every start use fresh broker `revocation.check`; `revoked` and `unavailable` refuse, while final broker checks stay mandatory.
- Except for the narrowly gated pre-key registration exception (§3.3), mutating `dispatch-op/1` requests sign exactly `ASCII("swarma-dispatcher request/1") || 0x0a || CCJ-1(R)` without `auth` and without a terminating LF (§3.3). The exception itself stays disabled until its proof/replay contract qualifies. The repository-based wire naming decision needs no migration because nothing has shipped; transport/schema labels and object-kind discriminators are distinct from signed domains. The shared [naming index](https://github.com/relux-works/wiki/blob/main/naming.md) owns typed rule/format references; exact register rows/checks remain cross-module publication work ([§16](spec/dispatcher.md#16-open-questions)).
- Accounting, admission and cleanup belong here; SH2+ holding moves to the session host module's runner under the agent account after the backend gate. Session host owns output/queued-input/evidence budgets per session/principal and in aggregate, retained debt across replacement/retirement, bounded buffers/refusal and a hard control/journal reserve that producer output cannot consume; the supervisor output bound carries through the handoff ([§6](spec/dispatcher.md#6-holding-and-stopping), §§9, 12–13, §16 question 5). Storage custody, numeric budgets and retention formats remain unpublished.
- The dispatcher, board and goals are optional to direct Curator launches. The session host must work without a board, goals, an orchestrator or its toolchain.
- Standing team allocations have no expiry or lifetime quota. Only the operator retires or re-creates them with a signed record; the sweep never expires them (§§3.5, 5.4, 7.2). Their executions have no `max_runtime` quota or runtime deadline and the sweep never stops them for runtime, per the owner decision of 2026-10-12 (R3 closed in §16). They end only by explicit stop through `exec.stop`, operator action, own exit or an incident hard stop. Ordinary allocations keep the 24 h default and 7-day maximum; every other execution keeps the 4 h default and 12 h maximum.
- The operator-only bring-up-team operation creates or reconciles one standing allocation per role from an operator-signed description, idempotently. Its request object `swarma-dispatcher team/1` is specified-but-unpublished (§5.5).
- A new orchestrator's requester signs its narrowed caller delegation after generation-bound public-key association: exactly `ASCII("swarma-credential-broker grant/1") || 0x0a || CCJ-1(B)` for the closed grant body, without envelope `id`/`sig` or a terminating LF. The dispatcher verifies and records it, never substitutes as issuer. Registration alone gives no spawn authority; purpose-6/bootstrap, both adapters, replay/rotation and depth preflight remain gates even for operator enrollment (§§3.3, 5.2).
- Workspace-capable allocations carry an export cleanup obligation: complete-content custody of the result and every referenced blob outside retirement deletion, or an explicit per-act operator waiver, precedes home deletion (R-TRANSFER). Original admission consents to bounded export after ordinary expiry; security holds remain closed, every physical launch has distinct execution evidence, and worker repair needs current authority. Terminal refusal stays immutable; bounded same-workspace repair or per-act waiver retains the default, with standing discard unchosen and off. Retained content requires current authenticated caller retrieval and import acknowledgement or explicit authorized release (§§3.4, 5.2). Retirement follows the user manager's single account-wide stop scope, quiesce, declared-key cleanup and home deletion rule. Ordinary cleanup confirms unbind before retire; direct operator retirement keeps any unconfirmed broker cleanup as a durable obligation. Retention is a separate explicit operator operation with no confidentiality guarantee (§5.4).
- Receiver-owned `effect.observe` answers map exactly to the dispatcher's four observation classes; missing/expired evidence stays unknown, and effect absence never authorizes an automatic new create (§§4.5–4.6, 7).
- An orchestrator composes child plans from its own Curator configuration in its agent home; the child executor only verifies them. Child profile/environment access and narrowed-grant constraints remain open (§§5.2, 16).
- For the gated SH2+ backend, the owner's admitted start may carry session lifecycle authorisation. The configured host uses a session-scoped host admission permit through `host_start` and `host_stop`; every physical start retains its own claim/fence/receipt interlock. Successors require consent, unchanged plan, active owner ancestry, fresh authority and confirmed previous absence. Only the dispatcher invokes the root launcher; registration/readiness precede delivery (§12). Formats remain unpublished.
- With keeper integration enabled, the dispatcher revokes only identities it registered with the one-shot flag, in a separate operation after account retirement. Standing identities remain operator-only, with a signed revocation record (§14).
- The daemon sweeps internally every minute (§7.2). Recovery controls become reachable after ownership, compatibility, replay and authentication; forward admission requires complete classification/accounting and confinement of unresolved effects. Unknown records retain claims; unbounded shared uncertainty closes the node. An absent daemon refuses with no command fallback. Operator maintenance closes admission, inventories executions and explicitly stops selected executions through `exec.stop`; confirmed absence and reconciliation precede mode/schema/binary change. Its deadline bounds the attempt, never standing runtime; standing allocations and identities are retained (§11).
- Cross-service calls never hold the state-transition lock; durable per-record in-flight markers, cancel intent and daemon/controller fences protect late results (§4.1). The user manager §7 owns the shared process fate table.
- E-rebind renews credential authority on the same account without widening scope; status exposes allocation and node horizons. The sweep renews ahead only with supplied authorised chains/consent; standing re-grants require the signed operator path, and no automatic standing delegation is published here (§§3.4, 7.2).
- An authenticated or operator-controlled time source, durable watermark and boot-inclusive monotonic elapsed gate prevent clock steps from reviving authority or triggering destructive expiry (§§3.4, 5.4, 11).
- State loss/restore requires an external fence, independent receiver coverage and reverse accounts/bindings/receipts inventory; orphan decisions are operator-signed, never automatic (§11.1). Generations are fresh random 128-bit values owned by the user manager.
- Admission class is recorded at registration/admission, never inferred from names. Every workplace is untrusted-class unless the operator admits its remote worker to the trusted circle by an operator act at admission; membership enables only donor-side opt-ins and every host boundary stays. Slice 0 and user-manager v0 refuse the untrusted class (code unpublished). Admission requires the host’s qualified capability matrix and class minimums, exec profile, socket/deployment evidence and no shared services beside untrusted work; no OS is excluded by name (R-UNTRUSTED, R-CEILINGS, R-SOCKETS, R-SHARED).
- Protected machine policy selects one exec-profile identity/body digest from the admitted Curator profile/lock identity and placement/class minima; the dispatcher pins it at prepare and the launcher verifies/applies it before child code. Caller references cannot authorize another profile; drift refuses on start/retry/recovery. The plan carries an OS-enforced task exec profile. Qualification covers executable, entrypoint, arguments, environment and transitive interpreted inputs; worker scripts/plugins/configuration cannot supply executable behavior to a local light check. Unprovable profiles refuse locally and use R-VERIFY. Agent accounts run editing and qualified light checks only; `agent-headless-build` is withdrawn. Builds/tests and execution of worker code run only in off-host isolated ephemeral runners without secrets, with results returned to the board (R-EXEC, R-VERIFY).
- Internet is an agent-loop tool: workplaces have zero egress and no proxy destination; trusted local agents reach only provider proxies. No `direct` or `external-http-proxy` can bind to any account on a platform host, trusted or untrusted; they and cooperative profiles stay for operator machines without the platform. Web tools are default-off, with explicit per-identity opt-ins for the trusted circle only. Tasks carry read-only resources with plan-covered digests; the worker asks for more by letter/outcome and never fetches. Web/resource formats remain unpublished. N15 revision of 2026-10-13 owns one listener per `account_generation`, bound to `profile_ref` and only its own admitted port, with `served_policy_digest` and `service_generation` evidence. The independent static UID-range deny applies from v0, with active precedence qualification (R-NET-TOOL, R-RANGE-DENY).
- Standing host-local loops require trusted class and local agent placement; remote standing placements remain workplaces with donor-side loops, including circle members. Circle membership alone grants neither host-loop nor review-role authority; reviewers need positive role/class/placement admission and producer exclusion (§3.6), while optional local-only-reviewer and requester-exclusion policies remain off and unchosen (§16 question 30). Trusted standing harness surfaces are digest-checked before launch/restart/resume; drift requires explicit re-materialization (R-STANDING).
- R-WATCH v0 / S0a offers affected-account journaled stop, admission hold and status read through `swarma-observer`; it promises no per-account egress freeze or phone notification. Containment never waits for delivery; v1 / S2 freeze ordering, release and versioned outcomes remain open. Manual R-ALLSTOP directly fans the same act to independent service intakes, issues containment and stops without waiting for acknowledgements, and fences root launch/create/ingress before absence. A never-launched generation needs fresh fenced absence and complete launch coverage; it never gets a dummy start or automatic retirement. Direct cuts/stops progress during dispatcher failure, but whole-act completion and reopening need every durable part including dispatcher reconciliation. Recovery explicitly names stopped/restart scope, clears hold last and retains ordinary authority gates ([§6](spec/dispatcher.md#6-holding-and-stopping), [§7.3](spec/dispatcher.md#73-usage-watcher-r-watch)).
- The machine-owned network binding is pinned at admission; caller network text is not authority. Dispatcher egress is deny-all under the qualified v1 service profile. Remote `workplace` allocations need helper-v1 enforced networking and a service-principal `swarma-rb-exec`, with their backend/wires still gated (§§8.4, 12–14).
- R-TRANSFER: the caller supplies exact B as a bounded archive with a canonical manifest derived from immutable B objects: path, supported Git kind/mode and content identity. Child-UID materialization verifies every archive entry, including missing/extra paths, before work; board import independently recomputes that manifest from repository B using the same encoding. Export compares mode and content, including mode-only edits, regular↔symlink transitions and prior-mode deletions, with pinned exporter/caps and a first-excess stop. Successive round ends within one work execution advance the existing `round`; retries keep it and the final export is the last round. DRAFT [`swarma-dispatcher workspace-result/1`](schemas/workspace-result-1.schema.json) keeps its field set and closed refusal vocabulary ([§5.2.1](spec/dispatcher.md#521-draft-swarma-dispatcher-workspace-result1)); qualified source permission failures have a separate unpublished typed-failure disposition, while infrastructure uncertainty stays pending. Complete-content custody, cleanup consent, repair/linkage, round-end/blob transport, release/retention and cap encodings remain publication work ([§16 question 22](spec/dispatcher.md#16-open-questions)). The board owns import, candidate, exact-commit review and integration; dispatcher holds no repository (§5.2). Review first consumes inert exact-C diffs/blobs outside harness discovery roots; discovery-capable tree entry requires a qualified strict-context adapter, and acceptance never admits candidate control surfaces. Rework recomputes cumulative sensitive/provenance obligations against a trusted integration baseline, distinct from materialization B and verification H, with fresh exact-C acknowledgements. Workplace ingress/relay/start/export/reconnect lifetime and the non-agent external-effect performer are open requirements for stage A and stage T contract packages respectively (§16 questions 31–32); no mechanism or topology is selected.
- After any crash, the dispatcher reconciles with the helper, broker and process evidence under the forward-admission rules above; it follows receiver-owned outcome tables, keeping unfinished stop/retirement work pending under its original effect ID.

## Diagrams

These PlantUML sources describe the draft service design and label later SH2+ gates; they do not claim implementation. Each figure links its defining spec sections and marks unspecified details. The historical operator-enrolled initial-slice note in the bring-up-team figure is superseded by the [purpose-6 bootstrap gates](spec/dispatcher.md#33-caller-keys-and-request-signatures); it grants no current bootstrap availability.

| Audit row | Figures | Spec |
|---|---|---|
| M-DP1 | [Components](diagrams/components.puml), [dispatch lifecycle](diagrams/dispatch-lifecycle.puml) | [Versions](spec/dispatcher.md#2-versions), [lifecycle](spec/dispatcher.md#5-lifecycle) |
| M-DP2 | [Run states](diagrams/run-states.puml), [agent and claim states](diagrams/agent-claim-states.puml) | [Records](spec/dispatcher.md#4-records-and-state), [capacity](spec/dispatcher.md#9-capacity) |
| M-DP3 | [Storage layout](diagrams/storage-layout.puml) | [State directory](spec/dispatcher.md#41-state-directory), [socket](spec/dispatcher.md#83-socket-dispatch1) |

Companion views: [bring up team](diagrams/bring-up-team.puml) ([§5.5](spec/dispatcher.md#55-bring-up-team-specified-but-unpublished)), [reconciliation](diagrams/reconcile.puml) ([§7.1](spec/dispatcher.md#71-reconciliation)), [service daemon / gated SH2+ backend](diagrams/v1-daemon.puml) ([§12](spec/dispatcher.md#12-session-host-backend-later-activation-gate)), [workspace transfer](diagrams/workspace-transfer.puml) ([§5.2](spec/dispatcher.md#52-orchestrator-allocations)) and [activation / maintenance / node-wide stop](diagrams/activation.puml) ([§11](spec/dispatcher.md#11-installation-and-activation)).

## Linux transport tools

The [reference contract](spec/transport.md) is implemented in `internal/transport`,
with `cmd/swarma-dispatcher` and `cmd/swarma-capacity`. Both refuse unsupported
platforms. They require one explicit `--config /absolute/file.json`; there are no
environment defaults, endpoint overrides, account lookup or config reloads.
The daemon's optional `--m0-config` flag is the gated M0 extension described
below; without it the daemon is exactly this reference subset.
The separate closed server/client schemas, root ownership/modes, numeric pins,
caller UID non-reuse and fixed `/opt/swarma/run/dispatcher/dispatcher.sock` path
are normative in the contract. This repository does not provision them. The
transport never uses the admission reducer to advertise readiness or availability;
every valid capacity request returns `capacity:hold`. No launch, journal,
accounting, signed authority or recovery-control operation is implemented in this transport.

On an admitted isolated Linux runner, build and test with the pinned CI toolchain:

```sh
go build ./...
go vet ./...
go test -v -p 1 -count=1 -timeout=20m ./...
```

Build/run entrypoints for a separately qualified deployment are:

```sh
go build -o .temp/swarma-dispatcher ./cmd/swarma-dispatcher
go build -o .temp/swarma-capacity ./cmd/swarma-capacity
.temp/swarma-dispatcher --config /absolute/server.json
.temp/swarma-capacity --config /absolute/client.json
```

These commands do not authorize installation or execution on an orchestration
host. Daemon diagnostics go to stderr; it produces no readiness, journal or
accounting output. The client prints the exact capacity-refusal JSON and exits 2
after a valid hold exchange; local/config/protocol failure exits 1 with stderr
diagnostics. It makes one attempt and no automatic retry. SIGINT/SIGTERM requests
idempotent daemon shutdown; successful owned cleanup exits 0, while cleanup,
ownership or budget failure exits 1. Nothing launches agents.

The daemon sets startup umask `0177`, acquires its stable `flock`, refuses every
pre-existing endpoint, binds exactly `0600`, changes GID **before** mode `0660`,
and listens only after identity checks. Internal descriptor-relative Unix paths
anchor bind/connect to the validated directory without an endpoint override.
All socket reads use nonblocking `recvmsg` through the Go runtime poller; sends
use `MSG_NOSIGNAL`. Generic `frame.Read` is not an ancillary guard and is not used
on sockets. Product code never sweeps inherited descriptors or adopts a listener;
std-FD-only exec is a deployment requirement.

After unclean death there is no automatic restart or stale-socket removal.
The contract's operator filesystem procedure requires the verified lifetime
lock, exact socket/directory identity and an `ECONNREFUSED` probe. The three
residual states are `(service_gid, 0600)`, `(connect_gid, 0600)` and
`(connect_gid, 0660)` for this implementation's bind/GID/mode order. Other
GID/mode/owner/type states refuse; cleanup is not a wire operation and unknown,
live or foreign paths stay untouched.

Linux tests use real unprivileged sockets, kernel peer credentials, received
rights, local owned filesystem fixtures and narrowly injected publication/receive
errors. Fixture paths go under the checkout's ignored `.temp/`; output goes to
the hosted test log. Mechanism tests constructing internal prevalidated data do
**not** prove protected configuration, distinct caller/service UIDs or complete
CLI deployment. The root-owned fixed endpoint and separate UID/group enrollment
need an admitted isolated deployment fixture; no such qualification is supplied
here and no production bypass flag exists. Config/pin filesystems must be local
and trusted; userspace deadlines cannot bound a hung kernel/filesystem operation.

## Local journal checks

On an admitted isolated Linux runner, the component checks are:

```sh
go test -v -count=1 -timeout=20m ./internal/journal/
go vet ./internal/journal/
gofmt -l internal/journal/
```

The package has no CLI or deployment entrypoint. Tests use temporary filesystem
fixtures and child test processes; results go to the test/CI log. Real storage
controls require ext4 or XFS and print an explicit skip reason otherwise. Pure
codec and deterministic fault tests still run; substituted filesystem metadata
in fault tests is not durability qualification. Root-only foreign-UID cases,
reboot/writeback durability and deployment recovery enforcement are unqualified.

## M0 runtime (draft, non-activatable)

[`internal/m0`](internal/m0/) is a DRAFT implementation of the
[M0 existing-worker candidate](spec/m0-launch.md) against its draft
specification: the strict bounded CCJ-1 canonicalizer, the closed dispatch and
owned-cancel forms with their finite bounds and Ed25519 preimage verification,
the closed policy and signing-plan forms, the executor-entry /1 composition and
`run-codex` argv mapping as dispatcher-side witnesses, the durable one-launch
ledger on [`internal/journal`](internal/journal/) with established-recovery
replay, the receiver interlock A commit gate, owned cancel with the original
stop obligations, and the daemon activation gate. Nothing is deployed, installed
or enabled by default. Every dependency on an unpublished slice is a seam whose
default refuses by name: the user-launcher intake (S1), the broker Claude runner
argv (S3a), the qualified clock, the protected generation reader, the protected
policy source and the catalog input body. See the
[package documentation](internal/m0/doc.go).

The daemon gains one optional flag. Without it nothing M0 is loaded and the
wire behavior is the reference subset exactly; with it the M0 extension attaches
only when the configuration selects M0 **and** the policy source reports an
installed policy. With the default seams that is never the case, so the daemon
prints `m0 closed` and stays capacity-only:

```sh
.temp/swarma-dispatcher --config /absolute/server.json --m0-config /absolute/m0.json
```

The M0 configuration is a separate protected server-mode file (same ownership
and mode rules as the server configuration) holding exactly the canonical
object `{"callers","enabled","entry","kind","recovery_attested","state_dir","supported_local_storage","v"}`
with `kind` `swarma-dispatcher m0-config/2`, `v=2`, `entry` `initialize` or
`established`, and an absolute `state_dir` that already contains the journal
`records` and `inputs` directories. The transport server configuration is
unchanged. `recovery_attested` is the operator's recovery-prerequisite
attestation for the journal; without it an established reopen refuses
`m0_recovery_held`. A journal sync failure makes the daemon exit and must not be
reopened in the same boot.
The protected `callers` list contains 1..4 ascending managed UID/directory pins admitted only to M0 frames while the extension is attached, with the same pin rechecks and slot limits; configuration /1 now refuses.
Membership grants no M0 authority, current-generation checks remain in M0, and default seams still keep attachment closed.

On an admitted isolated Linux runner:

```sh
go test -v -count=1 -timeout=20m ./internal/m0/
go test -race -count=1 ./internal/m0/ ./internal/transport/ ./internal/journal/
go vet ./internal/m0/
gofmt -l internal/m0/
```

Tests use the synthetic fixtures under `fixtures/m0-launch/` and
`fixtures/m0-executor-entry/`, re-keyed to a per-test Ed25519 key, in-memory
fakes for every seam, an in-memory ledger for the platform-neutral runtime
tests and real `t.TempDir()` journals (ext4/XFS, otherwise an explicit skip)
for the Linux recovery tests. Results go to the test log. Nothing here
qualifies a receiver, a signer, protected installation, native timing or an
activation; the `m0_*` refusal codes are draft spellings pending publication.

## M0 caller tool (draft)

| Tool | Source | Purpose | Exit status |
| --- | --- | --- | --- |
| `swarma-m0-caller` | `cmd/swarma-m0-caller`, [`internal/m0client`](internal/m0client/) | Sign, store and send an installed M0 `dispatch` or `cancel` signing plan, or `resend` a stored request | `0` accepted, `16` refused by the daemon, `17` unknown after request-frame bytes, `64` request not sent / local refusal |

The caller side of the [M0 request form](spec/m0-launch.md#2-closed-proposed-forms-and-signing),
under the [first-run local caller-key cut](https://github.com/relux-works/swarma-dispatcher/blob/ce3b2bdcb26818e8b58b5995bf7d4ddd2e652e80/spec/m0-launch.md#first-run-static-cut-local-caller-key-proposed).
That cut names the departures from purpose-6 association and Keymaker signing/custody;
it does not relax R, auth or the receiver's checks. It reads one closed, canonical ASCII JSON configuration (maximum 4096 bytes),
using the [transport client](spec/transport.md) member names: `v` (`1`),
`caller` (exactly `uid`, `dir`, `dev`, `ino`), `endpoint_dev`, `endpoint_ino`,
`service_uid`, `service_gid`, `connect_gid`; and the tool members `key_file`,
`state_dir`, `dispatch_plan`, `cancel_plan`, `stage_timeout_ms` (1..2000),
`session_timeout_ms` (1..5000). Every member is required, keys are sorted, and
there is no whitespace outside strings, escape or trailing newline. Configuration
is root-owned, group root, single-link and mode `0444`, opened through pinned,
root-owned, non-group/other-writable ancestors without symlinks. The endpoint is
fixed at `/opt/swarma/run/dispatcher/dispatcher.sock`. Caller and endpoint pins
are checked before connect and rechecked before hello; the service's kernel UID
is checked before any write. The private service group must not include the caller.
The client keeps its own closed parser because the transport parser is private
and currently excludes managed callers; it does not change the transport package.

`dispatch_plan` and `cancel_plan` are absolute protected file paths: regular,
non-symlink, root- or effective-UID-owned and not group/other-writable, with
root/effective-UID-owned, non-group/other-writable ancestors. The daemon's `DecodeSigningPlan`
is the decoder. The request is exactly the installed signing plan's canonical R,
including its `request_id` and cancel target; the tool generates no request values.
Before key access or storage, the effective UID must equal both `caller.uid` and
the plan's `association.uid` (the real UID must also equal `caller.uid`).
The key file is a 32-byte Ed25519 seed: regular, one link, effective-UID-owned,
mode exactly `0600`; its public key must equal the plan's `association.key`.
Its parent must be caller-owned, mode exactly `0700`, through non-symlink
root/caller-owned ancestors not writable by group or others. The tool opens the
key with `openat` and `O_NOFOLLOW` through that pinned parent, then rechecks the
file and ancestors after reading and before using the seed. The shared snapshot
check compares descriptor identity, mode, link count, size, mtime and ctime and
the name under the pinned parent. Directory identity excludes link count and
times: a transient directory change restored before recheck is not detected,
nor is a change after the final check.

Before opening the key the tool disables dumpability and sets both core limits
to zero, refusing if either syscall fails. It clears its local seed buffer and
expanded key on a best-effort basis; copies inside the standard library are not
cleared. It verifies the signed wire with the daemon decoder before storage.
Installation must keep the key outside every worker home, workspace, export and
retention path, including aliases or bind mounts into those trees, and exclude
it from backups; these are installer obligations, not tool checks. The key must
never enter argv, the environment, logs, a journal, backup or export. The installer
also enforces distinct caller/worker accounts and group separation, obtains consent
and disposes of the key after terminal cleanup, as the cut requires. The contract
requires dispatcher statement/window checks: dispatch must close at the statement's
`not_after`; owned cancel skips only that window check. That dispatcher integration
is separate work; this client adds no timestamp or local clock authority. The tool never creates, repairs, prints or copies a key. Canonical
request bytes and spellings come from `internal/m0`, not a second canonicalizer.

The exact signed wire bytes are stored in `state_dir/<request_id>.req`
(caller-owned directory exactly `0700`, file `0600`, both synced) before the single
exchange. The state directory is pinned through the same protected ancestor walk
as the key parent; it is never created or repaired. A second
submission of that plan refuses locally and says to use `resend`; it never
replaces an existing file. `resend` verifies the stored signature and exact R
against the current operation plan, then sends the stored bytes unchanged
without reading the key file. Linux only.

```sh
go build -o .temp/swarma-m0-caller ./cmd/swarma-m0-caller
.temp/swarma-m0-caller dispatch --config /absolute/caller.json
.temp/swarma-m0-caller cancel --config /absolute/caller.json
.temp/swarma-m0-caller resend --config /absolute/caller.json --request-id ID
go test -race -count=1 ./internal/m0client/... ./cmd/swarma-m0-caller/...
```

Stdout carries exactly the reply envelope bytes; diagnostics go to stderr. On
`17` the `request_id` is printed on stderr for `resend`; there is no retry
inside one invocation. A local transport refusal leaves the durable request and
reports `request_id=... stored, not sent`. Connect or hello failures, including
both close-before-hello-write and close-after-hello-write, are exit `64` while
the request frame is unwritten; hello failures have the fixed reason `hello_failed`.
Exit `17` requires at least one byte of the request frame, including its length
prefix, to have been written; hello bytes do not count. This tool activates nothing
and qualifies nothing: the
request is the installed signing plan, and the caller must be on the daemon's
managed M0 caller list. Managed-caller transport admission is separate work.
Tests drive the real M0 runtime/extension behind a test Unix-socket listener,
modeling root ownership and the managed caller UID through an unexported test
boundary. Proof with a real kernel caller peer belongs to the transport's
real-peer control ([PR #31](https://github.com/relux-works/swarma-dispatcher/pull/31));
an end-to-end test of this tool against it follows once that change has landed,
so these tests do not qualify the real serve loop or a deployed daemon.

## P17 launcher clients (Linux, standalone, not wired)

`internal/p17/` holds the launcher-side clients of the P17 hook interface, in
which the dispatcher is launcher L: `codes` (the closed code set and D4 exit
classes), `isolation` (H0), `source` (H1 and the validate step of H2),
`keymaker` (`j7_run_bind`, `j7_run_end`), `runrecord` (the durable run record
on `internal/journal`, with the H6 table), `incarnation` (D2 kernel
incarnations from procfs), `sweep` (the D5a zero-process proof, D5b kill by
uid and lease quarantine), `hygiene` (the H3 descriptor/argv/env assertion)
and `launcher` (the composition the M0 launch path calls). `fakes` is test
support only. No daemon command uses these packages yet.

Explicit `source.Pinned{}` and `keymaker.Pinned{}` adapters use a2a-swarm
`runorigin` public values and swarma-keymaker `j7lifecycle` public framing and
codec respectively, at the exact versions in `go.mod`. Nil wires still select
`Unpinned` and refuse by name. The helper (spawn and sweep), authoritative
generation reader, work-product export and operator release seams also refuse
by default. The Source adapter implements its documented closed protocol
without importing any upstream internal package. The packages follow the P17
hook interface text r2.13 (sha256
`6849a4edc98b8aa4b257498f5c5c1efa9d5b25d09187474ee7ef284c89eb607a`).

The native Keymaker hello is negotiated eagerly at connect, before the durable
binding record. A connect or hello failure is `run_bind_unavailable` with no
run record, and the next attempt starts from a fresh Source prepare. A
successful bind must become durably bound before input can be released. The
no-effect refusal table is empty until the pinned Keymaker registry explicitly
publishes it: correlation and nonretryability alone never make a refusal
terminal. All such native bind refusals currently remain HELD, exit 17, with
no input.

The Keymaker wire carries one request at a time, so no second bind is written
while a reply is owed. A local timeout closes the connection and the run is
HELD. The bind is written at most `keymaker.BindAttemptLimit` (3, a fixed
constant) times: the exact retry, with the same request ID and body on the
original assigned connection, follows only a read, correlated, retryable
refusal. A null, malformed or wrong-ID response is never retried, and at the
limit the run is HELD. There is no bind on a fresh connection.

A uid leased before any run record exists returns to the pool on a failure
only when the privilege-drop helper was never invoked for that lease and the
zero-process proof succeeds on a qualified procfs view; otherwise it is
quarantined. The input principal's uid must equal the leased uid.

A uid leaves quarantine only with the zero-process proof AND (operator release
OR (acknowledged end AND work-product export)). The export seam reports exactly
`exported`, explicit `no_working_copy`, or `not_proved`. The first two satisfy
the export obligation; missing, zero-value, unknown, partial or failed evidence
is not proved and keeps the uid quarantined. The seam reports evidence only:
it offers no operation to copy or snapshot a home directory. Home-operation
requests cannot resolve through this closed, report-only API. Homes are never
archived; preserving a home is a separate, explicit operator operation outside
retirement. A qualified export producer must run a fixed program under the agent
account to export the work product before home removal. This slice neither
removes homes nor authorizes removal on partial, unknown or failed evidence.
Operator release never bypasses the zero-process proof.

Source generations are nonzero numeric values; root launcher input generations
must therefore be canonical positive decimal strings. The native Keymaker
client itself preserves opaque generation strings. The launcher retains the
original validated origin bytes; the native codec encodes their exact typed
values into its canonical frame without changing any origin fact.

```sh
go test -v -count=1 -timeout=20m ./internal/p17/...
go vet ./internal/p17/...
gofmt -l internal/p17/
go test -run 'PinnedVectors|Corpus|PinnedRefusalPolicy' ./internal/p17/keymaker/
```

Tests are hermetic: fake Source and Keymaker servers over socket pairs, a
fake procfs tree, temporary journal fixtures. Journal-backed tests print an
explicit skip reason on filesystems other than ext4/XFS. The pinned corpus is
an unchanged byte copy under `keymaker/testdata/`, always checked against its
pinned size and SHA-256; its digest test never skips. Results go to the test/CI log. Nothing here qualifies a deployment, a helper or a real Source
or Keymaker.

## Demo tools (not production)

The separate [demo contract](spec/demo-launch.md) defines protected configuration,
roster, irreversible attempt markers, receipt observations and bounded helper
stops. No production flag enables it. Both commands refuse non-Linux platforms.
These are build/entrypoint references for a separately authorized isolated demo,
not an installation or real-account execution instruction:

```sh
go build -o .temp/swarma-demo-dispatcher ./cmd/swarma-demo-dispatcher
go build -o .temp/swarma-demo-client ./cmd/swarma-demo-client
.temp/swarma-demo-dispatcher --config /absolute/demo-server.json
.temp/swarma-demo-client --config /absolute/demo-client.json launch < launch-request.json
.temp/swarma-demo-client --config /absolute/demo-client.json stop < stop-request.json
.temp/swarma-demo-client --config /absolute/demo-client.json status < status-request.json
```

The demo client's stdin intake preserves shared descriptor flags. Socket reads
use per-call nonblocking receive; pipes and terminals retain bounded polling.
A terminal hangup refuses with `input/output error` (EIO), not a clean EOF.
Only actual regular files use the synchronous redirected-file path.

The client prints one JSON result with no automatic retry. `receipt-seen` proves
only receipt observation; `unknown` and `unresolved` retain uncertainty. A client
exit of zero means a reply was received, not absence or readiness. Daemon errors
go to stderr. Attempt/stop/closed records and capped output logs remain in the
protected configured state directory; nothing deletes, renames or truncates them.
Fixture child processes and sockets use checkout-local `.temp/` allocations;
Linux CI emits test results to its hosted job log. Fixtures require non-group-writable ancestors (including the checkout path);
they do not repair those permissions. Tests require neither root,
sudo nor provisioned `/run`, `/etc` or `/var` paths. They do not qualify the real
launcher, helper, process identity, sudo policy or account-wide absence.

## Tools and CI

The `go` job in [`.github/workflows/linux-ci.yml`](.github/workflows/linux-ci.yml)
runs on Linux (`ubuntu-24.04`) for pull requests and pushes to `main`, with
read-only repository contents permission. Checkout and setup-go are pinned to
commit IDs; checkout does not persist credentials. Go is pinned to `1.26.10`
with `GOTOOLCHAIN=local`, `GOMAXPROCS=2`, and module caching disabled.

The job installs CI tools in the runner's temporary directory:

```sh
python3 -m venv "$RUNNER_TEMP/ci-python"
"$RUNNER_TEMP/ci-python/bin/python3" -m pip install jsonschema==4.25.1 PyYAML==6.0.2
mkdir -p "$RUNNER_TEMP/ci-bin"
GOBIN="$RUNNER_TEMP/ci-bin" go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
```

It adds those `bin` directories to `PATH`, then runs these entrypoints:

- `actionlint -shellcheck= -pyflakes= .github/workflows/linux-ci.yml` checks
  workflow syntax and action expressions. Optional ShellCheck and Pyflakes
  integrations are disabled; this is not a complete shell/Python linter.
- The workflow's `schemas` step parses every `schemas/**/*.schema.json` with
  Python `json.loads` and calls `jsonschema.Draft202012Validator.check_schema`.
  Invalid JSON, an invalid meta-schema shape, or no schemas fails the job.
  This does not validate result instances, cross-service semantics, the prose
  specification, or deployment readiness.
- The `source` step refuses any `*.go` file or nested `go.mod` anywhere below the
  checkout (including symlinks, excluding `.git`) without a root `go.mod`.
  When neither exists, it explicitly reports **spec-only bootstrap**, with
  dispatcher build/vet/formatting/tests
  unrun. Installing Go to run CI tooling does not implement the dispatcher.
- The root `go.mod` declares Go `1.26.10`. The P17 clients use the exactly pinned
  `github.com/relux-works/a2a-swarm/runorigin` and
  `github.com/relux-works/swarma-keymaker/j7lifecycle` public packages; other code
  uses the standard library.
  With that module present, the same step runs, in order, under Bash with
  `-e -o pipefail`:

  ```sh
  go mod verify
  go build ./...
  go vet ./...
  out="$(gofmt -l .)"
  if [[ -n "$out" ]]; then
    printf '%s\n' "$out"
  fi
  test -z "$out"
  go test -v -p 1 -count=1 -timeout=20m ./...
  ```

  Each error stops the step. The module must use this pinned toolchain or
  deliberately update CI. The commands target the root module and active Linux
  build tags; nested modules, other platforms/tags, race detection, privileged
  account fixtures, runtime activation and integration qualification are outside
  this bootstrap's coverage. `go mod verify` checks downloaded module contents;
  it does not replace dependency policy or guarantee an unchanged `go.sum`.
  Frame tests in `internal/frame/frame_test.go` cover wire bytes, length gates,
  fragmented/truncated reads, writer failures and consecutive frame boundaries.
  These checks require execution on the isolated Linux runner; implementation
  and local formatting inspection alone are not passing build/vet/test evidence
  or native service qualification. Results go to the hosted job output.
- `python3 .scripts/test_linux_ci.py -v` qualifies the CI blocks using temporary,
  dependency-free Go modules, schema/workflow fixtures, and narrowing mutations.
  These are CI regression tests, not dispatcher implementation tests. Python
  orchestrates the real YAML entrypoints and meta-schema validator without adding
  a placeholder Go package. Run this command only on an isolated runner; fixtures
  execute real Go builds and tests. Logs go to the job output, fixtures are
  removed after each test, and the whole job has a 30-minute timeout.

Documentation inspection uses these additional entrypoints within this toolset:

- Git: `git diff --check` checks whitespace; `git diff -- README.md spec/dispatcher.md UNRESOLVED_QUESTIONS.md` reviews the bounded public delta. Include an untracked unresolved index separately when present.
- ripgrep: `rg -n 'purpose|bootstrap|standalone|qualification' README.md spec/dispatcher.md UNRESOLVED_QUESTIONS.md` locates affected gates for cross-reference review.
- Python 3 standard library: `python3 -` runs bounded checks supplied on stdin for local Markdown targets/heading anchors, table structure and public-diff hygiene.

These documentation checks do not qualify a service or activate an API; they are not project builds or tests. Output goes to stdout; private inspection scripts, logs and evidence belong under `.temp/`.

For the [M0 DRAFT fixtures](fixtures/m0-launch/README.md), the same pinned
`jsonschema` version checks shapes without running worker code:

```sh
python3 - <<'PY'
import json
from pathlib import Path
from jsonschema import Draft202012Validator

validators = {}
for name in ("request", "policy", "signing-plan"):
    schema = json.loads(Path(f"schemas/m0-{name}-1.schema.json").read_text())
    Draft202012Validator.check_schema(schema)
    validators[name] = Draft202012Validator(schema)
for path in sorted(Path("fixtures/m0-launch").glob("*.json")):
    name = "policy" if path.name.startswith("policy") else (
        "signing-plan" if path.name.startswith("signing") else "request")
    errors = list(validators[name].iter_errors(json.loads(path.read_text())))
    assert bool(errors) == (".invalid." in path.name), path
print("M0 shape fixtures passed; authority/runtime/activation unverified")
PY
```

Output goes to stdout. This does not check CCJ lexical rules, cryptographic
validity, cross-field/currentness/replay semantics or any native deployment gate.

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

### Demo mutation qualification

`.scripts/demo-launch-mutants.py` applies exact, public-clean plants only in a
copied checkout. It requires a disposable Linux host because a regression may
reach unknown paths. No mutation kill is established by source inspection:
manifest expectations require hosted execution. All named unmutated
controls run before any plant. Match/build errors and unexpected timeouts are
invalid, never kills. A kill additionally requires the named test failure and its
`expect` assertion regex inside that selected failing test’s RUN-to-FAIL output block; panic and runtime fatal errors are invalid. Accepted
coverage residues are listed explicitly and excluded from execution. Use a non-root user and non-group-writable ancestors:

```sh
python3 .scripts/demo-launch-mutants.py --disposable --fake-sudo "$DEMO_FAKE_SUDO" --fake-log "$DEMO_FAKE_SUDO_LOG" --scratch-root "$HOME" --logs "$HOME/demo-mutant-logs"
python3 .scripts/demo-launch-mutants.py --disposable --fake-sudo "$DEMO_FAKE_SUDO" --fake-log "$DEMO_FAKE_SUDO_LOG" --only S1-E04 --scratch-root "$HOME" --logs "$HOME/demo-one-mutant"
python3 -m unittest discover -s .scripts -p test_demo_launch_mutants.py
go test -race -p 1 -count=5 -timeout=20m ./...
```

The Python unit tests use no Go toolchain. The mutation runner writes per-plant
build/test logs under `--logs`; it never edits the checkout. An optional positive
integer `timeout_seconds` in an entry overrides `--timeout` for that entry’s test
process (including its unmutated control when run). S1-E28 uses 180 seconds so
both unchanged elapsed-budget assertions can report. A timeout is always `invalid`: a kill requires the test’s own matching assertion. Its manifest is
`.scripts/demo-launch-mutants.json`.

`TestDiskFullStopRecovery` runs only when `DEMOLAUNCH_ENOSPC_DIR` names an empty,
writable, disposable filesystem no larger than 16 MiB (for example a prepared
2 MiB tmpfs). The test fills it with real writes; it must not name a useful
filesystem. Otherwise it skips with the exact missing prerequisite. The mount
must have trusted, non-group-writable ancestors. Always-running torn-file tests
cover the recovery rule without this environment; they do not qualify ENOSPC.

```sh
DEMOLAUNCH_ENOSPC_DIR=/absolute/disposable-small-filesystem go test -v -p 1 -count=1 -run '^TestDiskFullStopRecovery$' ./internal/demolaunch
```

Before the mutation runner, the disposable-host owner must create a private mount
namespace, bind-mount the committed executable `.scripts/demo-fake-sudo` over
every existing sudo path (including resolved alternatives), and drop to the
unprivileged test user inside that namespace. Set `DEMO_FAKE_SUDO` to that same
inode and `DEMO_FAKE_SUDO_LOG` to an absolute writable JSONL path. The runner
checks bytes and device/inode for `/usr/bin/sudo`, `/usr/bin/sudo-rs`,
`/usr/bin/sudo.ws`, `/bin/sudo`, `/usr/local/bin/sudo`, `/usr/sbin/sudo` and PATH's sudo, then requires
one recorded self-test exiting 77. Before any subprocess it requires
`NoNewPrivs: 1` from `/proc/self/status`. It never sets up mounts or escalates privileges.
For example, in a root shell **on the disposable host only**, set absolute
checkout/log paths and the test user's numeric UID/GID, then enter the namespace:

```sh
export DEMO_FAKE_SUDO=/absolute/checkout/.scripts/demo-fake-sudo
export DEMO_FAKE_SUDO_LOG=/absolute/test-home/demo-sudo.jsonl
export DEMO_TEST_UID=1000 DEMO_TEST_GID=1000
unshare --mount --propagation private /bin/sh
# The following commands run inside the new namespace.
for candidate in /usr/bin/sudo /usr/bin/sudo-rs /usr/bin/sudo.ws /bin/sudo /usr/local/bin/sudo /usr/sbin/sudo "$(command -v sudo)"; do
    [ -e "$candidate" ] || continue
    mount --bind "$DEMO_FAKE_SUDO" "$(readlink -f "$candidate")" || exit 1
done
exec setpriv --no-new-privs --reuid="$DEMO_TEST_UID" --regid="$DEMO_TEST_GID" --init-groups /bin/sh
# Now run the mutation command above as the unprivileged user.
```

The log location must be writable by that user; no setuid executable remains
reachable at any checked sudo alias inside the namespace.

Each control/plant and the summary report `sudo_calls`; the fake never executes
its arguments. The runner exclusively creates a per-UID rendezvous directory
`/tmp/demo-launch-fake-<uid>` (0700) with a `log-path` pointer (0600, single link,
caller-owned) and a precreated `healthy` sentinel. The fake checks the pointer
even with an empty environment; it never depends on its parent surviving.
A recording failure removes the sentinel and exits 78. The runner checks it
after each control/plant and at exit, and fails rather than reporting a zero
call count. Removing the sentinel requires no log-file allocation on a full disk.
The directory is removed when the run exits; a stale directory refuses a new
run and needs owner inspection in the disposable namespace. Only one runner per
UID may use that namespace at a time. Do not run this on a shared host merely by passing
`--disposable`.

Fixture ancestors must not be group/other writable. Fixtures explicitly set
checked file modes after creation, including receipts; umasks 022 and 0077 are
supported test preconditions. Real launcher/helper processes must set their own
umask because the daemon supplies 0177.
