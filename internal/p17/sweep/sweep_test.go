package sweep_test

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

const ri = uint32(2001)

func tree(t *testing.T) *fakes.ProcTree {
	t.Helper()
	p, err := fakes.NewProcTree(t.TempDir(), "boot-1")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func add(t *testing.T, p *fakes.ProcTree, pid int, uids [4]uint32) {
	t.Helper()
	if err := p.Add(pid, uids, 100); err != nil {
		t.Fatal(err)
	}
}

// Claim (D5a): the proof enumerates all processes and matches on the real,
// effective, saved OR filesystem uid; a setuid child (ruid R_i, other euid)
// and an euid-only match are both counted, other uids are not, and an empty
// tree proves zero.
func TestEnumerateMatchesEveryUIDField(t *testing.T) {
	p := tree(t)
	add(t, p, 10, [4]uint32{ri, ri, ri, ri})
	add(t, p, 11, [4]uint32{ri, 0, ri, 0})                   // setuid exec keeps ruid
	add(t, p, 12, [4]uint32{0, ri, 0, 0})                    // effective only
	add(t, p, 13, [4]uint32{0, 0, ri, 0})                    // saved only
	add(t, p, 14, [4]uint32{0, 0, 0, ri})                    // fs only
	add(t, p, 15, [4]uint32{0, 0, 0, 0})                     // root, unrelated
	add(t, p, 16, [4]uint32{ri + 1, ri + 1, ri + 1, ri + 1}) // neighbour uid
	e := sweep.Enumerate(p.Root, ri)
	want := []int{10, 11, 12, 13, 14}
	if e.Err != nil || e.Scanned != 7 || len(e.Unreadable) != 0 || len(e.Matched) != len(want) {
		t.Fatalf("%+v", e)
	}
	for i, pid := range want {
		if e.Matched[i] != pid {
			t.Fatalf("matched %v want %v", e.Matched, want)
		}
	}
	if e.Proved() {
		t.Fatal("proved with matches")
	}
	for _, pid := range want {
		if err := p.Remove(pid); err != nil {
			t.Fatal(err)
		}
	}
	if e := sweep.Enumerate(p.Root, ri); !e.Proved() || e.Scanned != 2 {
		t.Fatalf("positive control: %+v", e)
	}
}

// Claim (D5a): any pid that cannot be read counts as NOT proved even when no
// process matches; a missing procfs root is a failed enumeration, also not
// proved.
func TestEnumerateUnreadableNotProved(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file; unreadable plant needs a non-root test process")
	}
	p := tree(t)
	add(t, p, 20, [4]uint32{0, 0, 0, 0})
	if err := p.MakeUnreadable(20); err != nil {
		t.Fatal(err)
	}
	e := sweep.Enumerate(p.Root, ri)
	if e.Err != nil || len(e.Matched) != 0 || len(e.Unreadable) != 1 || e.Unreadable[0] != 20 || e.Proved() {
		t.Fatalf("%+v", e)
	}
	if e := sweep.Enumerate(p.Root+"/missing", ri); e.Err == nil || e.Proved() {
		t.Fatalf("missing root: %+v", e)
	}
}

// Claim (D5b, D5c): the sweep kills by uid through the helper and never
// names a pid; a process of another uid holding a recycled pid from the old
// record survives, and the planted setuid child under R_i is killed.
func TestSweepKillsByUIDOnly(t *testing.T) {
	p := tree(t)
	const recycled = 4242
	add(t, p, recycled, [4]uint32{ri + 7, ri + 7, ri + 7, ri + 7}) // another uid, old pid
	add(t, p, 30, [4]uint32{ri, ri, ri, ri})
	add(t, p, 31, [4]uint32{ri, 0, ri, 0}) // setuid child
	k := &fakes.Killer{Tree: p}
	out := sweep.Sweep(context.Background(), p.Root, ri, k, sweep.Budget{Attempts: 3})
	if out.Err != nil || !out.Final.Proved() || out.Kills != 1 {
		t.Fatalf("%+v", out)
	}
	if uids := k.UIDs(); len(uids) != 1 || uids[0] != ri {
		t.Fatalf("killer asked for %v", uids)
	}
	if _, err := os.Stat(p.Root + "/4242/status"); err != nil {
		t.Fatalf("recycled pid of another uid was signalled: %v", err)
	}
	for _, pid := range []string{"30", "31"} {
		if _, err := os.Stat(p.Root + "/" + pid); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pid %s survived the uid sweep: %v", pid, err)
		}
	}
}

