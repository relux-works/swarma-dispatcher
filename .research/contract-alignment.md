# R-DP1 contract alignment

Documentation-only review handoff. The scope is dispatcher prose and the three existing diagrams named by R-DP1; no runtime support or qualification is claimed.

## Authority

- [Contract table at 177cb554](https://github.com/relux-works/curator/blob/177cb554/.research/261010_platform-contract-table.md): canonical values and unresolved owner choices.
- [Docs audit at 48789217](https://github.com/relux-works/curator/blob/48789217/.research/261010_platform-docs-audit.md): section D1 R-DP1 and its six A-rows.
- Task precondition `owner-decisions-20261010-audit.md`: D-GOALS, D-IDENTITY, D-EXECUTOR, D-PATH, D-DONOR-PROVISIONING, D-WIRE-NAMES and D-RESUME. These newer decisions override the corresponding older open choices; they are not represented as bytes in the pinned contract-table commit.

## Audit alignment (6/6 R-DP1 rows)

| Audit row | Sections changed | Canonical value or explicit question |
|---|---|---|
| A05.6 | spec §3.3, §4.5; README principles; all three named diagrams | W1: Ed25519 over exactly `swarma-dispatcher request/1`, newline, CCJ-1 request without `auth`; principal/key/grant-subject checks preserved. D-WIRE-NAMES supersedes W2's historical retention baseline: repository-based signed domains, nothing shipped and no migration needed. Request signature and effect hash preimages stay distinct. |
| A09.6 | spec §1.2, §4.2–4.5, §5.3, §12, §14, §16; README; components/lifecycle/v1 diagrams | P1/P2/L2 and D-EXECUTOR: caller composes the immutable plan; `curator-run` under agent UID verifies/executes only, no profile resolution, broker-only network, first broker consumer. Launcher receipt precedes privilege drop; immutable plan on FD3, fresh agent broker connection. Intake/Decision 0021 amendment and credential qualification remain explicit launcher-owned activation gates. I2/I4 preserve allocation, dispatcher/board execution handles, generation and controller epochs. Ticket signature/host-role choice remains open: jointly publish/version and activate, or keep SH2 backend disabled (§16.3). |
| A09.7 | spec §1, §4.3, §5.1/§5.4, §7.2, §8.4, §14, §16; README; lifecycle/components diagrams | Dispatcher and board remain optional. Grant intersection/caller dependencies preserved. R1/R2 + D-IDENTITY: only identities registered by dispatcher with one-shot flag may be revoked by it, separately after account retirement; standing identity revocation is operator-only with signed record. Unknown cleanup stays pending. P7 open question (§16.1): admit operator-only `keep` in helper request schema, or remove it from policy; dispatcher requests remain archive/delete only. P11 internal service-only sweep is distinct from caller `reconcile`. |
| A09.8 | spec §2, §4.2–4.5, §5.3, §7.2, §8.3; README; all three named diagrams | N3: `swarma-dispatcher` binary/account in both command and daemon modes. I1: root caller correlation retained separately; six create/bind/launch/stop/unbind/retire effect IDs are distinct, retries reuse the same ID, exact existing SHA-256 derivation and no added newline. Symbolic table does not mint literal IDs. Separate provision/start root requests are distinguished from one combined dispatch. P11: v0 internal service-account sweep every minute outside public no-argument sudo path; v1 internal daemon sweep. |
| A09.9 | spec §2, §3.5 (existing configurable defaults retained), §5.1/§5.3, §9; README; all three named diagrams | Q1: temporary capacity closure refuses in v0 and queues in v1; quota/authority refusal never queues. Durable owned claims, self-exclusion and fail-closed hold retained; release requires confirmed end/absence. Q2 remains configurable: executions 2/caller and 6/node, allocations 4/caller and 16/node, allocation default 24 h/max 7 days, execution default 4 h/max 12 h. No numeric queue bounds invented. |
| A09.10 | spec §1.2, §5.3, §8.4, §12, §14, §16; README; all three named diagrams | N6/P1 + D-EXECUTOR settle narrow one-shot `curator-run` and shared launch library; published versioned intake remains required, no unsupported interactive capability is inferred. N5/P3: SH2 service owns metadata/logs/receipts/fences/input admission; agent-side `swarma-session-runner` owns PTY/per-session app-server and lease renewal. Local headless subset still refuses interactive/native resume/seeded/remote requests. SH2 backend and ticket gate remain future activation work. |

## Diagram alignment (3/3 R-DP1 diagrams)

| Existing diagram | Correction |
|---|---|
| `diagrams/components.puml` | Caller-side composition, narrow exec/first consumer, agent-side session runner and service split, version labels, internal sweep and separate root/effect keys; closed stdin requests through no-argument sudo. |
| `diagrams/dispatch-lifecycle.puml` | Root correlation versus symbolic effect keys; v0 refusal/v1 wait and recheck; separate provision/start roots; immutable plan/receipt-before-drop/fresh broker connection; confirmed retirement followed by applicable one-shot identity revocation. |
| `diagrams/v1-daemon.puml` | Canonical dispatcher binary name, capacity-only queue, separate controller epoch, SH2 service/runner ownership, ticket activation gate and internal sweep; default one-shot executor/first consumer. |

New P1 diagrams and the broader run-state/reconciliation diagram repairs remain R-DP2 work. No new figure was created or rendered here.

## Remaining questions and owner decisions

Spec §16 explicitly retains P7 retirement enum, W3 helper v2 format (component-specific format versus versioned shared adapter), I4 ticket/host-role publication, and G2 deferred self-goals (separate agent namespace versus caller/board-only authorship). No helper signed-domain grammar, ticket signed-domain grammar, socket basename, queue number or exact new intake schema is invented.

D-GOALS is reflected in §8.4: caller/board authorship for now, no agent goal capability, rationale and SH2+ deferral, with a board-free and goal-free host. D-PATH is reflected in §5.3 in its accepted order and bound to plan digest. D-DONOR-PROVISIONING is reflected in §16: separate component, variant B disabled pending specification. D-RESUME is reflected only in the later backend (§12): same major / same 0.x minor, caller handles `resume-failed`, no fallback or portable seed; current dispatcher subset still refuses resume.

## Verification and limits

| Command or review | Real exit code | Scope |
|---|---:|---|
| `git diff --check` | 0 | Standalone whitespace check after prose/diagram edits; repeated after evidence preparation. |
| `git diff --name-only` | 0 | Five tracked changes: README, dispatcher spec and exactly the three R-DP1 diagrams. The new alignment artifact is separately recorded by `git status --short`. |
| Historical `git diff --no-index --check /dev/null` for the alignment artifact (original filename omitted) | 1 (expected difference) | New artifact differs from the empty file; no whitespace diagnostic was printed. This non-zero command is not a passing gate. |
| Historical `git diff --no-index --check /dev/null` for the logbook artifact (original filename omitted) | 1 (expected difference) | New artifact differs from the empty file; no whitespace diagnostic was printed. This non-zero command is not a passing gate. |
| `git status --short` | 0 | Five tracked documentation changes and two new task-scoped Markdown evidence files under `.research/`; no code changes or commits. |
| Manual source/diff review | Not a command gate | Six audit rows mapped to owning contract rows and precondition decisions; three diagrams reviewed as text. Coverage is 6/6 rows and 3/3 named source diagrams, not runtime coverage. |

No tests, builds, product lint suite, provider experiments or PlantUML rendering ran, as required by the explicit task brief. Historical audit/contract checks were read as provenance and are not adopted as current passing evidence. This docs-only scope uses a whitespace check and source review; generic code/tests/build checklist entries are replaced by applicable documentation entries rather than checked as fictitious passes. Work remains uncommitted for the orchestrator's integration.
