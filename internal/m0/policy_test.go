package m0

import "testing"

// Claim: the policy decoder enforces every cross-field bound of section 2 one
// step past the boundary and accepts the boundary: window 3600/3601, reversed
// or equal times, runtime 3600/3601 and 0, detect 30/31, stop 270/271,
// detect+stop 300/301, a second operation ordering, and timestamps past
// 253402300799.
func TestPolicyBounds(t *testing.T) {
	base := parseDoc(t, fixture(t, "m0-launch", "policy.valid.json"))
	try := func(edits map[string]*value) error {
		v := parseDoc(t, base.encode(nil))
		for p, nv := range edits {
			setPath(t, v, p, nv)
		}
		_, err := DecodePolicy(v.encode(nil))
		return err
	}
	if err := try(nil); err != nil {
		t.Fatalf("POLICY: control refused: %v", err)
	}
	nb := int64(1792972800)
	for name, tc := range map[string]struct {
		edits map[string]*value
		ok    bool
	}{
		"window-max":        {map[string]*value{"not_after": integer(nb + 3600)}, true},
		"window-over":       {map[string]*value{"not_after": integer(nb + 3601)}, false},
		"window-reversed":   {map[string]*value{"not_after": integer(nb - 1)}, false},
		"window-equal":      {map[string]*value{"not_after": integer(nb)}, false},
		"runtime-max":       {map[string]*value{"limits.max_runtime_seconds": integer(3600)}, true},
		"runtime-over":      {map[string]*value{"limits.max_runtime_seconds": integer(3601)}, false},
		"runtime-zero":      {map[string]*value{"limits.max_runtime_seconds": integer(0)}, false},
		"detect-max":        {map[string]*value{"limits.detect_seconds": integer(30)}, true},
		"detect-over":       {map[string]*value{"limits.detect_seconds": integer(31)}, false},
		"stop-max":          {map[string]*value{"limits.stop_seconds": integer(270)}, true},
		"stop-over":         {map[string]*value{"limits.stop_seconds": integer(271)}, false},
		"envelope-max":      {map[string]*value{"limits.detect_seconds": integer(30), "limits.stop_seconds": integer(270)}, true},
		"two-starts":        {map[string]*value{"limits.max_physical_starts": integer(2)}, false},
		"two-workers":       {map[string]*value{"limits.max_workers": integer(2)}, false},
		"ops-reversed":      {map[string]*value{"operations": array(text("cancel"), text("dispatch"))}, false},
		"ops-one":           {map[string]*value{"operations": array(text("dispatch"))}, false},
		"timestamp-max":     {map[string]*value{"not_before": integer(253402300799 - 1), "not_after": integer(253402300799)}, true},
		"timestamp-over":    {map[string]*value{"not_before": integer(253402300799), "not_after": integer(253402300800)}, false},
		"not-before-zero":   {map[string]*value{"not_before": integer(0), "not_after": integer(3600)}, true},
		"not-before-negate": {map[string]*value{"not_before": integer(-1), "not_after": integer(3599)}, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := try(tc.edits)
			if tc.ok && err != nil {
				t.Errorf("POLICY: boundary refused: %v", err)
			}
			if !tc.ok && CodeOf(err) != CodeRequestInvalid {
				t.Errorf("POLICY: mutant accepted: %v", err)
			}
		})
	}
}

// Claim: the signing plan's plan_id is sha256: of CCJ-1 of the whole plan
// object, stable under whitespace and order; the installed tuple refuses a
// missing plan, a plan whose pins differ from the policy, a cancel plan that
// targets another dispatch ID, and missing or oversized qualification, each
// beside the accepted control built from the fixtures.
func TestInstalledValidation(t *testing.T) {
	plan, err := DecodeSigningPlan(fixture(t, "m0-launch", "signing-plan.valid.json"))
	must(t, err)
	again, err := DecodeSigningPlan([]byte(" " + string(plan.Canonical) + "\n"))
	must(t, err)
	if plan.PlanID != again.PlanID || !isDigest(plan.PlanID) {
		t.Fatal("PLAN-ID: unstable or malformed")
	}
	k := newKey(t)
	in := specimenInput(t)
	_, workerPlan, err := in.Canonical()
	must(t, err)
	control := buildInstalled(t, k, workerPlan)
	if err := control.validate(); err != nil {
		t.Fatalf("INSTALLED: control refused: %v", err)
	}
	for name, tc := range map[string]struct {
		edit func(*Installed)
		code Code
	}{
		"no-dispatch-plan": {func(i *Installed) { i.Catalog.Dispatch = nil }, CodePolicyMismatch},
		"no-cancel-plan":   {func(i *Installed) { i.Catalog.Cancel = nil }, CodePolicyMismatch},
		"plan-pin-differs": {func(i *Installed) {
			p := *i.Catalog.Dispatch
			r := *p.Request
			r.Receiver = "ffffffffffffffffffffffffffffffff"
			p.Request = &r
			i.Catalog.Dispatch = &p
		}, CodePolicyMismatch},
		"cancel-other-target": {func(i *Installed) {
			p := *i.Catalog.Cancel
			r := *p.Request
			r.DispatchRequestID = "ffffffffffffffffffffffffffffffff"
			p.Request = &r
			i.Catalog.Cancel = &p
		}, CodePolicyMismatch},
		"no-handoff":          {func(i *Installed) { i.Qualified.HandoffMs = 0 }, CodeQualificationMissing},
		"no-independent-stop": {func(i *Installed) { i.Qualified.IndependentStop = false }, CodeQualificationMissing},
		"residual-too-long":   {func(i *Installed) { i.Qualified.ResidualMs = 270*1000 - i.Qualified.HandoffMs + 1 }, CodeQualificationMissing},
		"no-policy":           {func(i *Installed) { i.Policy = nil }, CodePolicyNotInstalled},
	} {
		t.Run(name, func(t *testing.T) {
			i := *control
			tc.edit(&i)
			wantCode(t, i.validate(), tc.code)
		})
	}
	var absent *Installed
	wantCode(t, absent.validate(), CodePolicyNotInstalled)
}