// Claim (D5a bounded budget): a child that survives every round leaves the
// proof unestablished after the budget, with exactly Attempts kill rounds and
// no further signalling; a failing or unpinned killer stops the sweep with
// its own error.
func TestSweepBudgetAndKillerFailures(t *testing.T) {
	p := tree(t)
	add(t, p, 40, [4]uint32{ri, ri, ri, ri})
	k := &fakes.Killer{Tree: p, Immortal: true}
	out := sweep.Sweep(context.Background(), p.Root, ri, k, sweep.Budget{Attempts: 2})
	if !errors.Is(out.Err, sweep.ErrNotProved) || out.Kills != 2 || out.Final.Proved() || len(k.UIDs()) != 2 {
		t.Fatalf("%+v kills=%v", out, k.UIDs())
	}
	failing := &fakes.Killer{Tree: p, Fail: errors.New("helper refused")}
	out = sweep.Sweep(context.Background(), p.Root, ri, failing, sweep.Budget{Attempts: 2})
	if out.Err == nil || errors.Is(out.Err, sweep.ErrNotProved) || out.Kills != 0 {
		t.Fatalf("failing killer: %+v", out)
	}
	out = sweep.Sweep(context.Background(), p.Root, ri, sweep.UnpinnedKiller{}, sweep.Budget{Attempts: 2})
	if !codes.IsUnpinned(out.Err) || out.Final.Proved() {
		t.Fatalf("unpinned killer: %+v", out)
	}
	// Positive control: with nothing under the uid, no killer is needed.
	out = sweep.Sweep(context.Background(), p.Root, ri+5, nil, sweep.Budget{Attempts: 1})
	if out.Err != nil || !out.Final.Proved() || out.Kills != 0 {
		t.Fatalf("empty uid: %+v", out)
	}
}

type failingWorkProductExport struct{ err error }

func (f failingWorkProductExport) ExportStatus(string) (sweep.WorkProductExportResult, error) {
	return sweep.Exported, f.err
}

