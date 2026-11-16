package m0

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	PolicyKind      = "swarma-dispatcher m0-policy/1"
	SigningPlanKind = "swarma-dispatcher m0-signing-plan/1"
	SigningProfile  = "dispatcher-request/1"
)

// Bounds are the finite policy limits. max_workers and max_physical_starts are
// the constant 1 and are not stored.
type Bounds struct {
	MaxRuntimeSeconds int64
	DetectSeconds     int64
	StopSeconds       int64
}

// Policy is one decoded protected limited policy body. It is local authority
// data, not an owner-signature or installation envelope.
type Policy struct {
	ID            string
	Revision      uint64
	Receiver      string
	Association   Association
	Worker        Worker
	Plan          Plan
	Catalog       string
	Qualification string
	Bounds        Bounds
	NotBefore     int64
	NotAfter      int64
	Canonical     []byte
	Digest        string
}

// DecodePolicy strictly decodes a policy body and checks the cross-field
// bounds of section 2: not_before < not_after, window <= 3600 s, max_runtime
// 1..3600, detect 1..30, stop 1..270, detect + stop <= 300, operations exactly
// ["dispatch","cancel"], exactly one worker and one physical start.
func DecodePolicy(b []byte) (*Policy, error) {
	v, canon, err := canonicalize(b, policyLimits)
	if err != nil {
		return nil, err
	}
	if err := v.exactly("association", "catalog", "id", "kind", "limits", "not_after", "not_before", "operations", "plan", "qualification", "receiver", "revision", "v", "worker"); err != nil {
		return nil, err
	}
	if err := versionMember(v); err != nil {
		return nil, err
	}
	if err := constMember(v, "kind", PolicyKind); err != nil {
		return nil, err
	}
	p := &Policy{Canonical: canon}
	if p.ID, err = idMember(v, "id"); err != nil {
		return nil, err
	}
	if p.Revision, err = serialMember(v, "revision"); err != nil {
		return nil, err
	}
	if p.Receiver, err = idMember(v, "receiver"); err != nil {
		return nil, err
	}
	if p.Association, err = decodeAssociation(v.member("association")); err != nil {
		return nil, err
	}
	if p.Worker, err = decodeWorker(v.member("worker")); err != nil {
		return nil, err
	}
	if p.Plan, err = decodePlan(v.member("plan")); err != nil {
		return nil, err
	}
	if p.Catalog, err = digestMember(v, "catalog"); err != nil {
		return nil, err
	}
	if p.Qualification, err = digestMember(v, "qualification"); err != nil {
		return nil, err
	}
	ops := v.member("operations")
	if ops == nil || ops.kind != kindArray || len(ops.items) != 2 {
		return nil, invalid("operations must be exactly dispatch and cancel")
	}
	if d, ok := ops.items[0].text(); !ok || d != string(OpDispatch) {
		return nil, invalid("operations must be exactly dispatch and cancel")
	}
	if c, ok := ops.items[1].text(); !ok || c != string(OpCancel) {
		return nil, invalid("operations must be exactly dispatch and cancel")
	}
	lim := v.member("limits")
	if err := lim.exactly("detect_seconds", "max_physical_starts", "max_runtime_seconds", "max_workers", "stop_seconds"); err != nil {
		return nil, err
	}
	if n, ok := lim.member("max_workers").number(); !ok || n != 1 {
		return nil, invalid("max_workers must be 1")
	}
	if n, ok := lim.member("max_physical_starts").number(); !ok || n != 1 {
		return nil, invalid("max_physical_starts must be 1")
	}
	var ok bool
	if p.Bounds.MaxRuntimeSeconds, ok = lim.member("max_runtime_seconds").number(); !ok || p.Bounds.MaxRuntimeSeconds < 1 || p.Bounds.MaxRuntimeSeconds > maxRuntime {
		return nil, invalid("max_runtime_seconds outside 1..3600")
	}
	if p.Bounds.DetectSeconds, ok = lim.member("detect_seconds").number(); !ok || p.Bounds.DetectSeconds < 1 || p.Bounds.DetectSeconds > maxDetect {
		return nil, invalid("detect_seconds outside 1..30")
	}
	if p.Bounds.StopSeconds, ok = lim.member("stop_seconds").number(); !ok || p.Bounds.StopSeconds < 1 || p.Bounds.StopSeconds > maxStop {
		return nil, invalid("stop_seconds outside 1..270")
	}
	if p.Bounds.DetectSeconds+p.Bounds.StopSeconds > maxEnvelope {
		return nil, invalid("detect_seconds plus stop_seconds exceeds 300")
	}
	if p.NotBefore, ok = v.member("not_before").number(); !ok || p.NotBefore < 0 || p.NotBefore > maxUnixSecond {
		return nil, invalid("not_before outside the timestamp range")
	}
	if p.NotAfter, ok = v.member("not_after").number(); !ok || p.NotAfter < 1 || p.NotAfter > maxUnixSecond {
		return nil, invalid("not_after outside the timestamp range")
	}
	if p.NotBefore >= p.NotAfter {
		return nil, invalid("not_before must precede not_after")
	}
	if p.NotAfter-p.NotBefore > maxWindow {
		return nil, invalid("validity window exceeds 3600 s")
	}
	sum := sha256.Sum256(canon)
	p.Digest = digestPrefix + hex.EncodeToString(sum[:])
	return p, nil
}

