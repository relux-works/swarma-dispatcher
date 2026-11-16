//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
)

// The two tests in this file use only fixture helpers that did not change
// with the fixes they prove, so the same file is the control that fails on
// the revision before them and passes with them.

// Claim (End on a store that refuses Update, control across revisions): with
// the record store closed before End, so that the bound -> ending update is
// refused, End still calls teardown, still kills by uid through the helper
// (the released child is gone), sends no end frame, returns run_end_unknown
// with exit 17 and leaves the uid quarantined.
func TestEndStoreClosed(t *testing.T) {
	e := newEnv(t)
	run := endReady(t, e)
	must(t, e.store.Close())
	torn := false
	_, err := run.End(context.Background(), 3, true, func() error { torn = true; return nil })
	if !torn {
		t.Error("teardown was not called")
	}
	if uids := e.killer.UIDs(); len(uids) != 1 || uids[0] != cuid {
		t.Errorf("no kill by uid: %v", uids)
	}
	if _, serr := os.Stat(filepath.Join(e.proc.Root, "777")); !errors.Is(serr, os.ErrNotExist) {
		t.Error("the released child survived")
	}
	if len(e.km.Ends()) != 0 {
		t.Errorf("an end was sent without a durable intent: %d", len(e.km.Ends()))
	}
	v, x := codes.Exit(err, 3, true)
	if !codes.Is(err, codes.EndUnknown) || x != nil || v.Status != 17 {
		t.Errorf("exit: %v %+v %v", err, v, x)
	}
	if !errors.Is(err, runrecord.ErrClosed) {
		t.Errorf("cause not carried: %v", err)
	}
	if !e.quar.Held(cuid) {
		t.Error("uid released without an acknowledged end")
	}
}

// Claim (pre-record lease, control across revisions): with the fixed
// placement, a transient Source connect failure and then a transient
// Keymaker connect failure each fail retryable (exit 1) with no record, and
// the following Begin with the same fixed uid binds under that uid.
func TestFixedLeaseSurvivesTransientFailures(t *testing.T) {
	e := newEnv(t)
	l := e.build()
	retryable := func(what string, err error) {
		t.Helper()
		v, x := codes.Exit(err, 0, false)
		if x != nil || v.Status != 1 || len(e.store.Keys()) != 0 {
			t.Fatalf("%s: not a retryable failure without a record: %v %+v %v", what, err, v, x)
		}
	}
	e.srcDialer.Fail = errors.New("synthetic source connect failure")
	_, err := l.Begin(context.Background(), "input-1")
	retryable("source", err)
	if !codes.Is(err, codes.OriginUnavailable) {
		t.Fatalf("source: %v", err)
	}
	e.srcDialer.Fail = nil
	e.kmDialer.Fail = errors.New("synthetic keymaker connect failure")
	_, err = l.Begin(context.Background(), "input-1")
	retryable("keymaker", err)
	if !codes.Is(err, codes.BindUnavailable) {
		t.Fatalf("keymaker after a source failure with the same fixed uid: %v", err)
	}
	e.kmDialer.Fail = nil
	run, err := l.Begin(context.Background(), "input-1")
	if err != nil {
		t.Fatalf("Begin with the same fixed uid after transient failures: %v", err)
	}
	if rec := e.record(run.Key); rec.State != runrecord.Bound || rec.ChildUID != cuid {
		t.Fatalf("record %+v", rec)
	}
}
