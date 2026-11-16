package sweep_test

import (
	"errors"
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
			q := sweep.Quarantine{Gate: sweep.ReleaseGate{WorkProductExport: c.seam, Operator: sweep.StaticOperatorRelease{}}}
			q.Hold(ri)
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
	for _, c := range []struct {
		name          string
		ack, operator bool
		proof         sweep.Enumeration
		want          bool
	}{
		{"export-and-ack", true, false, proof, true},
		{"export-no-ack", false, false, proof, false},
		{"operator-no-ack", false, true, proof, true},
		{"export-live", true, false, sweep.Enumeration{UID: ri, Matched: []int{1}}, false},
		{"operator-live", false, true, sweep.Enumeration{UID: ri, Matched: []int{1}}, false},
		{"operator-unreadable", false, true, sweep.Enumeration{UID: ri, Unreadable: []int{1}}, false},
		{"operator-wrong-uid", false, true, sweep.Enumeration{UID: ri + 1}, false},
		{"export-failed-proof", true, false, sweep.Enumeration{UID: ri, Err: errors.New("synthetic proof failure")}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			operator := sweep.StaticOperatorRelease{}
			if c.operator {
				operator["run"] = ri
			}
			q := sweep.Quarantine{Gate: sweep.ReleaseGate{WorkProductExport: sweep.StaticWorkProductExport{"run": sweep.Exported}, Operator: operator}}
			q.Hold(ri)
			d, err := q.Release("run", ri, c.ack, c.proof)
			if d.Release != c.want || q.Held(ri) == c.want || (c.want && err != nil) || (!c.want && !errors.Is(err, sweep.ErrQuarantined)) {
				t.Fatalf("decision %+v error %v", d, err)
			}
		})
	}
}
