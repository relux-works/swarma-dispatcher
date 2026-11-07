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
bounded feeder are proposed only. PATH narrowing and adapter refusal status 78
await owning selection. Typed catalog/installation, native qualification and
authenticated Result joins remain gates; no adapter runtime is supplied.

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
- The root `go.mod` declares Go `1.26.0` and uses only the standard library.
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
manifest expectations are **intended, parent runs it**. All named unmutated
controls run before any plant. Match/build errors and unexpected timeouts are
invalid, never kills. Use a non-root user and non-group-writable ancestors:

```sh
python3 .scripts/demo-launch-mutants.py --disposable --scratch-root "$HOME" --logs "$HOME/demo-mutant-logs"
python3 .scripts/demo-launch-mutants.py --disposable --only S1-E04 --scratch-root "$HOME" --logs "$HOME/demo-one-mutant"
python3 -m unittest discover -s .scripts -p test_demo_launch_mutants.py
go test -race -p 1 -count=5 -timeout=20m ./...
```

The Python unit tests use no Go toolchain. The mutation runner writes per-plant
build/test logs under `--logs`; it never edits the checkout. An optional positive
integer `timeout_seconds` in an entry overrides `--timeout` for that entry’s test
process (including its unmutated control when run). S1-E28 uses 180 seconds so
both unchanged elapsed-budget assertions can report. A timeout remains `invalid`
unless the entry explicitly sets `timeout_kills` with a reason. Its manifest is
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
