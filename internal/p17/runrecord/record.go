// Package runrecord is the durable P17 run record on internal/journal: one
// record per run, the states of r2.16, and atomic transitions. Each transition
// is a journal frame followed by the materialized record file. Every record
// loaded at restart starts out fenced.
//
// The record holds no secrets and no descriptors. Local success here proves
// only local durability of the recorded fact, never a Keymaker or Source
// outcome.
package runrecord

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
)

// State is an H6 record state. Refused certifies a correlated no-effect bind
// refusal. Unsent certifies that this process never called write for the bind
// frame because its post-hello guard elapsed. An attempted short, failed or
// timed-out write, even reporting zero bytes, can never justify Unsent.
// Neither state owes a Keymaker end or is adopted; native hello audit entries
// may still exist. Death before the durable Unsent update leaves Binding,
// HELD. Both terminal states return their uid only under the explicit
// helper-never-invoked AND qualified zero-process rule.
type State string

const (
	Binding State = "binding"
	Bound   State = "bound"
	Ending  State = "ending"
	Ended   State = "ended"
	Refused State = "refused"
	Unsent  State = "unsent"
)

// Known reports whether s is a record state.
func (s State) Known() bool {
	switch s {
	case Binding, Bound, Ending, Ended, Refused, Unsent:
		return true
	}
	return false
}

// Terminal reports whether s admits no further transition.
func (s State) Terminal() bool { return s == Ended || s == Refused || s == Unsent }

// Kind and Version identify the record schema.
const (
	Kind    = "p17-run-record/1"
	Version = 1
)

// Record is the durable run record (r2.16 H2 step 4 and D2).
type Record struct {
	Version int    `json:"v"`
	Kind    string `json:"kind"`
	// Key is the record key; it equals LaunchNonce.
	Key   string `json:"key"`
	State State  `json:"state"`

	LauncherUID         uint32 `json:"launcher_uid"`
	LauncherGeneration  string `json:"launcher_generation"`
	StationRegistration string `json:"identity_registration_number"`
	InputPrincipal      string `json:"input_principal"`
	// Origin is the immutable admission origin exactly as validate returned
	// it, or JSON null for a child run.
	Origin            json.RawMessage `json:"origin"`
	ParentRun         string          `json:"parent_run"`
	LaunchNonce       string          `json:"launch_nonce"`
	SourceIncarnation string          `json:"source_incarnation"`
	PreparedID        string          `json:"prepared_id"`
	BindRequestID     string          `json:"bind_request_id"`
	ChildUID          uint32          `json:"child_uid"`

	RunID string `json:"run_id"`

	Launcher *incarnation.Incarnation `json:"launcher,omitempty"`
	Child    *incarnation.Incarnation `json:"child,omitempty"`

	InputReleased   bool   `json:"input_released"`
	EndRequestID    string `json:"end_request_id"`
	EndRequestBody  []byte `json:"end_request_body"`
	TeardownDone    bool   `json:"teardown_done"`
	EndAcknowledged bool   `json:"end_acknowledged"`
}

// ErrRecord is the root of every record validation refusal.
var ErrRecord = errors.New("runrecord: invalid record")

func invalid(what string) error { return errors.Join(ErrRecord, errors.New("runrecord: "+what)) }

func nonceOK(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'z') && c != '-' {
			return false
		}
	}
	return true
}

var jsonNull = []byte("null")

// IsChild reports whether the record is a child run (origin null).
func (r *Record) IsChild() bool { return bytes.Equal(bytes.TrimSpace(r.Origin), jsonNull) }

// Validate checks one record in isolation.
func (r *Record) Validate() error {
	if r.Version != Version || r.Kind != Kind {
		return invalid("version or kind")
	}
	if !nonceOK(r.LaunchNonce) || r.Key != r.LaunchNonce {
		return invalid("key must equal a well-formed launch_nonce")
	}
	if !r.State.Known() {
		return invalid("state")
	}
	if r.LauncherGeneration == "" || r.StationRegistration == "" || r.InputPrincipal == "" || r.BindRequestID == "" {
		return invalid("required binding fields")
	}
	if r.Launcher == nil || !r.Launcher.Valid() || r.Launcher.UID != r.LauncherUID {
		return invalid("launcher incarnation")
	}
	if r.ChildUID == r.LauncherUID {
		return invalid("child uid equals launcher uid")
	}
	if len(r.Origin) == 0 || !json.Valid(r.Origin) {
		return invalid("origin bytes")
	}
	if r.IsChild() {
		if r.ParentRun == "" {
			return invalid("child run without parent_run")
		}
	} else {
		if r.ParentRun != "" {
			return invalid("root run with parent_run")
		}
		if r.SourceIncarnation == "" || r.PreparedID == "" {
			return invalid("root run without source_incarnation and prepared_id")
		}
	}
	switch r.State {
	case Binding, Refused, Unsent:
		if r.RunID != "" || r.Child != nil || r.InputReleased || r.EndRequestID != "" || len(r.EndRequestBody) != 0 || r.EndAcknowledged {
			return invalid(string(r.State) + " record carries later facts")
		}
	case Bound:
		if r.RunID == "" {
			return invalid("bound without run_id")
		}
		if r.EndRequestID != "" || len(r.EndRequestBody) != 0 || r.EndAcknowledged {
			return invalid("bound record carries end facts")
		}
	case Ending, Ended:
		if r.RunID == "" || r.EndRequestID == "" || len(r.EndRequestBody) == 0 {
			return invalid(string(r.State) + " without run_id, end request id and body")
		}
		if r.State == Ended && !r.EndAcknowledged {
			return invalid("ended without acknowledgement")
		}
		if r.State == Ending && r.EndAcknowledged {
			return invalid("ending with acknowledgement")
		}
	}
	if r.Child != nil {
		if !r.Child.Valid() || r.Child.UID != r.ChildUID {
			return invalid("child incarnation")
		}
	} else if r.InputReleased {
		return invalid("input released without child")
	}
	return nil
}

