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

// Error and Unwrap are nil-safe: a typed-nil *Error reached through errors.Is,
// errors.As or an errors.Join tree neither panics nor terminates the walk early,
// so a poisoned sibling behind it is still found (journal review r7).
func (e *Error) Error() string {
	if e == nil {
		return "journal: <nil>"
	}
	return "journal: " + string(e.Class) + ": " + e.Op
}
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
func ClassOf(err error) Class {
	if e := firstError(err); e != nil {
		return e.Class
	}
	return ""
}

// firstError is errors.As for *Error that skips typed-nil nodes instead of
// returning them, in the same depth-first order. errors.As would assign a
// typed nil and stop, hiding the class behind it; a nil result means no
// non-nil *Error exists in the tree.
func firstError(err error) *Error {
	switch e := err.(type) {
	case nil:
		return nil
	case *Error:
		if e != nil {
			return e
		}
		return nil
	case interface{ Unwrap() []error }:
		for _, child := range e.Unwrap() {
			if found := firstError(child); found != nil {
				return found
			}
		}
		return nil
	case interface{ Unwrap() error }:
		return firstError(e.Unwrap())
	}
	return nil
}

// MustExit reports the process-exit duty; it never exits the process itself.
func MustExit(err error) bool                        { return errors.Is(err, ErrSyncFailure) }
func failure(c Class, op string, cause error) *Error { return &Error{Class: c, Op: op, Cause: cause} }
