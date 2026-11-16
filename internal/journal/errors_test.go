package journal

import (
	"errors"
	"fmt"
	"testing"
)

// Claim: a typed-nil *Error is a safe node for Error, Unwrap, errors.Is and
// errors.As in bare, wrapped and joined positions, and a poisoned sibling behind
// it still carries the sync sentinel. The non-nil control pins the unchanged
// message and cause so a stub that always reports nil cannot pass.
func TestErrorTypedNilSafe(t *testing.T) {
	var empty *Error
	cause := errors.New("cause")
	control := &Error{Class: StorageWriteFailure, Op: "pwrite", Cause: cause}
	defer func() {
		if recover() != nil {
			t.Fatal("TYPED-NIL-METHODS: Error or Unwrap panicked")
		}
	}()
	if empty.Error() != "journal: <nil>" || empty.Unwrap() != nil {
		t.Errorf("TYPED-NIL-METHODS: nil receiver changed: %q %v", empty.Error(), empty.Unwrap())
	}
	if control.Error() != "journal: storage-write-failure: pwrite" || control.Unwrap() != cause {
		t.Errorf("TYPED-NIL-METHODS: control changed: %q %v", control.Error(), control.Unwrap())
	}
	marked := &Error{Class: StorageWriteFailure, Op: "fsync", Cause: ErrSyncFailure, Poisoned: true}
	// ClassOf skips a typed nil that errors.As would return and stop at.
	if ClassOf(empty) != "" || ClassOf(errors.Join(empty, marked)) != StorageWriteFailure || ClassOf(control) != StorageWriteFailure {
		t.Errorf("TYPED-NIL-METHODS: ClassOf hid a class behind a typed nil: %q %q", ClassOf(empty), ClassOf(errors.Join(empty, marked)))
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"join/nil-first", errors.Join(empty, marked), true},
		{"join/nil-only", errors.Join(empty, errors.New("plain")), false},
		{"wrap/nil-cause", &Error{Class: InvalidState, Op: "outer", Cause: empty}, false},
		{"wrap/nil-then-poison", fmt.Errorf("outer: %w", errors.Join(empty, marked)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errors.Is(tc.err, ErrSyncFailure); got != tc.want {
				t.Errorf("TYPED-NIL-METHODS: sentinel=%v want=%v", got, tc.want)
			}
			var je *Error
			if !errors.As(tc.err, &je) {
				t.Error("TYPED-NIL-METHODS: errors.As found no *Error")
			}
			if MustExit(tc.err) != tc.want {
				t.Error("TYPED-NIL-METHODS: MustExit disagrees with sentinel")
			}
		})
	}
}