// Claim: only proof AND (operator release OR (end ACK AND work-product export)) frees a uid; unreadable/live/wrong-uid/failed proof and missing evidence keep it quarantined.
func TestQuarantineRelease(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file; unreadable plant needs a non-root test process")
	}
	p := tree(t)
	exportEvidence := sweep.StaticWorkProductExport{}
	operator := sweep.StaticOperatorRelease{}
	q := sweep.Quarantine{Gate: sweep.ReleaseGate{WorkProductExport: exportEvidence, Operator: operator}}
	const run = "run-a"
	if err := q.Lease(ri); err != nil {
		t.Fatal(err)
	}
	if err := q.Lease(ri); !errors.Is(err, sweep.ErrQuarantined) {
		t.Fatalf("held uid leased again: %v", err)
	}
	proved := sweep.Enumerate(p.Root, ri)
	if !proved.Proved() {
		t.Fatal("setup: empty tree not proved")
	}
	refuse := func(name string, d sweep.ReleaseDecision, err error) {
		t.Helper()
		if !errors.Is(err, sweep.ErrQuarantined) || d.Release || !q.Held(ri) {
			t.Fatalf("%s: released: %+v err=%v", name, d, err)
		}
	}
	// Zero processes and end ACK, but no export or operator release.
	d, err := q.Release(run, ri, true, proved)
	refuse("no export or operator release", d, err)
	if d.WorkProductExportSatisfied || d.OperatorReleased || !d.Proved || !d.EndAcknowledged {
		t.Fatalf("decision inputs %+v", d)
	}
	// Export proved but one pid unreadable.
	exportEvidence[run] = sweep.Exported
	add(t, p, 60, [4]uint32{0, 0, 0, 0})
	if err := p.MakeUnreadable(60); err != nil {
		t.Fatal(err)
	}
	unreadable := sweep.Enumerate(p.Root, ri)
	if unreadable.Err != nil || len(unreadable.Unreadable) != 1 || unreadable.Proved() {
		t.Fatalf("setup: %+v", unreadable)
	}
	d, err = q.Release(run, ri, true, unreadable)
	refuse("export with an unreadable pid", d, err)
	if !d.WorkProductExportSatisfied || d.Proved {
		t.Fatalf("decision inputs %+v", d)
	}
	if err := p.Remove(60); err != nil {
		t.Fatal(err)
	}
	// Export and proof, but no end ACK.
	d, err = q.Release(run, ri, false, sweep.Enumerate(p.Root, ri))
	refuse("export without end ACK", d, err)
	// Export and ACK, but a live child under R_i.
	add(t, p, 50, [4]uint32{ri, ri, ri, ri})
	d, err = q.Release(run, ri, true, sweep.Enumerate(p.Root, ri))
	refuse("live child", d, err)
	if err := p.Remove(50); err != nil {
		t.Fatal(err)
	}
	// Proof for another uid, and a failed enumeration.
	d, err = q.Release(run, ri, true, sweep.Enumerate(p.Root, ri+1))
	refuse("another uid's proof", d, err)
	d, err = q.Release(run, ri, true, sweep.Enumerate(p.Root+"/missing", ri))
	refuse("failed enumeration", d, err)
	// Failed export status, and the refusing default.
	broken := sweep.Quarantine{Gate: sweep.ReleaseGate{WorkProductExport: failingWorkProductExport{errors.New("export evidence unavailable")}, Operator: operator}}
	broken.Hold(ri)
	d, err = broken.Release(run, ri, true, sweep.Enumerate(p.Root, ri))
	if !errors.Is(err, sweep.ErrQuarantined) || d.Release || d.WorkProductExportSatisfied || d.WorkProductExportErr == nil || !broken.Held(ri) {
		t.Fatalf("unreadable export evidence: %+v err=%v", d, err)
	}
	var unpinned sweep.Quarantine
	unpinned.Hold(ri)
	d, err = unpinned.Release(run, ri, true, sweep.Enumerate(p.Root, ri))
	if !errors.Is(err, sweep.ErrQuarantined) || d.Release || !codes.IsUnpinned(d.WorkProductExportErr) || !codes.IsUnpinned(d.OperatorErr) || !unpinned.Held(ri) {
		t.Fatalf("unpinned seams: %+v err=%v", d, err)
	}
	if err := q.Lease(ri); !errors.Is(err, sweep.ErrQuarantined) {
		t.Fatal("new run received a quarantined uid")
	}
	// Positive control: zero processes, proved export and end ACK.
	d, err = q.Release(run, ri, true, sweep.Enumerate(p.Root, ri))
	if err != nil || !d.Release || q.Held(ri) {
		t.Fatalf("positive control: %+v err=%v", d, err)
	}
	if err := q.Lease(ri); err != nil {
		t.Fatalf("lease after release: %v", err)
	}
	// Operator release: proof plus an explicit release for this run and uid,
	// with neither export nor end ACK; a release recorded for another uid
	// does not count.
	delete(exportEvidence, run)
	operator[run] = ri + 1
	d, err = q.Release(run, ri, false, sweep.Enumerate(p.Root, ri))
	refuse("operator release for another uid", d, err)
	operator[run] = ri
	d, err = q.Release(run, ri, false, sweep.Enumerate(p.Root, ri))
	if err != nil || !d.Release || !d.OperatorReleased || q.Held(ri) {
		t.Fatalf("operator release: %+v err=%v", d, err)
	}
	// Operator release never bypasses the proof.
	q.Hold(ri)
	add(t, p, 51, [4]uint32{0, ri, 0, 0})
	d, err = q.Release(run, ri, false, sweep.Enumerate(p.Root, ri))
	refuse("operator release with a live process", d, err)
}

// Claim: ReleaseAllowed is proof AND (operator OR (ACK AND export)); every other combination is false.
func TestReleaseAllowedTable(t *testing.T) {
	p := tree(t)
	proved := sweep.Enumerate(p.Root, ri)
	add(t, p, 70, [4]uint32{ri, ri, ri, ri})
	live := sweep.Enumerate(p.Root, ri)
	for _, c := range []struct {
		e                             sweep.Enumeration
		ack, exportEvidence, operator bool
		want                          bool
	}{
		{proved, true, true, false, true},
		{proved, false, false, true, true},
		{proved, true, false, true, true},
		{proved, true, false, false, false},
		{proved, false, true, false, false},
		{proved, false, false, false, false},
		{live, true, true, true, false},
	} {
		if got := sweep.ReleaseAllowed(c.e, c.ack, c.exportEvidence, c.operator); got != c.want {
			t.Fatalf("proved=%v ack=%v export=%v operator=%v: got %v", c.e.Proved(), c.ack, c.exportEvidence, c.operator, got)
		}
	}
}

// Claim (positive control on the real procfs): the enumeration for the test
// process's own uid contains the test process; it is not proved. Limit: on a
// host with hidepid other pids may be unreadable, which is also not proved,
// so only the positive match is asserted.
func TestEnumerateRealProc(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs is Linux only")
	}
	e := sweep.Enumerate("/proc", uint32(os.Getuid()))
	found := false
	for _, pid := range e.Matched {
		if pid == os.Getpid() {
			found = true
		}
	}
	if e.Err != nil || !found || e.Proved() {
		t.Fatalf("self not enumerated: err=%v matched=%d proved=%v", e.Err, len(e.Matched), e.Proved())
	}
}
