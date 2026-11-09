//go:build linux && (amd64 || arm64)

package journal

import (
	"crypto/sha256"
	"errors"
	"testing"
)

// Spec 215, 217-221: first class is sticky, poison is the union of inputs,
// and the sync sentinel survives. Both recovery result and terminal Session
// teardown obey this, including the unpoisoned positive control.
func TestFailureJoinDisposition(t *testing.T) {
	for _, firstPoison := range []bool{false, true} {
		for _, incomingPoison := range []bool{false, true} {
			var first error = failure(MissingState, "first", nil)
			var incoming error = failure(StorageWriteFailure, "later", ErrSyncFailure)
			if firstPoison {
				first = poison(first)
			}
			if incomingPoison {
				incoming = poison(incoming)
			}
			s := &Session{lock: -1, callbackFailure: first}
			r := &Recovery{s: s, err: incoming}
			result := r.result(nil)
			terminal := s.fail(incoming)
			for _, err := range []error{result, terminal} {
				var je *Error
				if ClassOf(err) != MissingState || !errors.As(err, &je) || je.Poisoned != (firstPoison || incomingPoison) || !errors.Is(err, first) || !errors.Is(err, ErrSyncFailure) {
					t.Errorf("FAILURE-JOIN: class/poison/sentinel changed: first=%v incoming=%v result=%v", firstPoison, incomingPoison, err)
				}
			}
			if !s.state.Closed || s.state.Poisoned != (firstPoison || incomingPoison) {
				t.Error("FAILURE-JOIN: terminal state differs from error")
			}
		}
	}
}

// Outside callbacks, a cleanly classified Session refusal retains its concrete
// *Error shape. A successful input read prevents an always-refuse implementation
// from satisfying the control. This also pins non-poisoned terminal disposition.
func TestSessionFailureShape(t *testing.T) {
	_, _, s := sweepSetup(t)
	b, err := s.ReadInput("arg", sha256.Sum256([]byte("abc")))
	if err != nil || string(b) != "abc" {
		t.Fatalf("FAILURE-SHAPE: valid read refused: %v", err)
	}
	_, err = s.ReadInput("missing", [32]byte{})
	je, ok := err.(*Error)
	if !ok || je.Class != MissingState || je.Poisoned || !s.State().Closed || s.State().Poisoned {
		t.Fatalf("FAILURE-SHAPE: non-callback error wrapped or disposition changed: %T %v", err, err)
	}
	original := failure(InvalidState, "direct", nil)
	empty := &Session{lock: -1}
	if empty.fail(original) != original {
		t.Fatal("FAILURE-SHAPE: original error identity changed")
	}
}
