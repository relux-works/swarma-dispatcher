package m0

import (
	"bytes"
	"encoding/json"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

// Journal payloads of the M0 ledger. Every frame is one closed JSON object;
// the projection is rebuilt from frames by established recovery and cached in
// one bounded record. The journal is the source of truth; the record is not.
const (
	FrameKind          = "swarma-dispatcher m0-journal/1"
	projectionRecord   = "m0-projection"
	maxProjectionBytes = 65536 // section 2: <=64 KiB durable control/result state per lineage
	maxFrameBytes      = 8192
	maxEvidenceBytes   = 4096
)

type frameType string

const (
	frameOpen       frameType = "open"
	framePolicy     frameType = "policy"
	frameReserve    frameType = "reserve"
	frameIntent     frameType = "intent"
	frameCommit     frameType = "commit"
	frameResult     frameType = "result"
	frameCancel     frameType = "cancel"
	frameStopIntent frameType = "stop-intent"
	frameStop       frameType = "stop"
	frameObserve    frameType = "observe"
	frameRemoval    frameType = "removal"
)

type frame struct {
	Kind       string           `json:"kind"`
	Type       frameType        `json:"type"`
	Epoch      uint64           `json:"epoch,omitempty"`
	Now        int64            `json:"now,omitempty"`
	Policy     *policySeen      `json:"policy,omitempty"`
	Reserve    *reserveFrame    `json:"reserve,omitempty"`
	Intent     *intentFrame     `json:"intent,omitempty"`
	Commit     *commitFrame     `json:"commit,omitempty"`
	Result     *resultFrame     `json:"result,omitempty"`
	Cancel     *cancelFrame     `json:"cancel,omitempty"`
	StopIntent *stopIntentFrame `json:"stop_intent,omitempty"`
	Stop       *stopFrame       `json:"stop,omitempty"`
	Observe    *observeFrame    `json:"observe,omitempty"`
	Removal    *removalFrame    `json:"removal,omitempty"`
}

// policySeen is the last protected installation observed by this ledger.
type policySeen struct {
	ID              string `json:"id"`
	Revision        uint64 `json:"revision"`
	Digest          string `json:"digest"`
	InstallRevision uint64 `json:"install_revision"`
	Removed         bool   `json:"removed"`
}

type reserveFrame struct {
	RequestID   string      `json:"request_id"`
	Principal   string      `json:"principal"`
	PeerUID     uint32      `json:"peer_uid"`
	Canonical   string      `json:"canonical"`
	Association Association `json:"association"`
	Worker      Worker      `json:"worker"`
	Policy      PolicyPin   `json:"policy"`
	Receiver    string      `json:"receiver"`
	Plan        Plan        `json:"plan"`
	Lineage     string      `json:"lineage"`
	Deadlines   Deadlines   `json:"deadlines"`
}

type intentFrame struct {
	RequestID   string `json:"request_id"`
	EffectID    string `json:"effect_id"`
	ExecutionID string `json:"execution_id"`
	EntryDigest string `json:"entry_digest"`
	Attempt     uint64 `json:"attempt"`
}

type commitFrame struct {
	RequestID string `json:"request_id"`
	Unix      int64  `json:"unix"`
}

type resultFrame struct {
	RequestID string `json:"request_id"`
	Outcome   phase  `json:"outcome"`
	ExecUnix  int64  `json:"exec_unix,omitempty"`
	Receipt   []byte `json:"receipt,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// cancelFrame is the owner cancel record and its stop intent in one durable
// transition. NoEffect marks a cancel of a reservation that never reached
// intent: no receiver effect is possible and no stop is issued.
type cancelFrame struct {
	RequestID string `json:"request_id"`
	Principal string `json:"principal"`
	PeerUID   uint32 `json:"peer_uid"`
	Canonical string `json:"canonical"`
	Target    string `json:"target"`
	EffectID  string `json:"effect_id"`
	Event     int64  `json:"event"`
	Detection int64  `json:"detection"`
	NoEffect  bool   `json:"no_effect"`
}

// stopIntentFrame is the dispatcher's already-authorized automatic stop after
// policy removal, replacement or expiry.
type stopIntentFrame struct {
	RequestID string `json:"request_id"`
	EffectID  string `json:"effect_id"`
	Event     int64  `json:"event"`
	Detection int64  `json:"detection"`
	Source    string `json:"source"`
}

type stopFrame struct {
	RequestID string `json:"request_id"`
	Outcome   string `json:"outcome"`
	Evidence  []byte `json:"evidence,omitempty"`
}

// observeFrame records one receiver observation. Effect selects the observed
// original effect: "" or "start" is the launch effect (older frames omit the
// member), "stop" is the stop effect of a continuation.
type observeFrame struct {
	RequestID string      `json:"request_id"`
	Class     Observation `json:"class"`
	Effect    string      `json:"effect,omitempty"`
}

const (
	observeStart = "start"
	observeStop  = "stop"
)

type removalFrame struct {
	Event  int64  `json:"event"`
	Reason string `json:"reason"`
}

type phase string

const (
	phaseReserved  phase = "reserved"
	phaseInFlight  phase = "in_flight"
	phaseCommitted phase = "committed"
	phaseStarted   phase = "started"
	phaseRefused   phase = "refused"
	phaseUnknown   phase = "unknown"
	phaseLate      phase = "late_refused"
	phaseCancelled phase = "cancelled"
	phaseAbsent    phase = "absent"
)

const (
	stopAbsent   = "absent"
	stopUnknown  = "unknown"
	stopNoEffect = "no_effect"
)

const (
	sourceCancel  = "cancel"
	sourceRemoval = "removal"
	sourceExpiry  = "expiry"
)

// terminalStop reports whether a stop outcome ends the stop transaction.
// An unknown outcome is debt, not an end: it is continued under the same
// effect until the receiver answers.
func terminalStop(outcome string) bool { return outcome == stopAbsent || outcome == stopNoEffect }

type dispatchRecord struct {
	RequestID       string
	Principal       string
	PeerUID         uint32
	Canonical       []byte
	Association     Association
	Worker          Worker
	Policy          PolicyPin
	Receiver        string
	Plan            Plan
	Lineage         string
	Deadlines       Deadlines
	Phase           phase
	EffectID        string
	ExecutionID     string
	EntryDigest     string
	Attempt         uint64
	CommitUnix      int64
	ExecUnix        int64
	Receipt         []byte
	Detail          string
	CancelRequested bool
	StopEffectID    string
	StopEvent       int64
	StopDetection   int64
	StopSource      string
	StopObligations []StopObligation
	StopAttempts    int
	StopOutcome     string
	StopEvidence    []byte
	StopObserved    Observation
	Observed        Observation
}

// addObligation records the stop effect identity on first use and retains one
// obligation per source. A repeated source keeps its original event and
// detection: later polling never moves an earlier clock.
func (rec *dispatchRecord) addObligation(effect, source string, event, detection int64) {
	if rec.StopEffectID == "" {
		rec.StopEffectID, rec.StopEvent, rec.StopDetection, rec.StopSource = effect, event, detection, source
	}
	for _, o := range rec.StopObligations {
		if o.Source == source {
			return
		}
	}
	rec.StopObligations = append(rec.StopObligations, StopObligation{Source: source, Event: event, Detection: detection})
}

func (rec *dispatchRecord) hasObligation(source string) bool {
	for _, o := range rec.StopObligations {
		if o.Source == source {
			return true
		}
	}
	return false
}

// earliestCeiling is the earliest completion ceiling over every retained
// obligation with qualified time. Adding obligations can only lower it, so a
// retried stop never receives a later ceiling than the original.
func (rec *dispatchRecord) earliestCeiling() int64 {
	ceiling := int64(0)
	for _, o := range rec.StopObligations {
		if o.Event <= 0 || o.Detection <= 0 {
			continue
		}
		c := StopCeiling(o.Event, o.Detection, rec.Deadlines.DetectSeconds, rec.Deadlines.StopSeconds)
		if ceiling == 0 || c < ceiling {
			ceiling = c
		}
	}
	return ceiling
}

type cancelRecord struct {
	RequestID string
	Principal string
	PeerUID   uint32
	Canonical []byte
	Target    string
	EffectID  string
	Event     int64
	Detection int64
	NoEffect  bool
}

// projection is the in-memory reduction of the ledger.
type projection struct {
	Epoch           uint64
	Policy          *policySeen
	Watermark       int64
	Removal         int64
	RemovalRecorded bool
	Dispatches      map[string]*dispatchRecord
	Debits          map[string]string
	Cancels         map[string]*cancelRecord
}

func newProjection() *projection {
	return &projection{Dispatches: map[string]*dispatchRecord{}, Debits: map[string]string{}, Cancels: map[string]*cancelRecord{}}
}

func inconsistent(detail string) *Refusal { return refuse(CodeStateInconsistent, detail) }

func encodeFrame(f *frame) ([]byte, error) {
	f.Kind = FrameKind
	b, err := json.Marshal(f)
	if err != nil {
		return nil, inconsistent("frame encoding")
	}
	if len(b) > maxFrameBytes {
		return nil, refuse(CodeReserveExhausted, "frame exceeds its byte bound")
	}
	return b, nil
}

func decodeFrame(b []byte) (*frame, error) {
	if len(b) > maxFrameBytes {
		return nil, inconsistent("frame exceeds its byte bound")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var f frame
	if err := d.Decode(&f); err != nil || d.More() {
		return nil, inconsistent("frame decoding")
	}
	if f.Kind != FrameKind {
		return nil, inconsistent("frame kind")
	}
	return &f, nil
}

// apply reduces one frame. Every forbidden transition is an inconsistency,
// never silently repaired.
func (p *projection) apply(f *frame) error {
	if f.Kind != FrameKind {
		return inconsistent("frame kind")
	}
	if f.Now > p.Watermark {
		p.Watermark = f.Now
	}
	switch f.Type {
	case frameOpen:
		if f.Epoch == 0 || f.Epoch <= p.Epoch {
			return inconsistent("daemon epoch must advance")
		}
		p.Epoch = f.Epoch
	case framePolicy:
		if f.Policy == nil {
			return inconsistent("policy frame without body")
		}
		seen := *f.Policy
		p.Policy = &seen
	case frameReserve:
		r := f.Reserve
		if r == nil || p.Epoch == 0 {
			return inconsistent("reserve frame without body or epoch")
		}
		if _, dup := p.Dispatches[r.RequestID]; dup {
			return inconsistent("duplicate reservation")
		}
		if holder, ok := p.Debits[r.Lineage]; ok && holder != r.RequestID {
			return inconsistent("lineage debit already held")
		}
		p.Dispatches[r.RequestID] = &dispatchRecord{
			RequestID: r.RequestID, Principal: r.Principal, PeerUID: r.PeerUID, Canonical: []byte(r.Canonical),
			Association: r.Association, Worker: r.Worker, Policy: r.Policy, Receiver: r.Receiver, Plan: r.Plan,
			Lineage: r.Lineage, Deadlines: r.Deadlines, Phase: phaseReserved,
		}
		p.Debits[r.Lineage] = r.RequestID
	case frameIntent:
		i := f.Intent
		if i == nil {
			return inconsistent("intent frame without body")
		}
		rec := p.Dispatches[i.RequestID]
		if rec == nil || rec.Phase != phaseReserved {
			return inconsistent("intent without a reservation")
		}
		rec.EffectID, rec.ExecutionID, rec.EntryDigest, rec.Attempt = i.EffectID, i.ExecutionID, i.EntryDigest, i.Attempt
		rec.Phase = phaseInFlight
	case frameCommit:
		c := f.Commit
		if c == nil {
			return inconsistent("commit frame without body")
		}
		rec := p.Dispatches[c.RequestID]
		if rec == nil || rec.Phase != phaseInFlight {
			return inconsistent("commit without an in-flight intent")
		}
		rec.CommitUnix = c.Unix
		rec.Phase = phaseCommitted
	case frameResult:
		res := f.Result
		if res == nil {
			return inconsistent("result frame without body")
		}
		rec := p.Dispatches[res.RequestID]
		if rec == nil || (rec.Phase != phaseInFlight && rec.Phase != phaseCommitted) {
			return inconsistent("result without an open intent")
		}
		switch res.Outcome {
		case phaseStarted, phaseLate:
			if rec.Phase != phaseCommitted {
				return inconsistent("start outcome without committed C")
			}
		case phaseRefused, phaseUnknown:
		default:
			return inconsistent("unknown result outcome")
		}
		rec.Phase, rec.ExecUnix, rec.Receipt, rec.Detail = res.Outcome, res.ExecUnix, res.Receipt, res.Detail
	case frameCancel:
		c := f.Cancel
		if c == nil {
			return inconsistent("cancel frame without body")
		}
		if _, dup := p.Cancels[c.RequestID]; dup {
			return inconsistent("duplicate cancel record")
		}
		rec := p.Dispatches[c.Target]
		if rec == nil {
			return inconsistent("cancel of an unknown dispatch")
		}
		rec.CancelRequested = true
		if !c.NoEffect {
			if rec.StopEffectID != "" && rec.StopEffectID != c.EffectID {
				return inconsistent("cancel names a different stop effect")
			}
			rec.addObligation(c.EffectID, sourceCancel, c.Event, c.Detection)
		}
		p.Cancels[c.RequestID] = &cancelRecord{RequestID: c.RequestID, Principal: c.Principal, PeerUID: c.PeerUID, Canonical: []byte(c.Canonical), Target: c.Target, EffectID: c.EffectID, Event: c.Event, Detection: c.Detection, NoEffect: c.NoEffect}
	case frameStopIntent:
		s := f.StopIntent
		if s == nil {
			return inconsistent("stop-intent frame without body")
		}
		rec := p.Dispatches[s.RequestID]
		if rec == nil {
			return inconsistent("stop intent for an unknown dispatch")
		}
		if rec.StopEffectID != "" && rec.StopEffectID != s.EffectID {
			return inconsistent("stop intent names a different stop effect")
		}
		rec.addObligation(s.EffectID, s.Source, s.Event, s.Detection)
	case frameStop:
		s := f.Stop
		if s == nil {
			return inconsistent("stop frame without body")
		}
		rec := p.Dispatches[s.RequestID]
		if rec == nil {
			return inconsistent("stop outcome for an unknown dispatch")
		}
		if terminalStop(rec.StopOutcome) {
			return inconsistent("stop outcome after a terminal stop")
		}
		rec.StopAttempts++
		switch s.Outcome {
		case stopAbsent:
			if len(s.Evidence) == 0 || len(s.Evidence) > maxEvidenceBytes {
				return inconsistent("absence without bounded evidence")
			}
			rec.Phase = phaseAbsent
			rec.StopEvidence = s.Evidence
		case stopUnknown:
		case stopNoEffect:
			if rec.Phase != phaseReserved {
				return inconsistent("no-effect stop after intent")
			}
			rec.Phase = phaseCancelled
		default:
			return inconsistent("unknown stop outcome")
		}
		rec.StopOutcome = s.Outcome
	case frameObserve:
		o := f.Observe
		if o == nil {
			return inconsistent("observe frame without body")
		}
		rec := p.Dispatches[o.RequestID]
		if rec == nil {
			return inconsistent("observation of an unknown dispatch")
		}
		switch o.Class {
		case ObservationUnknown, ObservationPresent, ObservationAbsent, ObservationContradictory:
		default:
			return inconsistent("unknown observation class")
		}
		switch o.Effect {
		case "", observeStart:
			rec.Observed = o.Class
		case observeStop:
			rec.StopObserved = o.Class
		default:
			return inconsistent("unknown observed effect")
		}
	case frameRemoval:
		if f.Removal == nil {
			return inconsistent("removal frame without body")
		}
		if !p.RemovalRecorded {
			p.Removal = f.Removal.Event
			p.RemovalRecorded = true
		}
	default:
		return inconsistent("unknown frame type")
	}
	return nil
}

// replay reduces a complete frame sequence into a fresh projection.
func replay(frames [][]byte) (*projection, error) {
	p := newProjection()
	for _, b := range frames {
		f, err := decodeFrame(b)
		if err != nil {
			return nil, err
		}
		if err := p.apply(f); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *projection) marshal() ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, inconsistent("projection encoding")
	}
	return b, nil
}

// consumer rebuilds the projection during established recovery. It charges
// every retained payload and the cached projection against the budget.
type consumer struct {
	proj *projection
}

func (c *consumer) Consume(_ uint64, payload []byte, rec *journal.Recovery) error {
	if err := rec.Budget.Charge(uint64(len(payload))); err != nil {
		return err
	}
	f, err := decodeFrame(payload)
	if err != nil {
		return err
	}
	return c.proj.apply(f)
}

func (c *consumer) Finish(rec *journal.Recovery) error {
	if c.proj.Epoch == 0 {
		return inconsistent("ledger without an open frame")
	}
	b, err := c.proj.marshal()
	if err != nil {
		return err
	}
	if len(b) > maxProjectionBytes {
		return refuse(CodeReserveExhausted, "projection exceeds the durable reserve")
	}
	if err := rec.Budget.Charge(uint64(len(b))); err != nil {
		return err
	}
	return rec.ReplaceRecord(projectionRecord, b)
}
