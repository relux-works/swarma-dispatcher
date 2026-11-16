package sweep_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

type exportResult struct {
	status sweep.WorkProductExportResult
	err    error
}

func (r exportResult) ExportStatus(string) (sweep.WorkProductExportResult, error) {
	return r.status, r.err
}

// Claim: only explicit exported/no-working-copy results plus proof and ACK release; missing, zero, unknown, partial and failed export results stay quarantined.
func TestWorkProductExport(t *testing.T) {
	p := tree(t)
	proof := sweep.Enumerate(p.View(), ri)
	if !proof.Proved() {
		t.Fatal("positive zero-process control failed")
	}
	for _, c := range []struct {
		name string
		seam sweep.WorkProductExport
		want bool
	}{
		{"exported", exportResult{sweep.Exported, nil}, true},
		{"no-working-copy", exportResult{sweep.NoWorkingCopy, nil}, true},
		{"not-proved", exportResult{sweep.ExportNotProved, nil}, false},
		{"zero", exportResult{}, false},
		{"unknown", exportResult{"unknown", nil}, false},
		{"partial", exportResult{"exported_partial", nil}, false},
		{"failed-export", exportResult{sweep.Exported, errors.New("synthetic export failure")}, false},
		{"failed-no-working-copy", exportResult{sweep.NoWorkingCopy, errors.New("synthetic status failure")}, false},
		{"missing-seam", nil, false},
		{"nil-table", sweep.StaticWorkProductExport(nil), false},
		{"missing-entry", sweep.StaticWorkProductExport{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newQuarantine(t, sweep.ReleaseGate{WorkProductExport: c.seam, Operator: sweep.StaticOperatorRelease{}})
			q.Hold(ri, "run")
			d, err := q.Release("run", ri, true, proof)
			if d.Release != c.want || d.WorkProductExportSatisfied != c.want || q.Held(ri) == c.want || (c.want && err != nil) || (!c.want && !errors.Is(err, sweep.ErrQuarantined)) {
				t.Fatalf("decision %+v error %v", d, err)
			}
			if !c.want && d.WorkProductExport != sweep.ExportNotProved {
				t.Fatal("failed/unknown export not normalized to not proved")
			}
		})
	}
}

// Claim: export still requires an ACK, operator release does not, and neither path bypasses zero-process proof for the same uid.
func TestExportReleaseGates(t *testing.T) {
	p := tree(t)
	proof := sweep.Enumerate(p.View(), ri)
	add(t, p, 60, [4]uint32{ri, ri, ri, ri})
	live := sweep.Enumerate(p.View(), ri)
	wrongUID := sweep.Enumerate(p.View(), ri+1)
	if err := os.WriteFile(filepath.Join(p.Root, "60", "status"), []byte("invalid status"), 0600); err != nil {
		t.Fatal(err)
	}
	unreadable := sweep.Enumerate(p.View(), ri)
	if !proof.Proved() || !live.Qualified() || len(live.Matched) != 1 || !unreadable.Qualified() || len(unreadable.Unreadable) != 1 {
		t.Fatal("fixture scans not qualified")
	}
	failedProof := proof
	failedProof.Err = errors.New("synthetic proof failure")
	if !wrongUID.Proved() || !failedProof.Qualified() {
		t.Fatal("proof neighbours are not qualified")
	}
	for _, c := range []struct {
		name          string
		ack, operator bool
		proof         sweep.Enumeration
		want          bool
	}{
		{"export-and-ack", true, false, proof, true},
		{"export-no-ack", false, false, proof, false},
		{"operator-no-ack", false, true, proof, true},
		{"export-live", true, false, live, false},
		{"operator-live", false, true, live, false},
		{"operator-unreadable", false, true, unreadable, false},
		{"operator-wrong-uid", false, true, wrongUID, false},
		{"export-failed-proof", true, false, failedProof, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			operator := sweep.StaticOperatorRelease{}
			if c.operator {
				operator["run"] = ri
			}
			q := newQuarantine(t, sweep.ReleaseGate{WorkProductExport: sweep.StaticWorkProductExport{"run": sweep.Exported}, Operator: operator})
			q.Hold(ri, "run")
			d, err := q.Release("run", ri, c.ack, c.proof)
			if d.Release != c.want || q.Held(ri) == c.want || (c.want && err != nil) || (!c.want && !errors.Is(err, sweep.ErrQuarantined)) {
				t.Fatalf("decision %+v error %v", d, err)
			}
		})
	}
}
