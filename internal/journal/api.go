package journal

import "strings"

// Config contains explicit caller attestations, not automatically inferred facts.
// Records and Inputs are single descendant directory basenames, already present.
type Config struct {
	Directory                       string
	Records                         string
	Inputs                          string
	DeclaredUmask                   *uint32
	SupportedLocalStorage           bool
	RecoveryPrerequisiteEstablished bool
	ConsumerBudget                  uint64
}

func basename(s string) bool {
	return s != "" && len(s) <= 255 && s != "." && s != ".." && !strings.ContainsAny(s, "/\x00") && !strings.HasPrefix(s, ".record-tmp-")
}
func validate(c Config, first []byte, initializing bool) error {
	if c.DeclaredUmask == nil || *c.DeclaredUmask&0600 != 0 {
		return failure(InvalidState, "declared umask", nil)
	}
	if c.ConsumerBudget == 0 || c.ConsumerBudget > uint64(^uint(0)>>1) {
		return failure(ConsumerFailure, "budget required", nil)
	}
	if c.Directory == "" || strings.IndexByte(c.Directory, 0) >= 0 || !basename(c.Records) || !basename(c.Inputs) {
		return failure(InvalidState, "paths", nil)
	}
	if initializing && (len(first) < 1 || len(first) > MaxPayload) {
		return failure(PayloadInvalid, "first payload", nil)
	}
	if !initializing && !c.RecoveryPrerequisiteEstablished {
		return failure(InvalidState, "recovery attestation required", nil)
	}
	if !c.SupportedLocalStorage {
		return failure(UnsupportedStorage, "deployment prerequisite", nil)
	}
	return nil
}

// Budget is a sticky, checked accounting ledger. Consumers must Charge before
// allocating decoded/retained memory, and Release only when that memory is dead.
// A failed charge cannot be hidden by ignoring its return or releasing later.
type Budget struct {
	limit, used uint64
	failed      bool
}

func (b *Budget) Charge(n uint64) error {
	if b.failed || n > b.limit-b.used {
		b.failed = true
		return failure(ConsumerFailure, "budget exceeded", nil)
	}
	b.used += n
	return nil
}
func (b *Budget) Release(n uint64) error {
	if n > b.used {
		b.failed = true
		return failure(ConsumerFailure, "budget release", nil)
	}
	b.used -= n
	return nil
}
func (b *Budget) Used() uint64 { return b.used }

// Consumer runs synchronously during the second pass only. It must not publish
// readiness/effects, perform downstream I/O, retain borrowed payloads, spawn
// work, panic, or keep Recovery after return. Finish validates whole-state success.
// It owns semantic, epoch and original-effect-ID validation and allocation charges.
type Consumer interface {
	Consume(sequence uint64, payload []byte, recovery *Recovery) error
	Finish(recovery *Recovery) error
}

// State is an observation, never permission to issue an external effect.
type State struct {
	EOF                     int64
	Next                    uint64
	Ready, Closed, Poisoned bool
}
