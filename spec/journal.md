# Local durable journal contract

This draft narrows the local storage choices needed to implement
[dispatcher §4.1](dispatcher.md#41-state-directory). MUST and MUST NOT are
normative. It specifies storage, not a launch path or an implemented/qualified
service. Input, transition, in-flight marker and epoch encodings remain with
their owning contracts; the journal stores opaque transition bytes and cannot
by itself validate their authority or semantics.

## Framing and limits

One journal is a concatenation of frames, without padding or a file preamble.
All integers are unsigned, big-endian. The version-1 frame is:

| Byte offset | Length | Value |
| ---: | ---: | --- |
| 0 | 8 | ASCII `SDJRNL01` |
| 8 | 2 | Version, exactly 1 |
| 10 | 2 | Reserved flags, exactly 0 |
| 12 | 4 | Payload byte length N |
| 16 | 8 | Sequence |
| 24 | N | Exact payload bytes |
| 24 + N | 32 | Raw SHA-256 of the complete 24-byte header followed by payload |

N MUST be 1..1,048,576 bytes. Frame length is 56 + N, at most 1,048,632
bytes. The journal file MUST NOT exceed 67,108,864 bytes (64 MiB). These are
internal storage limits, not request/output quotas or a control-space reserve.
Sequence starts at 1 and increases by exactly 1 for every frame; zero, gaps,
duplicates and uint64 wrap are invalid. Unknown magic, version or flags,
invalid length, sequence or digest MUST fail replay. Never scan ahead for a
new magic value. Check the header and checked size arithmetic before body
allocation/read, and the projected file size before any append. Reject a file
over the cap before replay. No rotation, compaction, snapshot or migration is
specified here; exhaustion refuses further appends without deleting history.

## Local ownership and initialization

This version targets a local Linux filesystem with working `flock`, file
`fsync`, atomic same-directory rename and directory `fsync`; unsupported
storage MUST refuse. In the protected state directory (§4.1), use regular
files named `lock` and `journal`, mode 0600, with no symlink following or
shared hard links. Take a nonblocking exclusive Linux `flock` on `lock`
before opening/replaying the journal or touching record state. Hold it through
all replay, append and record I/O until those operations and descriptors have
closed. Prevent descriptor inheritance into children. A contender MUST fail
ownership without reading or writing the journal. Never unlink or replace the
lock inode, or release its flock while state I/O can still occur.
State-directory replacement is outside ordinary operation.

Journal creation is permitted only for explicit fresh-state initialization,
using exclusive creation, file flush and parent-directory flush before it is
usable. Opening established state MUST NOT create a missing journal or use
truncation. An existing empty journal is valid only in a known fresh-state
context; emptiness cannot certify absence of prior effects. Missing/lost state
uses [§11.1](dispatcher.md#111-state-loss-and-restore-r-gen), never automatic
fresh initialization. Immutable inputs are written once and made durable
before acceptance (§4.2); replay MUST NOT overwrite them.

## Append and durability ordering

Serialize local transitions under the state-transition lock. After successful
replay, fix the next sequence and immutable payload, write the entire frame at
the validated EOF, then `fsync` the journal. A short, zero-progress or failed
write is failure, not a complete append. Only successful full write and sync
commit the append and permit a durable-success acknowledgement, publication
of the transition or its dependent external effect. Do not advance the usable
sequence or publish tentative state before that boundary.

Journal the complete intent and in-flight marker before issuing an effect.
Apply the journaled record changes using §4.1's temporary-file write, flush,
rename and directory-flush order before issuing the dependent call. Immutable
inputs are never replacement targets. Release the state-transition lock
before every downstream call or service wait; it covers only journal/local
state I/O. Reacquire it to revalidate versions/fences and journal the result.
The lifetime ownership lock is separate. Existing cancellation, late-result,
daemon-epoch and per-record serialization requirements remain unchanged.

Any write, sync or record-replacement failure, including ENOSPC, poisons
forward admission and dependent effects until sound recovery. Preserve bytes,
claims, pending obligations and the original effect IDs. Report the failed
operation; never report durable success, roll back a committed intent or retry
the uncertain append blindly. Failure after journal sync leaves a committed
intent even if record publication or the caller's acknowledgement failed.
No space-reclamation or successful cleanup is inferred from a storage error.
This bounded store does not promise writable recovery controls on a full disk;
independent containment remains governed by the dispatcher specification.

## Replay, crash and corruption

Start with admission closed and exclusive ownership. Replay from byte zero,
validating every frame before delivering it in sequence to a recovery consumer.
Record files are derived state: replay must idempotently reconstruct journaled
transitions over them, including a crash between commit and record replacement.
Do not publish a partly replayed state or authorize effects from its prefix.
A semantically invalid transition or consumer failure also aborts recovery.

Clean EOF exactly at a frame boundary completes structural replay. EOF after
1..23 header bytes, or before all N payload and 32 digest bytes of a validated
header arrive, is an **incomplete tail**, never accepted as a committed frame.
This structural label cannot prove the bytes were never previously committed
or that no effect occurred. A full frame with a bad digest or other invalid
field is **corruption**, including at EOF. An invalid full header fails
immediately rather than being excused as
an incomplete body. Read errors are storage failures, not clean EOF or absence.
Every such failure keeps admission closed and the original file unchanged:
no truncation, repair, skipped bytes or prefix-as-complete recovery. Preserve
the validated prefix as evidence, not a basis for releasing capacity.

A complete, valid frame is a replay candidate, not proof that its original
writer's sync or acknowledgement succeeded. After a crash, conservatively
replay every complete valid frame, including an unacknowledged final frame.
Before recovery is durable, sync the validated journal, materialize and flush
derived record changes, and require the consumer's whole-replay success.
Sync/consumer failure keeps admission poisoned. Only then may appends resume;
forward admission still needs §7.1 reconciliation/accounting and current
authority. Retry an unresolved external effect with its original ID and
arguments, never by appending a new intent merely because a reply was lost.
Tail/corruption recovery needs an explicitly reviewed recovery procedure and
independent evidence; this contract authorizes no byte repair or loss of debt.

Replay MUST stream, retaining at most one raw frame (1,048,632 bytes), at most
32,768 bytes of I/O scratch, and constant-size counters/hash state. No frame
list or journal-sized buffer. This bounds storage replay buffers, not decoded
application state: the consumer MUST set a finite state-memory budget before
replay, charge retained/decoded allocations before use, and abort with admission
closed on missing budget or excess. It MUST NOT hide unbounded buffering behind
the streaming interface. Retained history remains bounded by the file cap.

A digest, sequence and local lock detect only their stated local properties.
They are not authenticated state custody, restore completeness, a generation
source, an external fence or a native-launcher guarantee. Internally consistent
rollback is not locally detectable; §11.1's external fence, independent
inventory and non-resurrection gates remain mandatory. No receiver observation,
external-reference API, grant or operator/launch wire is introduced here.

## Adjacent contract examples

Let `F(s,p)` mean the frame above with sequence s, payload p and a correct
digest; `a` and `b` each denote one opaque byte. Examples describe storage
verdicts only, assuming valid consumer semantics and required ownership.

| Valid control | Forbidden/failed neighbor and required disposition |
| --- | --- |
| `F(1,a) \|\| F(2,b)` replays both | `F(1,a) \|\| F(3,b)` fails sequence validation; no skip or capacity release |
| Unchanged `F(1,a)` validates | Change its payload to b without recomputing digest, or change digest alone: corruption, preserve bytes |
| Complete 24-byte header and body/digest | EOF at header byte 23, or one missing payload/digest byte: incomplete tail, admission closed, no truncation |
| N=1,048,576 with complete bytes is within the frame bound | N=1,048,577 in a full header fails before body allocation, even if no body follows |
| One owner holds `flock` through replay/append | Second process refuses ownership and performs no journal access |
| Full frame write + successful sync commits | Short write, ENOSPC or failed sync: no acknowledgement/effect, poisoned admission, no blind append retry |
| Journal synced, crash before record rename: replay reconstructs the transition | Failed record/directory flush cannot authorize the dependent call; committed intent remains for recovery |
| Complete unacknowledged `F(2,b)` after `F(1,a)`: replay and successful recovery sync retain both | Lost reply does not authorize a second intent/new effect ID; retry only after sound recovery and reconciliation |

These are contract examples, not executable or passing tests. Implementation
requires isolated Linux tests for framing/boundaries and valid controls beside
each rejection; actual process contention, bounded replay/consumer memory,
short/zero writes, ENOSPC, file/directory sync errors and crash points before
and after append/sync/rename/acknowledgement require executable fault injection.
Verify preserved journal/input bytes, closed admission, retained claims and
no premature/duplicate effect, not merely an arbitrary error. Static review
cannot qualify durability, restore custody or native execution.
