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
live-run Restart is used.

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
only the next process's H6 retries it. The live Run pointer is dropped on the
first End, never called twice, and its first outcome remains retained.
`Attempt.ChildStatusKnown` is explicitly false: M0 receipts supply no child
exit status, so End.Verdict.Status zero is not a known exit 0.

Stop first looks up execution ID and the exact worker tuple, not the distinct
stop effect ID. A unique known effect is locked **before** Inner.Stop, ordering
it with Start. Inner.Stop precedes H5. Unknown effects keep unlocked
pass-through. An ambiguous match returns `ErrStopAmbiguous`, retains it on
both candidates and chooses neither for H5. Start requires nonempty effect
and execution IDs. An empty Stop effect is refused with `ErrEffectRequired`
**after** Inner.Stop, with no H5.

Stop's deliberate startup/latch exception: it always calls Inner.Stop, even
before successful restart or after the fatal latch, because stopping a child
is never refused. It then returns the gate/fatal error and performs **no
launcher state I/O**. A sync-carrying Inner.Stop error also latches and skips
H5. Receipts and all available inner/end error causes are preserved.

An unproved committed start needs an emergency stop. `StopRequests` must
supply M0's own distinct stop-effect identity and request for the same
execution and worker. The default refuses by name. A missing/invalid identity
never sends a made-up stop; `Attempt.InnerStopAttempted == false` together
with `InnerStopErr` explicitly records that no inner stop was possible. The
returned error retains this cause, and the ordinary failure path relies on
H5's uid sweep. A supplied emergency stop's own error is joined and retained.
Fatal errors instead preserve the held run and exit duty without further
launcher state I/O; a fatal duty is never a clean refusal.

The bounded diagnostic cache retains 256 terminal attempts; live and HELD
attempts are not evicted. A separate process-life seen set is never evicted:
one entry per distinct attempted effect until process exit. Diagnostic
eviction never allows a second Begin for an old effect.

## Daemon composition obligations

- Supply **two independent durable installation facts**: the quarantine
  ledger's and the journal's. `Deps.Installation` is required and must be the
  same boot fact passed as `launcher.Config.Installation` and to Start;
  zero/invalid values refuse. This package neither infers nor persists it.
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

Observe is a pure pass-through. Nothing activates this package; the daemon
stays on `m0.DefaultSeams()`. No M0 frame, record, code or receipt is changed.
Tests use hermetic clients, qualified procfs fixtures, counting inner seams
and real ext4/XFS journals (with explicit filesystem skips if unqualified).
These fixtures do not qualify a deployment, live holder or privilege helper.
