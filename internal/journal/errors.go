package journal

import "errors"

// Class is a local disposition, not a wire refusal code.
type Class string

const (
	PayloadInvalid      Class = "payload-invalid"
	JournalFull         Class = "journal-full"
	OwnershipContended  Class = "ownership-contended"
	UnsupportedStorage  Class = "unsupported-storage"
	MissingState        Class = "missing-state"
	InvalidState        Class = "invalid-state"
	Corruption          Class = "corruption"
	IncompleteTail      Class = "incomplete-tail"
	StorageReadFailure  Class = "storage-read-failure"
	ConsumerFailure     Class = "consumer-failure"
	StorageWriteFailure Class = "storage-write-failure"
)

// ErrSyncFailure survives wrapping and teardown errors via errors.Is.
var ErrSyncFailure = errors.New("journal: sync failure requires process exit")

// Error preserves the primary class and original cause. Poisoned means the
// failed acquisition/session encountered uncertain mutation, not lost intent.
type Error struct {
	Class    Class
	Op       string
	Cause    error
	Poisoned bool
}

func (e *Error) Error() string { return "journal: " + string(e.Class) + ": " + e.Op }
func (e *Error) Unwrap() error { return e.Cause }
func ClassOf(err error) Class {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ""
}

// MustExit reports the process-exit duty; it never exits the process itself.
func MustExit(err error) bool                        { return errors.Is(err, ErrSyncFailure) }
func failure(c Class, op string, cause error) *Error { return &Error{Class: c, Op: op, Cause: cause} }
