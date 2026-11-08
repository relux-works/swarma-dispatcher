// Package journal implements the local opaque journal in spec/journal.md.
// Linux amd64 and arm64 have direct syscall implementations; other targets
// refuse unsupported-storage. There is no daemon wiring or launch authority.
//
// Installation supplies protected nonsymlink ancestors and qualified ext4/XFS
// backing. Only the final root component and direct descendant components are
// checked here. The filesystem type gate does not qualify durability. Descendant
// mounts/st_dev are not checked for equality with the root; no cross-mount
// qualification is claimed. Records and inputs must be distinct dev/inode pairs.
// Neither entry creates directories. Files are never repaired or deleted.
//
// Declare the stable process umask explicitly. The library neither reads /proc
// nor calls umask. A false declaration cannot be detected before creation and
// may leave an unsafe lock (for example 0400), permanently refused until operator
// disposition. No mode repair is offered. Forked children must close inherited
// descriptors before external code and perform no state I/O; CLOEXEC alone is
// insufficient. Callers must bound queued work and prevent surviving writers.
//
// Any fsync error matches ErrSyncFailure and MustExit returns true. A process-wide
// latch refuses later ownership sessions; the library never exits, kills or
// reboots. The caller/service manager must enforce the barrier across processes.
// OpenEstablished requires an explicit recovery-prerequisite attestation. Every
// termination without a positively known no-sync-failure outcome (SIGKILL, OOM,
// panic, generic nonzero exit) is unknown: recovery holds until host restart or
// dispatcher section 11.1 original-evidence/operator disposition. This is a
// material availability cost, not something cached bytes or a later fsync fixes.
//
// Initialize needs a genuine first transition from its caller, never synthetic
// genesis. Effect-table, index and claims are records when their owners implement
// their schemas; the dispatcher key is outside this primitive. Payloads remain
// opaque. Callers own semantic transitions, epochs, fences, authority and original
// effect IDs. Local success proves none of those and never proves absence.
//
// Session methods serialize local I/O only, with no caller callbacks under their
// mutex. Callers keep supplied slices immutable until return. Recovery callbacks run synchronously before the Session is published,
// with no state mutex held; their limited Recovery handle performs local I/O.
// They must not spawn work or perform downstream calls. Release the caller's
// state-transition lock before external waits; keep the separate lifetime flock.
// On terminal failure all descriptors close, flock last. No in-session reset or
// tail repair exists: every torn tail closes admission. A consumer panic unwinds
// ownership, but the process still has an unknown recovery outcome.
//
// Inputs are created once at their final name, with O_EXCL, written fully and
// file- then directory-synced before success. Existing inputs refuse invalid-state
// without opening or deleting them. ReadInput requires a digest from committed
// history; mismatched bytes refuse corruption. A crashed partial input is never
// reused implicitly. Records cannot rename into the separate input directory.
// Record and input byte arguments are bounded to MaxPayload; consumers charge
// returned input bytes to their budget. No input-read success establishes that
// the supplied digest actually came from committed history; that is caller duty.
//
// Stale .record-tmp-* files are retained and ignored, with no reclamation. Their
// unbounded growth across crashes is an operator/storage cost. ENOSPC is an
// ordinary write failure and poisons, not journal-full. Journal-full refuses
// every transition. There is no SH1 or SH2+ control-reserve claim, rotation,
// compaction, restore or cleanup workflow.
package journal
