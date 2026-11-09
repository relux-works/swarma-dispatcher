//go:build linux && (amd64 || arm64)

package journal

import (
	"crypto/sha256"
	"errors"
	"fmt"
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
	// Nested causes are placed in either argument. Depth two adds a unary
	// wrapper inside the join, so both Unwrap forms must be traversed.
	for _, depth := range []int{1, 2} {
		for _, nestedFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("nested/depth-%d/first-%v", depth, nestedFirst), func(t *testing.T) {
				plain := failure(InvalidState, "protection", nil)
				var branch error = poison(failure(StorageWriteFailure, "close", ErrSyncFailure))
				if depth == 2 {
					branch = fmt.Errorf("context: %w", branch)
				}
				nested := errors.Join(plain, branch)
				var first, incoming error = failure(MissingState, "earlier", nil), nested
				want := MissingState
				if nestedFirst {
					first, incoming = nested, first
					want = InvalidState
				}
				s := &Session{e: &engine{}, lock: -1, callbackFailure: first}
				r := &Recovery{s: s, err: incoming}
				check := func(label string, err error) {
					var je *Error
					if ClassOf(err) != want || !errors.As(err, &je) || !je.Poisoned || !errors.Is(err, ErrSyncFailure) {
						t.Errorf("NESTED-JOIN: %s lost class, poison or sentinel: %v", label, err)
					}
				}
				check("result", r.result(nil))
				check("fail", s.fail(incoming))
				check("terminal", s.terminal)
				if !s.State().Poisoned || !s.State().Closed {
					t.Error("NESTED-JOIN: terminal state unpoisoned")
				}
				// No callback record: exercise the quiescent tail independently of joinFailure.
				direct := &Session{e: &engine{}, lock: -1}
				err := direct.fail(nested)
				var je *Error
				if !errors.As(err, &je) || !je.Poisoned || !direct.State().Poisoned || direct.terminal != err {
					t.Error("NESTED-TAIL: nested poison lost")
				}
				// Observe the recorder before the later join can rescue its poison bit.
				recording := &Session{e: &engine{}, lock: -1, callbackRunning: true}
				ret := recording.fail(nested)
				if ret != nested || !recording.state.Poisoned || recording.state.Closed || recording.terminal != nil {
					t.Error("NESTED-RECORDER: nested poison lost or teardown occurred")
				}
			})
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
