package sweep_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Claim (D5a view qualification): with no process under R_i in the listing,
// each disqualifying procfs view (hidepid 1, 2, noaccess, invisible without
// a matching gid=, hidepid=ptraceable even with a matching gid=, subset=pid,
// a mount at ProcRoot that is not procfs, a procfs subtree mount, no mount at
// ProcRoot, unreadable mountinfo, pid 1 not listed, NSpid with two fields,
// no NSpid line) reports ErrViewUnqualified and is NOT proved, while its
// qualifying neighbour (the same tree with only that one condition restored:
// hidepid=0/off, hidepid with gid= equal to the launcher gid, a whole proc
// mount, pid 1 present, single-field NSpid) is proved.
func TestViewQualification(t *testing.T) {
	const gid = uint32(120)
	own := func() uint32 { return gid }
	type tc struct {
		name      string
		plant     func(t *testing.T, p *fakes.ProcTree)
		neighbour func(t *testing.T, p *fakes.ProcTree)
	}
	mount := func(fstype, root, opts string) func(*testing.T, *fakes.ProcTree) {
		return func(t *testing.T, p *fakes.ProcTree) {
			t.Helper()
			if err := p.SetMount(fstype, root, opts); err != nil {
				t.Fatal(err)
			}
		}
	}
	nspid := func(fields string) func(*testing.T, *fakes.ProcTree) {
		return func(t *testing.T, p *fakes.ProcTree) {
			t.Helper()
			if err := p.SetNSpid(fields); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []tc{
		{"hidepid=2", mount("proc", "/", "rw,hidepid=2"), mount("proc", "/", "rw,hidepid=0")},
		{"hidepid=invisible", mount("proc", "/", "rw,hidepid=invisible"), mount("proc", "/", "rw,hidepid=off")},
		{"hidepid=1", mount("proc", "/", "rw,hidepid=1"), mount("proc", "/", "rw")},
		{"hidepid=noaccess", mount("proc", "/", "rw,hidepid=noaccess"), mount("proc", "/", "rw,hidepid=noaccess,gid=120")},
		{"hidepid=2 with another gid", mount("proc", "/", "rw,gid=121,hidepid=2"), mount("proc", "/", "rw,gid=120,hidepid=2")},
		{"hidepid=invisible with another gid", mount("proc", "/", "rw,hidepid=invisible,gid=0"), mount("proc", "/", "rw,hidepid=invisible,gid=120")},
		{"hidepid=ptraceable with the launcher gid", mount("proc", "/", "rw,hidepid=ptraceable,gid=120"), mount("proc", "/", "rw,gid=120")},
		{"hidepid=4", mount("proc", "/", "rw,hidepid=4"), mount("proc", "/", "rw,hidepid=0")},
		{"unknown hidepid value", mount("proc", "/", "rw,hidepid=7"), mount("proc", "/", "rw")},
		{"subset=pid", mount("proc", "/", "rw,subset=pid"), mount("proc", "/", "rw")},
		{"not a procfs mount", mount("tmpfs", "/", "rw"), mount("proc", "/", "rw")},
		{"procfs subtree mount", mount("proc", "/sys", "rw"), mount("proc", "/", "rw")},
		{"no mount at the root", func(t *testing.T, p *fakes.ProcTree) {
			if err := p.SetMountInfo("21 1 8:1 / / rw - ext4 /dev/root rw\n22 21 0:21 / " + p.Root + "-other rw - proc proc rw\n"); err != nil {
				t.Fatal(err)
			}
		}, mount("proc", "/", "rw")},
		{"a later non-proc mount covers the root", func(t *testing.T, p *fakes.ProcTree) {
			if err := p.SetMountInfo("22 21 0:21 / " + p.Root + " rw - proc proc rw\n23 22 0:40 / " + p.Root + " rw - tmpfs tmpfs rw\n"); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, p *fakes.ProcTree) {
			if err := p.SetMountInfo("23 22 0:40 / " + p.Root + " rw - tmpfs tmpfs rw\n22 21 0:21 / " + p.Root + " rw - proc proc rw\n"); err != nil {
				t.Fatal(err)
			}
		}},
		{"mountinfo missing", func(t *testing.T, p *fakes.ProcTree) {
			if err := os.Remove(p.MountInfoPath()); err != nil {
				t.Fatal(err)
			}
		}, mount("proc", "/", "rw")},
		{"pid 1 not listed", func(t *testing.T, p *fakes.ProcTree) {
			if err := p.Remove(1); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, p *fakes.ProcTree) {
			if err := p.Add(1, [4]uint32{0, 0, 0, 0}, 1); err != nil {
				t.Fatal(err)
			}
		}},
		{"pid 1 status missing", func(t *testing.T, p *fakes.ProcTree) {
			if err := os.Remove(filepath.Join(p.Root, "1", "status")); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, p *fakes.ProcTree) {
			if err := p.Add(1, [4]uint32{0, 0, 0, 0}, 1); err != nil {
				t.Fatal(err)
			}
		}},
		{"NSpid with two fields", nspid("4242 7"), nspid("4242")},
		{"no NSpid line", nspid(""), nspid("7")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := tree(t)
			add(t, p, 15, [4]uint32{0, 0, 0, 0})
			v := p.View()
			v.GID = own
			if e := sweep.Enumerate(v, ri); !e.Proved() || !e.Qualified() {
				t.Fatalf("setup: fresh fixture view not proved: %+v", e)
			}
			c.plant(t, p)
			e := sweep.Enumerate(v, ri)
			if e.Proved() || e.Qualified() || !errors.Is(e.Err, sweep.ErrViewUnqualified) || len(e.Matched) != 0 {
				t.Fatalf("unqualified view trusted: proved=%v qualified=%v err=%v", e.Proved(), e.Qualified(), e.Err)
			}
			if err := v.Qualify(); !errors.Is(err, sweep.ErrViewUnqualified) {
				t.Fatalf("Qualify: %v", err)
			}
			c.neighbour(t, p)
			if e := sweep.Enumerate(v, ri); !e.Proved() || e.Err != nil {
				t.Fatalf("qualifying neighbour not proved: %+v", e)
			}
		})
	}
}

// Claim (D5a, the release input): on an unqualified view (hidepid=2, which
// hides R_i's pids instead of making them unreadable) the sweep is not
// proved, the release gate keeps the uid quarantined even with an end ACK
// and export evidence, a pre-record lease is not returned, and a hand-built
// Enumeration cannot report Proved either; on the qualifying neighbour
// (hidepid=0) the same calls prove zero, release the uid and return the lease.
func TestUnqualifiedViewNeverReleases(t *testing.T) {
	p := tree(t)
	const run = "run-a"
	q := newQuarantine(t, sweep.ReleaseGate{WorkProductExport: sweep.StaticWorkProductExport{run: sweep.Exported}})
	if err := q.Hold(ri, run); err != nil {
		t.Fatal(err)
	}
	if err := p.SetMount("proc", "/", "rw,hidepid=2"); err != nil {
		t.Fatal(err)
	}
	out := sweep.Sweep(context.Background(), p.View(), ri, &fakes.Killer{Tree: p}, sweep.Budget{Attempts: 2})
	if out.Final.Proved() || !errors.Is(out.Err, sweep.ErrViewUnqualified) {
		t.Fatalf("sweep on a hidepid view: %+v", out)
	}
	if d, err := q.Release(run, ri, true, out.Final); !errors.Is(err, sweep.ErrQuarantined) || d.Release || d.Proved || d.Reason != "zero-process proof not established" || !q.Held(ri) {
		t.Fatalf("released from an unqualified view: %+v %v", d, err)
	}
	if err := q.ReturnUnused(ri, out.Final, run); !errors.Is(err, sweep.ErrQuarantined) || !q.Held(ri) {
		t.Fatalf("lease returned from an unqualified view: %v", err)
	}
	if (sweep.Enumeration{UID: ri}).Proved() {
		t.Fatal("a hand-built enumeration is proved")
	}
	if err := q.ReturnUnused(ri, sweep.Enumeration{UID: ri}, run); !errors.Is(err, sweep.ErrQuarantined) || !q.Held(ri) {
		t.Fatalf("lease returned from a hand-built enumeration: %v", err)
	}
	// Qualifying neighbour.
	if err := p.SetMount("proc", "/", "rw,hidepid=0"); err != nil {
		t.Fatal(err)
	}
	out = sweep.Sweep(context.Background(), p.View(), ri, &fakes.Killer{Tree: p}, sweep.Budget{Attempts: 2})
	if !out.Final.Proved() || out.Err != nil {
		t.Fatalf("neighbour sweep: %+v", out)
	}
	if err := q.ReturnUnused(ri, out.Final, run); err != nil || q.Held(ri) {
		t.Fatalf("neighbour lease not returned: %v", err)
	}
	q.Hold(ri, run)
	if d, err := q.Release(run, ri, true, out.Final); err != nil || !d.Release || q.Held(ri) {
		t.Fatalf("neighbour release: %+v %v", d, err)
	}
}

// Claim (pre-record lease return): ReturnUnused frees the uid only on a
// proved enumeration for that same uid; a live process under the uid and a
// proof for another uid each keep it quarantined.
func TestReturnUnusedNeedsProof(t *testing.T) {
	p := tree(t)
	q := newQuarantine(t, sweep.ReleaseGate{})
	if err := q.Lease(ri, "run"); err != nil {
		t.Fatal(err)
	}
	add(t, p, 80, [4]uint32{0, 0, ri, 0}) // saved uid only
	if err := q.ReturnUnused(ri, sweep.Enumerate(p.View(), ri), "run"); !errors.Is(err, sweep.ErrQuarantined) || !q.Held(ri) {
		t.Fatalf("returned with a live process: %v", err)
	}
	if err := q.ReturnUnused(ri, sweep.Enumerate(p.View(), ri+1), "run"); !errors.Is(err, sweep.ErrQuarantined) || !q.Held(ri) {
		t.Fatalf("returned on another uid's proof: %v", err)
	}
	if err := p.Remove(80); err != nil {
		t.Fatal(err)
	}
	// Positive control.
	if err := q.ReturnUnused(ri, sweep.Enumerate(p.View(), ri), "run"); err != nil || q.Held(ri) {
		t.Fatalf("positive control: %v", err)
	}
	if err := q.Lease(ri, "run"); err != nil {
		t.Fatalf("lease after return: %v", err)
	}
}

// Claim (pre-record lease return needs BOTH conditions): a lease for which
// the privilege-drop helper WAS invoked is not returned although the
// enumeration proves zero processes; the uid stays quarantined and cannot be
// leased. Positive control: helper never invoked and zero processes, the
// uid returns; and a new lease starts without the mark.
func TestReturnUnusedRefusesAfterHelper(t *testing.T) {
	p := tree(t)
	q := newQuarantine(t, sweep.ReleaseGate{})
	if err := q.Lease(ri, "run"); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkHelper(ri, "run"); err != nil {
		t.Fatal(err)
	}
	proof := sweep.Enumerate(p.View(), ri)
	if !proof.Proved() {
		t.Fatalf("setup: %+v", proof)
	}
	if err := q.ReturnUnused(ri, proof, "run"); !errors.Is(err, sweep.ErrQuarantined) || !q.Held(ri) || !q.HelperInvoked(ri) {
		t.Fatalf("returned after a helper invocation: %v", err)
	}
	if err := q.Lease(ri, "run"); !errors.Is(err, sweep.ErrQuarantined) {
		t.Fatalf("quarantined uid leased: %v", err)
	}
	// Positive control on a neighbour uid: no helper, zero processes.
	if err := q.Lease(ri+1, "next"); err != nil {
		t.Fatal(err)
	}
	if err := q.ReturnUnused(ri+1, sweep.Enumerate(p.View(), ri+1), "next"); err != nil || q.Held(ri+1) {
		t.Fatalf("positive control: %v", err)
	}
	if err := q.MarkHelper(ri+1, "next"); !errors.Is(err, sweep.ErrQuarantined) {
		t.Fatal("unheld helper mark accepted")
	}
	if err := q.Lease(ri+1, "next"); err != nil || q.HelperInvoked(ri+1) {
		t.Fatalf("a new lease kept the old helper mark: %v", err)
	}
}
