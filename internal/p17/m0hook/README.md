# P17 hooks on the M0 launch path (library only, not activated)

Hook text of record: **P17 Curator-side hook interface r2.16**, SHA256
`1ff91660ea77af7b0a826c6b8e2978e54770e2a67447f8f5e50bccc2dba922e1`.

`Hooked` decorates `m0.Launcher`. `New` runs H6 once before admitting any
Start. Every recovery error is returned together with readable reports and
prevents admission, including ordinary per-record HELD reports. A fresh
process/owner is required for another recovery; constructing another decorator
over the same live quarantine is not a recovery retry mechanism.

Start passes the retained event and exact worker principal to the concrete
`launcher.Launcher`. H0 enforces the worker/leased-uid equality, so there is no
redundant post-Begin uid guard here. Only a durable bound record permits the
hygiene assertion and single atomic Inner.Start. The child incarnation must
be valid and have the leased uid before recording it.

## Begin outcomes

| Typed outcome | M0 Start result | Retained evidence |
|---|---|---|
| Clean record-Create refusal | `Committed:false, nil` | `run_bind_unavailable`, no key |
| Local bind encoding refusal | `Committed:false, nil` | `run_origin_refused`, no key |
| Keymaker endpoint pin failure | `Committed:false, nil` | `run_bind_refused`, failed check, no key; no retry |
| Connect/hello transport failure | `Committed:false, nil` | `run_bind_unavailable`, no key |
| Certain terminal unsent | `Committed:false, nil` | `run_bind_unavailable`, actual record key |
| Certain terminal no-effect refusal | `Committed:false, nil` | `run_bind_refused`, actual record key |
| Attempted bind write or uncertain record update | nonnil error, no inner call | unknown/HELD, actual key when exposed |
| Named unpinned seam | `Committed:false, nil` | original unpinned error |
| Any must-exit failure | fatal error, HELD | permanent exit latch, original chain |

The production no-effect registry remains empty; the terminal refused fixture
is explicitly test-only. No native refusal code is certified by this package.

The decorator checks must-exit first. It obtains a record-bearing failure's
actual key with `errors.As` to `*launcher.BeginRecordError`, reads
`RecordUncertain()` **before** `RecordState()`, and maps a terminal state only
when certain and consistent with its typed code. Under uncertainty the state
is the **attempted** state, not durable truth. Such an outcome remains HELD,
even if the attempted state is unsent or refused. Pre-record failures expose
no key. No detail-string parsing, guessed nonce, request-ID substitution or
live-run Restart is used. Both directions of state/code agreement are required:
a terminal state without its proper code, or a terminal code on a nonterminal
record, is HELD unknown. For a genuinely pre-record definite error, the client
must preserve its wrap discipline: every post-record Begin failure carries
BeginRecordError. The converse cannot be inferred if the client omits that
wrapper; it remains a launcher-client contract to pin explicitly.

A certain unsent owes no end. Stop sends no end for it; H6 never adopts it.
Only the launcher applies the helper-never-invoked AND qualified zero-process
proof when returning its uid. A fresh effect performs a fresh H1; repeating
the original effect never binds again. A failed update may follow a committed
frame: this process remains uncertain/HELD, while replay follows the true
committed history. The decorator does not roll it back or override recovery.

## Failure cleanup and Stop

An ordinary pre-spawn or non-committed refusal is clean only if H5 was
acknowledged and durably recorded. A failed End is returned, not swallowed
into a nil-error refusal. Unknown End leaves `ending` fenced and quarantined;
only the next process's H6 retries it. Until that next-process restart pass
(the only end retry path; Stop never calls RetryEnd), every Stop returns the
retained unknown refusal, the dispatch stays unresolved at M0, the record stays
`ending` or `binding`, and the account stays held. The live Run pointer is
dropped on the first End, never called twice, and its first outcome remains
retained; dropping it never drops the refusal. A `binding` record still needs
operator recovery; H6 never guesses its run identity.
`Attempt.ChildStatusKnown` is explicitly false: M0 receipts supply no child
exit status, so End.Verdict.Status zero is not a known exit 0.

Stop first looks up execution ID and the exact worker tuple, not the distinct
stop effect ID. All matching candidates are locked in effect-ID order **before**
Inner.Stop, including ambiguity. Only an in-flight claim or a live/held run
counts toward ambiguity; terminal run-less refusals cannot block a live run.
A Stop that acquires a claimed effect before Start marks it stopped: Start
returns `ErrStopped` before even the event seam. Inner.Stop precedes H5.
A Stop that predates claim cannot fence a future unknown Start; M0's durable
commit/cancel gate owns that case. Unknown effects keep unlocked pass-through.
An ambiguous match returns `ErrStopAmbiguous`, retains it on every run-owning
candidate and chooses none for H5. Start requires nonempty effect and execution
IDs. Empty Stop identities are refused with `ErrEffectRequired` or
`ErrExecutionRequired` **after** Inner.Stop, with no H5.

Stop's deliberate startup/latch exception: non-reentrant calls always reach
Inner.Stop, even before successful restart or after the fatal latch, because
stopping a child
is never refused. It then returns the gate/fatal error and performs **no
launcher state I/O**. A sync-carrying Inner.Stop error also latches and skips
H5. Receipts and all available inner/end error causes are preserved.

