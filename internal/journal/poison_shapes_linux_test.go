//go:build linux && (amd64 || arm64)

package journal

import (
	"errors"
	"fmt"
	"testing"
)

// A typed nil *Error is an empty node for hasPoison, bare, joined or as a Cause.
// Each shape has a non-nil poisoned control; a nil sibling must not hide it.
// This pins the predicate only: ClassOf, poison and Error methods are unchanged.
func TestFailurePoisonTypedNil(t *testing.T) {
	var empty *Error
	marked := &Error{Class: StorageWriteFailure, Poisoned: true}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"bare/nil", empty, false},
		{"bare/poison", marked, true},
		{"join/nil", errors.Join(empty, errors.New("plain")), false},
		{"join/poison-after-nil", errors.Join(empty, marked), true},
		{"cause/nil", &Error{Class: InvalidState, Cause: empty}, false},
		{"cause/poison", &Error{Class: InvalidState, Cause: marked}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() != nil {
					t.Error("TYPED-NIL: predicate panicked")
				}
			}()
			if got := hasPoison(tc.err); got != tc.want {
				t.Errorf("TYPED-NIL: predicate=%v want=%v", got, tc.want)
			}
		})
	}
}

// An unpoisoned Error is not a leaf: its Cause (including a join and another
// Error) contributes poison. Clean twins pin refusal shape and prevent a walk
// that simply treats every Error as poisoned from satisfying the witness.
func TestFailurePoisonCauseChain(t *testing.T) {
	for _, joined := range []bool{false, true} {
		t.Run(fmt.Sprintf("joined-%v", joined), func(t *testing.T) {
			checkPoisonShape(t, "CAUSE-CHAIN:", func(leaf error) error {
				inner := &Error{Class: InvalidState, Cause: leaf}
				if joined {
					return &Error{Class: InvalidState, Cause: errors.Join(errors.New("plain"), inner)}
				}
				return &Error{Class: InvalidState, Cause: inner}
			})
		})
	}
}

// The poisoned leaf is three edges below the root (join, unary, unary).
// A clean twin and both argument positions pin class, sentinel and disposition;
// the direct tail/recorder observations cannot be rescued by a later join.
func TestFailurePoisonDepthThree(t *testing.T) {
	checkPoisonShape(t, "DEPTH-THREE:", func(leaf error) error {
		return errors.Join(failure(InvalidState, "first", nil), fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", leaf)))
	})
}

func checkPoisonShape(t *testing.T, assertion string, shape func(error) error) {
	t.Helper()
	for _, marked := range []bool{false, true} {
		for _, first := range []bool{false, true} {
			t.Run(fmt.Sprintf("poison-%v/first-%v", marked, first), func(t *testing.T) {
				leaf := &Error{Class: StorageWriteFailure, Cause: ErrSyncFailure, Poisoned: marked}
				tree := shape(leaf)
				if hasPoison(tree) != marked {
					t.Errorf("%s predicate lost disposition", assertion)
				}
				var a, b error = failure(MissingState, "earlier", nil), tree
				want := MissingState
				if first {
					a, b, want = tree, a, InvalidState
				}
				s := &Session{e: &engine{}, lock: -1, callbackFailure: a}
				r := &Recovery{s: s, err: b}
				check := func(label string, err error, class Class) {
					t.Helper()
					var je *Error
					if ClassOf(err) != class || !errors.As(err, &je) || je == nil || je.Poisoned != marked || !errors.Is(err, ErrSyncFailure) {
						t.Errorf("%s %s lost class, poison or sentinel", assertion, label)
					}
				}
				result := r.result(nil)
				check("result", result, want)
				if !marked {
					if _, wrapped := result.(*Error); wrapped {
						t.Errorf("%s clean join promoted", assertion)
					}
				}
				terminal := s.fail(b)
				check("fail", terminal, want)
				if s.state.Poisoned != marked || !s.state.Closed || s.terminal != terminal {
					t.Errorf("%s joined terminal state or identity changed", assertion)
				}
				direct := &Session{e: &engine{}, lock: -1}
				terminal = direct.fail(tree)
				check("tail", terminal, InvalidState)
				if direct.state.Poisoned != marked || !direct.state.Closed || direct.terminal != terminal || (!marked && terminal != tree) {
					t.Errorf("%s direct terminal state or identity changed", assertion)
				}
				recording := &Session{e: &engine{}, lock: -1, callbackRunning: true}
				if ret := recording.fail(tree); ret != tree || recording.state.Poisoned != marked || recording.state.Closed || recording.terminal != nil || recording.callbackFailure != tree {
					t.Errorf("%s recorder lost disposition or changed lifetime", assertion)
				}
			})
		}
	}
}
