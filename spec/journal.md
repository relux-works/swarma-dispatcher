# Local durable journal contract

**Status:** normative draft v0.2; implementation and independent acceptance are
pending. MUST/MUST NOT use RFC 2119 meanings; bare imperatives are requirements.
This narrows local storage for [dispatcher §4.1](dispatcher.md#41-state-directory),
not launch authority or a qualified service. Payloads are opaque transition
bytes; input/marker/epoch schemas and their semantic authority remain with their
owning contracts. Internal entry points and error classes below are not wires.

## Framing and bounds

One journal concatenates frames without padding or a file preamble. Integers
are unsigned, big-endian. Version 1 is:

| Byte offset | Length | Value |
| ---: | ---: | --- |
| 0 | 8 | ASCII `SDJRNL01` |
| 8 | 2 | Version, exactly 1 |
| 10 | 2 | Reserved flags, exactly 0 |
| 12 | 4 | Payload byte length N |
| 16 | 8 | Sequence |
| 24 | N | Exact payload bytes |
| 24 + N | 32 | Raw SHA-256 of the complete 24-byte header followed by payload |

N is 1..1,048,576 bytes; frame size is 56 + N, at most 1,048,632 bytes.
The file cap is 67,108,864 bytes (64 MiB). Sequence starts at 1, advances by
exactly 1, and never wraps uint64. Zero, duplicates and gaps are invalid. Check
header fields, checked arithmetic and remaining file capacity before body
allocation/read; reject an oversized file before replay. Never scan for a later
magic. No rotation, compaction, snapshot, preallocation extending file size or
migration is specified. File size equals frame bytes plus any incomplete tail.
These bounds are neither request/output quotas nor a control-space reserve.

Golden vector `F(1,0x61)`, 57 bytes; spaces separate fields, not stored bytes:

```text
53444a524e4c3031 0001 0000 00000001 0000000000000001 61
7cbca5bdd6e3f363ddc0daf2942196ceada4e8d7a329922773e755ad35a6dc83
```

Every file flush/sync here means a successful `fsync` of that file descriptor,
including size metadata; directory flush means successful directory `fsync`.
Neither userspace-buffer flush nor `fdatasync` substitutes in this version.

## Ownership, storage and entries

Installation (§11) supplies the protected state directory and protected ancestor
path; neither entry point creates directories. Open that directory once with
`O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC`; `fstat` must show a service-owned directory
with permission/special bits exactly 0700. Pin this descriptor for the session.
All state opens, renames and directory syncs use it, or verified descendant
directory descriptors, never re-resolve its root path. Descendants follow the
same directory checks, with no symlink traversal, absolute names or `..` escape.
Path replacement cannot redirect this session's writes; it does not prevent a
new owner accessing a replacement tree. Cross-session replacement remains
externally fenced maintenance/restore under §11.1, not a local-lock guarantee.

The deployment MUST explicitly establish supported local Linux backing (ext4
or XFS) with durable file/directory `fsync`, same-directory atomic rename and
cooperating `flock`. Without that precondition, refuse `unsupported-storage`.
Before creating state files, `fstatfs` on the pinned directory must return type
0xEF53 or 0x58465342 respectively; all other types, including network, memory,
FUSE and overlay types, are unsupported in this version. A type match is only
a compatibility gate, never proof of the precise backing filesystem,
durability, mount/device behavior or qualification. Unsupported
errors from required flock/file-sync/directory-sync operations (`EINVAL`,
`ENOLCK`, `ENOSYS`, `EOPNOTSUPP`/`ENOTSUP`) also refuse `unsupported-storage`;
if discovered after a write/sync attempt, poison as below. No weaker fallback.

Open every state file relative to the pinned directory with `O_NOFOLLOW` and
`O_CLOEXEC`. Immediately `fstat` lock/journal/record/temporary descriptors:
require regular file, link count 1, service UID ownership and permission/special
bits exactly 0600. Reject violations as `invalid-state`; do not repair mode,
owner or links implicitly. Directories have the distinct checks above.
Every state descriptor is close-on-exec. Forked children must close inherited
state descriptors before external code and perform no state I/O; they must not
retain, duplicate or pass them. CLOEXEC alone does not prevent fork inheritance.

| Entry | Directory | `lock` | `journal` |
| --- | --- | --- | --- |
| `initialize` | Must already exist and pass checks | `O_RDWR \| O_CREAT`, never exclusive, mode 0600; validate and take ownership | `O_RDWR \| O_CREAT \| O_EXCL`, mode 0600; any existing entry refuses `invalid-state` |
| `open-established` | Must already exist and pass checks | Same open/create rule; prior absence proves nothing | `O_RDWR`, no create/truncate/append flag; missing or empty refuses `missing-state` |

Choose the entry explicitly; never infer it from missing/empty files or switch
entries on failure. `initialize` is only for installation-established fresh
state, never an automatic response to lost history. Acquire nonblocking
exclusive Linux `flock` on the checked `lock` descriptor before opening journal
or accessing records. Contention refuses `ownership-contended` without journal
access. Hold it for the whole ownership session, through replay/append/record
I/O. Never unlink/replace the lock inode. Its creation supplies no absence proof.

Only the flock-holding process appends journal or replaces records. Supervisors
and other components submit transitions to it; there it checks the current
durable daemon-instance epoch under the state-transition lock (§§4.1, 6).
Stale writers are not adopted; supervisor log epoch obligations remain in §6.
The advisory lock excludes cooperating owners, not a writer that ignores it.

After exclusive journal creation, fsync journal then pinned directory before
using it. Only that same unbroken ownership session may accept it empty and
append its first frame. It cannot hand an empty store to another process as
established history. Crashed initialization leaving an empty or partial journal
cannot be automatically retried: initialize refuses the existing file;
established opening refuses missing/empty or fails replay on a partial file.
A complete valid but unacknowledged first frame can instead undergo established
recovery, conservatively retaining its intent. Emptiness, creation or a failed
initialization never certifies effect absence. Lost state follows
[§11.1](dispatcher.md#111-state-loss-and-restore-r-gen); no bytes are removed.

## Append, full write and state ordering

A usable session has completed initialization or established recovery. Track
its validated EOF and next sequence. Under the state-transition lock, first
`fstat` journal and require size equal to tracked EOF; mismatch is
`invalid-state`, terminates/poisons the session, and writes nothing. Then check
payload and projected frame size. `payload-invalid` (N outside bounds) and
`journal-full` (projected cap excess or no next sequence) are clean refusals:
write nothing, consume no sequence, leave a previously usable session usable.
A full journal does not become writable by deleting history or releasing debt.

Fix immutable payload/header/digest and perform one full-write attempt using
offset-explicit writes (no `O_APPEND`) at tracked EOF plus exactly the known
transferred count. Positive short progress without error continues on only the
remaining suffix. Raw syscall `EINTR` that guarantees zero bytes transferred may
be retried at that same offset, at most three consecutive retries per syscall;
positive progress resets the counter. This exception occurs before reporting a
full-write error. Any other error, any wrapper return carrying an error even
with a positive count, exhausted EINTR retries, uncertain count, or zero progress
without error ends the attempt as `storage-write-failure`. Never restart the
frame, rewrite uncertain bytes or retry a returned write error. Existing bytes
before tracked EOF are never write targets. Other required syscalls are not
retried after an error in this version.

After all frame bytes have been written, fsync journal. Only success commits
the append and advances tracked EOF/sequence or permits durable-success
acknowledgement and transition publication. A committed intent stays committed
if a later operation or acknowledgement fails. Before a dependent external
call, materialize journaled records using temporary-file full write, fsync,
same-directory rename and directory fsync, in that order. Immutable inputs are
written once and durable before acceptance (§4.2), never replacement targets.
Their write/sync failures also poison, even before a journal append exists.

Journal the complete intent/in-flight marker before effects. The
state-transition lock covers only local journal/state I/O: release it before
all downstream calls/service waits; reacquire it to revalidate versions/fences
and journal results. Cancellation, per-record serialization, receiver fences
and late-result obligations remain unchanged. Ownership flock has a separate
lifetime; no downstream wait occurs under the state-transition lock.

## Failure classes and session termination

These are local semantic classes, not published refusal codes (§16):

| Class | Trigger and disposition |
| --- | --- |
| `payload-invalid`, `journal-full` | Pre-write append checks are clean, no bytes or sequence consumed; oversized-file `journal-full` during open instead ends acquisition |
| `ownership-contended` | Nonblocking flock contention; fail acquisition and close descriptors |
| `unsupported-storage` | Missing deployment precondition, type gate or required operation unsupported; end acquisition/session, no weaker retry |
| `missing-state` | Missing required directory/journal, or empty established journal; end acquisition/session, §11.1 |
| `invalid-state` | Descriptor/protection/creation rule violated or tracked EOF changed; end acquisition/session; EOF change poisons |
| `corruption`, `incomplete-tail` | Replay classification below; end session, preserve bytes |
| `storage-read-failure` | Other open/metadata/read operation error; end acquisition/session |
| `consumer-failure` | Invalid/missing replay budget, semantic rejection, allocation budget excess or consumer failure; end acquisition/session |
| `storage-write-failure` | Full-write, non-unsupported fsync, rename, other local-state mutation or descriptor-close error; poison and end session |

Apart from the two clean append refusals, failures are terminal to the current
acquisition/session; preserve any more specific unsupported/classification
cause. Poison is set for storage-write-failure, unsupported errors during state
mutation/sync, observed EOF drift, and consumer/read failure after recovery
materialization starts. Other terminal acquisition/validation/replay refusals
do not set poison, but still end the session. No append, dependent effect,
retry or new durable-success report is permitted in a failed/poisoned session.
Previously committed intents and prior valid acknowledgements are not revoked.
Retain input/journal bytes, claims, obligations and original effect IDs. ENOSPC
from actual I/O is a write failure, not the clean projected-cap refusal.

End the session promptly: reject/drain queued transitions without effects,
quiesce its local state I/O, close journal/record/temporary/directory descriptors,
then release/close flock last. No recovery/operator wait while retaining it.
If quiescence cannot be ensured, terminate the owning process rather than
release ownership while a writer may survive. This is session termination, not
a requirement to kill the daemon on every clean refusal. Failed initialization
also preserves files; no automatic unlink, truncate, chmod or repair.

**Sound recovery** means the failed session has ended and a new ownership
session successfully completes `open-established`, both replay passes, journal
recovery fsync, ordered record materialization and whole-consumer success below.
No in-session reopen/reset clears poison. Unreadable/empty/corrupt/incomplete
state cannot meet that definition; §11.1 reconciliation and any byte recovery
remain outside this contract. Storage errors never prove absence, free capacity
or successful cleanup; a full disk promises no writable recovery control path.

## Replay and partial-header classification

Both entries require a positive finite integer consumer-memory budget as an
explicit internal parameter, with no default; reject missing/zero/nonfinite or
unrepresentable budgets as `consumer-failure` before callbacks or state mutation.
The journal layer checks its presence/validity; the consumer charges retained
and decoded allocations before use and aborts on excess. Streaming cannot hide
an unbounded consumer or bypass this budget.

Established recovery starts admission closed, under exclusive ownership. First
validate the entire journal structurally, from byte zero, one frame at a time,
without consumer callbacks or record writes. Track validated EOF and expected
sequence. Clean EOF at a frame boundary completes this pass; established empty
history is still `missing-state`. For a header starting at offset O, define
U = min(1,048,576, 67,108,864 - O - 56), using checked signed arithmetic. If U < 1
there is no valid further header. Otherwise admissible N is [1,U].

EOF with 1..23 header bytes is `incomplete-tail` only if the bytes can extend
to a header with the exact fixed magic/version/flags, expected sequence, and
some admissible N. Compare every present fixed-field/sequence byte. For k present
length bytes (0..4) with big-endian value v (v=0 when k=0), possible N is
the closed interval
[v * 256^(4-k), (v+1) * 256^(4-k) - 1]. Its intersection with [1,U] must be
nonempty. No expected successor sequence, any contradictory byte, or infeasible
length interval means `corruption`, even if no body follows. A full header must
meet the same rules; invalid full header is corruption regardless of body EOF.

After a valid header, EOF before all N payload and 32 digest bytes arrive is
`incomplete-tail`; digest validation occurs on the complete frame. A complete
bad digest is corruption. Read errors are `storage-read-failure`, never EOF.
Neither tail classification nor a complete valid frame proves original sync,
acknowledgement or absence of prior effects. On any failed replay preserve the
entire file and treat the validated prefix only as evidence, not complete state.
No truncation, skip, resynchronization, silent repair or capacity release.

After a successful first pass, fsync the validated journal **before any derived
record materialization**. Require its size to remain the first pass's EOF,
including before and after the second pass; drift is terminal `invalid-state`.
The second pass is bounded by that EOF and validates the same frames again
and feeds the budgeted consumer, which idempotently reconstructs records using
temporary-file full write, fsync, rename and directory fsync. Consumer failure
may leave derived cache files partially materialized, but cannot publish
readiness or effects; the next recovery reconstructs them again, never treating
those caches as history. Require complete second-pass and whole-consumer
success before publishing recovered state or resuming appends. Any failure
ends the session as above. This two-pass ordering bounds memory without making
record state durable ahead of its journal.

Replay conservatively includes every complete valid unacknowledged frame,
including a first frame. Retries resolve the original effect ID and immutable
arguments; a lost reply does not authorize a new intent or effect. Forward
admission still requires §7.1 reconciliation/accounting and current authority;
local replay alone supplies neither. No tail-repair procedure is introduced.

Append and replay each retain at most one frame's raw storage (1,048,632 bytes,
including owned payload copies), at most 32,768 bytes I/O scratch and constant
counters/hash state; no frame list, second assembled copy or journal buffer.
Borrowed caller payload and separately budgeted consumer state are not hidden
storage copies. Bounded queued/in-flight work remains required by §4.1.

Checksum, sequence, file cap and advisory ownership are integrity/bounds only,
not authenticated custody, generation sources, restore completeness, external
fences or native-launcher guarantees. Consistent rollback is locally
undetectable; §11.1's independent inventory, external fence and non-resurrection
requirements remain mandatory. No receiver outcome, external-reference API,
launch/grant/operator wire or authority is added.

## Adjacent contract examples

`F(s,p)` is a correctly hashed frame; a/b are single bytes. These assume valid
consumer semantics, adequate budget and storage/ownership preconditions.

| Valid control | Forbidden/failed neighbor and required disposition |
| --- | --- |
| New journal, first frame sequence 1 | First sequence 0 or 2: corruption; never skip |
| `F(1,a)` then `F(2,b)` | `F(1,a)` then `F(3,b)`: corruption; claims retained |
| Unchanged golden vector | Change payload without rehash or digest alone: corruption |
| First 23 bytes of the expected golden header: incomplete-tail | Wrong first magic byte, expected-sequence prefix or infeasible N prefix: corruption, not tail |
| Three N bytes `00 10 00` at O=0 can extend to N=1,048,576 | `00 10 01` cannot extend within the payload cap: corruption |
| Complete valid header/payload/digest | Remove one payload/digest byte at EOF: incomplete-tail, no truncate |
| N=1,048,576 within projected capacity | N=1,048,577 full header: corruption before body allocation; append refuses payload-invalid before write |
| 63 maximum frames then sequence 64 with N=1,044,992: exactly 67,108,864 bytes, replays | Increase that final N by 1: 67,108,865 bytes refused journal-full before replay; projected append also refuses cleanly |
| Clean refusal, then fitting valid append at unchanged EOF/sequence | Consuming sequence or poisoning on payload-invalid/projected journal-full is forbidden |
| Positive finite replay budget and consumer within it | Missing/zero budget refuses before callbacks; excess ends session consumer-failure |
| One checked owner, descriptor-relative journal/record operations | Second owner: ownership-contended, no journal access; unsafe descriptor: invalid-state, no repair |
| Same initialize session accepts empty then commits first real frame | Established empty: missing-state; initialize existing journal: invalid-state; no cross-process empty handoff or absence inference |
| Complete first frame survives crash before acknowledgement: established recovery retains intent | Empty/partial crashed initialization cannot silently recreate history |
| Known short successful writes plus full completion and fsync commit | Returned error/zero progress, actual ENOSPC or failed sync: poison, no success/effect or uncertain retry |
| Raw zero-transfer EINTR then successful same-offset retry | Retry after a returned full-write error or beyond the retry bound is forbidden |
| Journal recovery fsync, then temp-write/fsync/rename/directory-fsync | Failed record/directory sync ends session; committed intent remains, no dependent effect |
| New session fully replays/syncs/reconstructs; original effect ID retry | Same-session poison reset, stale supervisor direct write or new effect on lost reply is forbidden |

These are contract examples, not executable or passing product tests. Static
literal/arithmetic checks cannot qualify storage. Later isolated Linux tests
must pair valid controls with framing/partial-header/cap/budget rejections,
actual process contention and unsafe-file/descriptor plants, short/zero/error
writes, bounded EINTR behavior, ENOSPC, file/directory sync/rename failures and
crashes around creation/append/sync/rename/acknowledgement. Assert unchanged
bytes/sequence on clean refusal, session termination/ownership release, bounded
memory, retained claims and no early success/duplicate effects. Durability,
restore custody and native execution remain unqualified.