An unproved committed start needs an emergency stop. `StopRequests` must
supply M0's own distinct stop-effect identity and request for the same
execution and worker. The default refuses by name. A missing/invalid identity
never sends a made-up stop; `Attempt.InnerStopAttempted == false` together
with `InnerStopErr` explicitly records that no inner stop was possible. The
returned error retains this cause, and the ordinary failure path relies on
H5's uid sweep. The full request is checked: identities, the first obligation's
event/detection, allowed unique sources, expiry against the original deadline,
nonnegative times and the earliest ceiling computed from the start's fixed
bounds. External event provenance remains the seam producer's assertion;
Start carries no cancel/removal event against which to cross-check it.
`Attempt.InnerStopReceipt` retains the emergency receipt; a non-absent receipt
returns `ErrStopUnconfirmed`, and its own error is also joined and retained.
This layer writes **no durable intent** for that emergency call. Before
activation, the daemon must authorize consumption of its stop effect outside
M0's journaled stop pass and ensure repeated consumption is safe.
Fatal errors instead preserve the held run and exit duty without further
launcher state I/O; a fatal duty is never a clean refusal.

The bounded diagnostic cache retains 256 terminal attempts; live and HELD
attempts are not evicted. A separate process-life seen set is never evicted:
one entry per distinct attempted effect until process exit. Diagnostic
eviction never allows a second Begin for an old effect. Duplicate suppression
is per process; across restarts it rests on M0's durable intent and its one
dispatch per ledger.

## Named limit: emergency stop after fatal duty

With a latched exit duty and an unpinned or failing stop-identity seam, no
emergency inner stop can be sent, and fatal duty forbids H5 and its uid sweep.
Nothing here stops that child until the next process's restart pass; no
identity is invented and no extra kill path is added. **Activation precondition:
the stop-identity seam must be pinned before this wiring is activated.** The
process owner must also enforce the fatal exit duty below.

## Daemon composition obligations

- Supply **two independent durable installation facts**: the quarantine
  ledger's and the journal's. `Deps.Installation` is required and must be the
  same boot fact passed as `launcher.Config.Installation` and to Start;
  zero/invalid values refuse. Equality is caller-asserted: this constructor
  cannot inspect the already-built launcher's private installation fact.
  This package neither infers nor persists it.
- Take the shared quarantine lifetime lock with
  `Quarantine.Start(directory, sweep.Fresh or sweep.Established)` **before**
  initializing/opening the record store. Immediately after the first
  successful Start, durably flip the external ledger fact to Established,
  **before any Begin**, independently of the journal's first successful
  Create. The active lifetime retains its original boot fact; the durable
  flip controls future boots. Failure to persist the flip must prevent runs.
  There is no in-tree flip or crash-window repair in this wiring. Never infer
  Fresh from a missing file, and never reuse the journal flip point for it.
- Share one exact record-store instance and one quarantine across launchers.
  Store decorators forward StateDirectory. Established Open receives the
  live quarantine owner token; Initialize remains lazy, so first Create's
  lock order is a caller duty too. Close the store before the quarantine,
  after all runs stop. No direct store mutation may bypass this order.
- Supply retained events, the restricted child interface, child incarnations
  and M0-owned emergency-stop requests. Their refusing defaults are named,
  not guessed from Entry or receipt bytes. Supply the qualified placement,
  helper, process view and release-evidence producers to the launcher.
- Select `keymaker.UnixDialer` explicitly for Keymaker; it is the only
  Keymaker dialer pinned to that service uid. Do not reuse Source's dialer or
  interpret hello announcements as peer authentication. No default selects
  a qualified service configuration.
- The process owner must observe `ExitRequired`/`ExitCause`, stop further
  state I/O, apply its permanent fatal latch and exit. Every fatal return
  satisfies `m0.MustExit` and wraps `ErrMustExit`. **The current M0 runtime
  records launcher errors as unknown outcomes; it does not propagate this
  latch to Runtime.ExitRequired.** Automatic runtime propagation needs a
  separate M0 change or explicitly composed process owner before activation.
  This library never calls process exit.

**Input-release waiver, beside the fatal-propagation waiver:** ReleaseInput
here is bookkeeping, **not an input gate**. Atomic Inner.Start already
received the full input and ran its commit gate before the incarnation is
recorded. A real gate requires separate spawn-held and release seams. Neither
the tests nor the durable flag prove actual input withholding in this
composition.

Seams run while the effect lock is held. A seam must not call Start, Stop or
Observe of the same wiring for the same run. Context-preserving Inner callbacks
fail fast with `ErrReentrant`; context-free evidence seams and callbacks that
discard the context must enforce the rule themselves. This is not goroutine
identity detection.

Observe is otherwise a pure pass-through. Nothing activates this package; the
daemon stays on `m0.DefaultSeams()`. No M0 frame, record, code or receipt is changed.
Tests use hermetic clients, qualified procfs fixtures, counting inner seams
and real ext4/XFS journals (with explicit filesystem skips if unqualified).
These fixtures do not qualify a deployment, live holder or privilege helper.