// SigningPlan is one protected typed dispatcher adapter plan. PlanID is
// "sha256:" plus the lowercase SHA-256 of CCJ-1 of the entire plan object.
type SigningPlan struct {
	Request   *Request
	Canonical []byte
	PlanID    string
}

// DecodeSigningPlan strictly decodes a signing plan: exactly v, kind, profile
// and an unsigned request. A request carrying auth or any extra member refuses.
func DecodeSigningPlan(b []byte) (*SigningPlan, error) {
	v, canon, err := canonicalize(b, requestLimits)
	if err != nil {
		return nil, err
	}
	if err := v.exactly("kind", "profile", "request", "v"); err != nil {
		return nil, err
	}
	if err := versionMember(v); err != nil {
		return nil, err
	}
	if err := constMember(v, "kind", SigningPlanKind); err != nil {
		return nil, err
	}
	if err := constMember(v, "profile", SigningProfile); err != nil {
		return nil, err
	}
	r, err := decodeUnsigned(v.member("request"))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canon)
	return &SigningPlan{Request: r, Canonical: canon, PlanID: digestPrefix + hex.EncodeToString(sum[:])}, nil
}

// Catalog is the protected catalog of at most two signing plans, one per
// operation, with the cancel target pinned to the selected dispatch ID.
type Catalog struct {
	Dispatch *SigningPlan
	Cancel   *SigningPlan
}

// Qualified are the receiver-side intervals and slack that must be installed
// and qualified before any start: the C-to-exec-or-abort handoff, the maximum
// residual worker-code interval after expiry, the executor-entry slack and the
// independently installed stop authority during dispatcher failure.
type Qualified struct {
	HandoffMs       int64
	ResidualMs      int64
	SlackMs         int64
	IndependentStop bool
}

// Installed is what a protected policy source reports. InstallRevision is the
// monotonic installation counter advanced by every install, replacement and
// removal; Removed marks a published tombstone whose body is retained for
// owned-run cancel.
type Installed struct {
	Policy          *Policy
	Catalog         Catalog
	Qualified       Qualified
	InstallRevision uint64
	Removed         bool
}

// validate checks the installed tuple against the policy: both plans pin the
// policy's receiver, policy ID/revision, association, worker and plan; the
// cancel plan targets the dispatch plan; the qualified bounds fit the policy's
// stop bound. A missing plan or qualification refuses.
func (in *Installed) validate() error {
	if in == nil || in.Policy == nil {
		return refuse(CodePolicyNotInstalled, "no installed policy")
	}
	p := in.Policy
	for _, plan := range []struct {
		name string
		sp   *SigningPlan
		op   Op
	}{{"dispatch", in.Catalog.Dispatch, OpDispatch}, {"cancel", in.Catalog.Cancel, OpCancel}} {
		if plan.sp == nil || plan.sp.Request == nil {
			return refuse(CodePolicyMismatch, "catalog lacks the "+plan.name+" signing plan")
		}
		r := plan.sp.Request
		if r.Op != plan.op || r.Receiver != p.Receiver || r.Policy != (PolicyPin{ID: p.ID, Revision: p.Revision}) || r.Association != p.Association || r.Worker != p.Worker || r.Plan != p.Plan {
			return refuse(CodePolicyMismatch, "catalog "+plan.name+" plan pins differ from the policy")
		}
	}
	if in.Catalog.Cancel.Request.DispatchRequestID != in.Catalog.Dispatch.Request.RequestID {
		return refuse(CodePolicyMismatch, "cancel plan does not target the selected dispatch ID")
	}
	q := in.Qualified
	if q.HandoffMs < 1 || q.ResidualMs < 1 || q.SlackMs < 1 || !q.IndependentStop {
		return refuse(CodeQualificationMissing, "handoff, residual, slack or independent stop is not qualified")
	}
	if q.HandoffMs+q.ResidualMs > p.Bounds.StopSeconds*1000 {
		return refuse(CodeQualificationMissing, "handoff plus residual exceeds the policy stop bound")
	}
	return nil
}

// PolicySource is the protected installation seam. Only the protected operator
// path installs, replaces or removes policy, association, catalog or
// qualification pins; this interface reads that installation.
type PolicySource interface {
	Current() (*Installed, error)
}

// UnpinnedPolicySource refuses: no protected installation path is published.
type UnpinnedPolicySource struct{}

func (UnpinnedPolicySource) Current() (*Installed, error) { return nil, Unpinned(SeamPolicySource) }
