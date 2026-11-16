//go:build linux && (amd64 || arm64)

package journal

import (
	"errors"
	"fmt"
	"testing"
)

// Claim (finite-depth witness): hasPoison terminates on an acyclic tree of
// known finite depth and reports poison exactly when the single deepest leaf is
// poisoned, for unary chains, join-bearing chains and chains with typed-nil
// siblings at every level. The unpoisoned twin at every depth is the control;
// the depth-1 case pins that the predicate is not "any Error present".
func TestPoisonWalkFiniteDepthWitness(t *testing.T) {
	var empty *Error
	shapes := map[string]func(child error, level int) error{
		"unary": func(child error, _ int) error { return fmt.Errorf("level: %w", child) },
		"error": func(child error, _ int) error { return &Error{Class: InvalidState, Op: "level", Cause: child} },
		"join":  func(child error, _ int) error { return errors.Join(errors.New("plain"), child) },
		"join/nil-sibling": func(child error, _ int) error {
			return errors.Join(empty, failure(MissingState, "clean", nil), child)
		},
	}
	for name, wrap := range shapes {
		for _, depth := range []int{1, 2, 16, 256, 1024} {
			for _, marked := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/depth-%d/poison-%v", name, depth, marked), func(t *testing.T) {
					leaf := &Error{Class: StorageWriteFailure, Op: "leaf", Cause: ErrSyncFailure, Poisoned: marked}
					var tree error = leaf
					for level := 0; level < depth; level++ {
						tree = wrap(tree, level)
					}
					if got := hasPoison(tree); got != marked {
						t.Errorf("DEPTH-WITNESS: predicate=%v want=%v", got, marked)
					}
					if !errors.Is(tree, ErrSyncFailure) {
						t.Error("DEPTH-WITNESS: sentinel lost on the same tree")
					}
					if ClassOf(tree) == "" {
						t.Error("DEPTH-WITNESS: class lost on the same tree")
					}
				})
			}
		}
	}
}
