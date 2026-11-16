package m0

import "errors"

// Code is a refusal code. Codes listed in dispatcher Appendix A keep their
// published meaning. Codes prefixed m0_ are DRAFT proposals of this runtime and
// are not published refusal codes; m0_start_invalidated is the outcome named in
// spec/m0-launch.md section 3, still pending result/error publication.
type Code string

const (
	CodeRequestInvalid     Code = "request_invalid"
	CodeRequestTooLarge    Code = "request_too_large"
	CodeSignatureInvalid   Code = "signature_invalid"
	CodeKeyNotAssociated   Code = "caller_key_not_associated"
	CodeCallerNotPermitted Code = "caller_not_permitted"
	CodeRequestIDConflict  Code = "request_id_conflict"
	CodePlanDigestMismatch Code = "plan_digest_mismatch"
	CodeRunNotFound        Code = "run_not_found"
	CodeRunNotOwned        Code = "run_not_owned"
	CodeRunTerminal        Code = "run_terminal"
	CodeObservationUnknown Code = "observation_unknown"

	CodeClosed               Code = "m0_closed"
	CodeSeamUnpinned         Code = "m0_seam_unpinned"
	CodePolicyNotInstalled   Code = "m0_policy_not_installed"
	CodePolicyMismatch       Code = "m0_policy_mismatch"
	CodePolicyWindow         Code = "m0_policy_window"
	CodePolicyRollback       Code = "m0_policy_rollback"
	CodePolicyRevisionReused Code = "m0_policy_revision_reused"
	CodePolicyRemoved        Code = "m0_policy_removed"
	CodeBudgetConsumed       Code = "m0_launch_budget_consumed"
	CodeGenerationMismatch   Code = "m0_generation_mismatch"
	CodeTimeUnknown          Code = "m0_time_unknown"
	CodeDeadlineExhausted    Code = "m0_deadline_exhausted"
	CodeQualificationMissing Code = "m0_qualification_missing"
	CodeStartInvalidated     Code = "m0_start_invalidated"
	CodeLateStart            Code = "m0_late_start_refused"
	CodeReserveExhausted     Code = "m0_reserve_exhausted"
	CodeStorageFailed        Code = "m0_storage_failed"
	CodeRecoveryHeld         Code = "m0_recovery_held"
	CodeStateInconsistent    Code = "m0_state_inconsistent"
	CodeUnsupportedPlatform  Code = "m0_unsupported_platform"
)

// Seam names the unpinned dependency a refusal stands in for. The text is the
// refusal detail, so a refusing seam is always identifiable by name.
type Seam string

const (
	SeamLauncher     Seam = "launcher: user-manager user-launcher intake (FD3 plan, exec profile, start, stop, effect.observe); slice S1, unpublished"
	SeamRunner       Seam = "runner argv: broker Claude runner variant; slice S3a, unpublished"
	SeamClock        Seam = "clock: qualified time and deadline source"
	SeamGeneration   Seam = "generation reader: current account generation from the protected user-manager journal"
	SeamPolicySource Seam = "policy source: protected operator installation of the limited policy, catalog and qualification"
	SeamEntry        Seam = "executor entry: protected catalog selection of the worker input body and installed mapping inputs"
)

// Refusal is the typed refusal of this package. Detail never carries caller
// bytes; it names the violated rule or the refusing seam.
type Refusal struct {
	Code   Code
	Detail string
	Seam   Seam
	Cause  error
}

func (r *Refusal) Error() string {
	if r == nil {
		return "m0: <nil>"
	}
	s := "m0: " + string(r.Code)
	if r.Detail != "" {
		s += ": " + r.Detail
	}
	return s
}

func (r *Refusal) Unwrap() error {
	if r == nil {
		return nil
	}
	return r.Cause
}

func refuse(code Code, detail string) *Refusal { return &Refusal{Code: code, Detail: detail} }

// Unpinned is the typed refusal every default seam returns.
func Unpinned(s Seam) *Refusal {
	return &Refusal{Code: CodeSeamUnpinned, Detail: string(s), Seam: s}
}

// CodeOf returns the refusal code carried by err, or "" when err carries none.
func CodeOf(err error) Code {
	var r *Refusal
	if errors.As(err, &r) && r != nil {
		return r.Code
	}
	return ""
}

// asRefusal keeps an existing refusal and otherwise wraps err under fallback.
func asRefusal(err error, fallback Code, detail string) *Refusal {
	var r *Refusal
	if errors.As(err, &r) && r != nil {
		return r
	}
	return &Refusal{Code: fallback, Detail: detail, Cause: err}
}
