// Package m0 is the DRAFT runtime for the bounded existing-worker launch
// authority in spec/m0-launch.md. It is written against a draft specification
// and is not activatable: nothing in this package installs, deploys or enables
// anything by default, and every seam that depends on an unpublished slice
// refuses by name until it is pinned.
//
// The package supplies:
//
//   - a strict bounded CCJ-1 canonicalizer for the closed M0 forms;
//   - the closed dispatch and owned-cancel request forms, their finite bounds,
//     the exact signing preimage and Ed25519 verification;
//   - the closed policy and signing-plan forms with their cross-field bounds;
//   - the executor-entry /1 composition and the Codex run-codex argv mapping as
//     dispatcher-side witnesses (the adapter itself is implemented elsewhere);
//   - the durable one-launch ledger on internal/journal: reservation, intent,
//     committed start C, result, owned cancel, stop and observation frames,
//     replayed by an established-recovery consumer;
//   - the receiver interlock A as a commit gate: C is accepted only after the
//     receiver reports a verified privilege drop and only while the original
//     policy revision, generation, cancel state and expiry still permit it;
//   - the activation gate for the transport daemon: an extension is attached
//     only when an explicit configuration selects M0 and the policy source
//     reports an installed policy.
//
// Seams (see seams.go): Launcher (S1 user-launcher intake), Runner (S3a Claude
// runner argv), Clock, GenerationReader, PolicySource and EntryComposer. Their
// default implementations return a typed refusal naming the seam. The package
// never falls back to a dispatcher record, a broker lease, a launch_id or the
// wall clock in place of a refusing seam.
//
// Unknown is never success: a missing seam, an unreadable proof, a timeout or
// a lost reply latches UNKNOWN on the original record and retains its claim,
// debit and stop obligation. A second physical launch is never issued.
//
// M0 is Linux only. The ledger entry points refuse on other platforms.
package m0