// ErrTransition is the root of every forbidden transition.
var ErrTransition = errors.New("runrecord: forbidden transition")

func forbidden(what string) error {
	return errors.Join(ErrTransition, errors.New("runrecord: "+what))
}

// CheckTransition validates new against old: immutable fields, state edges
// and monotone facts.
func CheckTransition(old, next *Record) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if old.Terminal() {
		return forbidden("terminal state " + string(old.State))
	}
	switch old.State {
	case Binding:
		if next.State != Bound && next.State != Refused && next.State != Unsent {
			return forbidden("binding -> " + string(next.State))
		}
	case Bound:
		if next.State != Bound && next.State != Ending {
			return forbidden("bound -> " + string(next.State))
		}
	case Ending:
		if next.State != Ending && next.State != Ended {
			return forbidden("ending -> " + string(next.State))
		}
	default:
		return forbidden("unknown old state")
	}
	if old.Key != next.Key || old.LaunchNonce != next.LaunchNonce || old.LauncherUID != next.LauncherUID || old.LauncherGeneration != next.LauncherGeneration ||
		old.StationRegistration != next.StationRegistration || old.InputPrincipal != next.InputPrincipal || !bytes.Equal(old.Origin, next.Origin) ||
		old.ParentRun != next.ParentRun || old.SourceIncarnation != next.SourceIncarnation || old.PreparedID != next.PreparedID ||
		old.BindRequestID != next.BindRequestID || old.ChildUID != next.ChildUID || *old.Launcher != *next.Launcher {
		return forbidden("immutable field changed")
	}
	if old.RunID != "" && old.RunID != next.RunID {
		return forbidden("run_id changed")
	}
	if old.Child != nil && (next.Child == nil || *old.Child != *next.Child) {
		return forbidden("child incarnation changed")
	}
	if old.InputReleased && !next.InputReleased {
		return forbidden("input release revoked")
	}
	if next.InputReleased && !old.InputReleased && next.State != Bound {
		return forbidden("input release outside bound")
	}
	if old.EndRequestID != "" && (old.EndRequestID != next.EndRequestID || !bytes.Equal(old.EndRequestBody, next.EndRequestBody)) {
		return forbidden("end request changed")
	}
	if old.TeardownDone && !next.TeardownDone {
		return forbidden("teardown revoked")
	}
	if old.EndAcknowledged && !next.EndAcknowledged {
		return forbidden("acknowledgement revoked")
	}
	return nil
}

// Terminal reports whether the record admits no further transition.
func (r *Record) Terminal() bool { return r.State.Terminal() }

// Clone returns a deep copy.
func (r *Record) Clone() *Record {
	c := *r
	if r.Origin != nil {
		c.Origin = append(json.RawMessage(nil), r.Origin...)
	}
	if r.EndRequestBody != nil {
		c.EndRequestBody = append([]byte(nil), r.EndRequestBody...)
	}
	if r.Launcher != nil {
		l := *r.Launcher
		c.Launcher = &l
	}
	if r.Child != nil {
		ch := *r.Child
		c.Child = &ch
	}
	return &c
}

// RestartAction is the H6 table entry for one record.
type RestartAction string

const (
	// ActionHold: binding; Keymaker may or may not have committed. HELD,
	// run_bind_unknown, operator recovery.
	ActionHold RestartAction = "hold"
	// ActionEndAndKill: bound with no spawn, or spawned without input
	// release. j7_run_end and kill any child.
	ActionEndAndKill RestartAction = "end-and-kill"
	// ActionEndThenSweep: bound with input released and the launcher gone.
	// j7_run_end, then the uid sweep; never rebind.
	ActionEndThenSweep RestartAction = "end-then-sweep"
	// ActionOwned: bound with input released and the recorded launcher alive;
	// the live launcher owns it. Not an orphan.
	ActionOwned RestartAction = "owned"
	// ActionRetryEnd: ending; retry the end.
	ActionRetryEnd RestartAction = "retry-end"
	// ActionNeverAdopt: ended, refused, unsent, unknown or incomplete.
	ActionNeverAdopt RestartAction = "never-adopt"
	// ActionUndetermined: liveness of the recorded launcher could not be
	// read. Nothing is adopted and nothing is ended on that basis.
	ActionUndetermined RestartAction = "undetermined"
)

// Decide applies the H6 table. launcherAlive is the liveness of the RECORDED
// launcher incarnation; liveErr means it could not be determined. That the
// launcher restarted is never an input here.
func Decide(r *Record, launcherAlive bool, liveErr error) RestartAction {
	if r == nil || r.Validate() != nil {
		return ActionNeverAdopt
	}
	switch r.State {
	case Binding:
		return ActionHold
	case Bound:
		if liveErr != nil {
			return ActionUndetermined
		}
		if launcherAlive {
			return ActionOwned
		}
		if r.Child == nil || !r.InputReleased {
			return ActionEndAndKill
		}
		return ActionEndThenSweep
	case Ending:
		return ActionRetryEnd
	}
	return ActionNeverAdopt
}
