package m0

import "context"

// Generation is the current account generation of one UID as read from the
// protected user-manager journal. A dispatcher record, a broker lease or a
// launch_id never substitutes for it.
type Generation struct {
	Generation string
	Active     bool
}

// GenerationReader is the generation seam.
type GenerationReader interface {
	Current(uid uint32) (Generation, error)
}

// UnpinnedGenerationReader refuses by name.
type UnpinnedGenerationReader struct{}

func (UnpinnedGenerationReader) Current(uint32) (Generation, error) {
	return Generation{}, Unpinned(SeamGeneration)
}

// ReceiverFacts are the selected protected facts the receiver presents at C,
// after root receipt and verified UID/GID drop and before any target code.
type ReceiverFacts struct {
	DropVerified    bool
	Receiver        string
	WorkerUID       uint32
	Generation      string
	ControllerEpoch uint64
	ExecutionID     string
	EffectID        string
}

// CommitGate is the dispatcher side of receiver interlock A. The receiver
// calls Commit exactly once per prepared child, after verified drop. A nil
// return means C is durably committed and the debit consumed; any error means
// the child must be aborted before target code.
type CommitGate interface {
	Commit(facts ReceiverFacts) error
}

// StartRequest is the dispatcher-owned launch effect handed to the launcher.
type StartRequest struct {
	EffectID        string
	ExecutionID     string
	ControllerEpoch uint64
	Worker          Worker
	ExecProfile     string
	Entry           []byte
	EntryDigest     string
	Deadlines       Deadlines
}

// StartReceipt is the launcher's answer. Committed reports that the gate
// accepted C; ExecUnix is the receiver's S (committed handoff) time or zero
// when unknown; Receipt is the bounded receiver receipt.
type StartReceipt struct {
	Committed  bool
	CommitUnix int64
	ExecUnix   int64
	Receipt    []byte
}

// StopObligation is one original stop obligation with its own provenance:
// the authoritative event (owner cancel intent, removal intent or original
// expiry) and the dispatcher's detection of it. Obligations are only ever
// added, never moved or reset.
type StopObligation struct {
	Source    string `json:"source"`
	Event     int64  `json:"event"`
	Detection int64  `json:"detection"`
}

// StopRequest is the owned stop effect with its original obligations. Event
// and Detection are the first recorded obligation; Obligations lists every
// retained one; Ceiling is the EARLIEST applicable completion deadline over all
// of them (zero only when no obligation has qualified time).
type StopRequest struct {
	EffectID    string
	ExecutionID string
	Worker      Worker
	Event       int64
	Detection   int64
	Obligations []StopObligation
	Ceiling     int64
}

// StopReceipt reports receiver-owned, fenced, account-wide confirmed absence
// with its evidence, or nothing. Leader exit, PID disappearance, socket EOF,
// an expired lease or a missing receipt is not absence.
type StopReceipt struct {
	Absent   bool
	Evidence []byte
}

// Observation is one of the dispatcher's four observation classes.
type Observation string

const (
	ObservationUnknown       Observation = "unknown"
	ObservationPresent       Observation = "present"
	ObservationAbsent        Observation = "absent"
	ObservationContradictory Observation = "contradictory"
)

// EffectQuery names the original effect for a read-only effect.observe.
type EffectQuery struct {
	EffectID        string
	Operation       string
	ArgumentsDigest string
}

// Launcher is the user-manager user-launcher intake seam (slice S1).
type Launcher interface {
	Start(ctx context.Context, req StartRequest, gate CommitGate) (StartReceipt, error)
	Stop(ctx context.Context, req StopRequest) (StopReceipt, error)
	Observe(ctx context.Context, q EffectQuery) (Observation, error)
}

// UnpinnedLauncher refuses by name on every method.
type UnpinnedLauncher struct{}

func (UnpinnedLauncher) Start(context.Context, StartRequest, CommitGate) (StartReceipt, error) {
	return StartReceipt{}, Unpinned(SeamLauncher)
}
func (UnpinnedLauncher) Stop(context.Context, StopRequest) (StopReceipt, error) {
	return StopReceipt{}, Unpinned(SeamLauncher)
}
func (UnpinnedLauncher) Observe(context.Context, EffectQuery) (Observation, error) {
	return ObservationUnknown, Unpinned(SeamLauncher)
}

// Seams are every external dependency of the runtime.
type Seams struct {
	Policy     PolicySource
	Launcher   Launcher
	Runner     Runner
	Clock      Clock
	Generation GenerationReader
	Entry      EntryComposer
}

// DefaultSeams refuse everywhere. This is the only configuration the daemon
// can construct today.
func DefaultSeams() Seams {
	return Seams{
		Policy:     UnpinnedPolicySource{},
		Launcher:   UnpinnedLauncher{},
		Runner:     UnpinnedRunner{},
		Clock:      UnpinnedClock{},
		Generation: UnpinnedGenerationReader{},
		Entry:      UnpinnedEntryComposer{},
	}
}

// complete fills nil seams with refusing defaults so a partially pinned Seams
// value never dereferences nil and never falls back.
func (s Seams) complete() Seams {
	d := DefaultSeams()
	if s.Policy == nil {
		s.Policy = d.Policy
	}
	if s.Launcher == nil {
		s.Launcher = d.Launcher
	}
	if s.Runner == nil {
		s.Runner = d.Runner
	}
	if s.Clock == nil {
		s.Clock = d.Clock
	}
	if s.Generation == nil {
		s.Generation = d.Generation
	}
	if s.Entry == nil {
		s.Entry = d.Entry
	}
	return s
}
