package m0

import (
	"context"
	"errors"
	"sync"
	"syscall"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

const (
	maxDetailBytes       = 160
	daemonUmask          = 0177
	daemonConsumerBudget = 4 * 1024 * 1024
)

// Extension adapts the runtime to the transport daemon's post-hello stage.
// It answers only frames whose kind is the M0 request discriminator; every
// other frame keeps the reference subset's close-without-reply behavior.
type Extension struct {
	rt        *Runtime
	callers   []transport.Principal
	config    []byte
	exit      func() error
	exitMu    sync.Mutex
	exitCause error
}

// NewExtension optionally observes the process owner's permanent exit duty.
func NewExtension(rt *Runtime, exit ...func() error) *Extension {
	e := &Extension{rt: rt}
	if len(exit) > 0 {
		e.exit = exit[0]
	}
	return e
}

// ConfigBytes returns the exact protected bytes Attach parsed, as a copy.
func (e *Extension) ConfigBytes() []byte {
	if e == nil {
		return nil
	}
	return append([]byte(nil), e.config...)
}

func (e *Extension) fatal() error {
	if e == nil {
		return nil
	}
	e.exitMu.Lock()
	defer e.exitMu.Unlock()
	if e.exitCause == nil && e.exit != nil {
		e.exitCause = e.exit()
	}
	if e.exitCause != nil {
		return errors.Join(transport.ErrFatal, e.exitCause)
	}
	return nil
}

// ManagedCallers returns a copy of the protected list supplied by Attach.
// NewExtension alone enrolls nobody; only Attach reads installation data.
func (e *Extension) ManagedCallers() []transport.Principal {
	return append([]transport.Principal(nil), e.callers...)
}

// Close ends the ledger session.
func (e *Extension) Close() error {
	if e == nil || e.rt == nil {
		return nil
	}
	return e.rt.Close()
}

// Serve handles one post-hello frame. The request's request_id must equal
// the connection correlation ID; a sync failure returns transport.ErrFatal so
// the daemon exits and recovery is held.
func (e *Extension) Serve(peer uint32, id string, body []byte) (response string, resultErr error) {
	if err := e.fatal(); err != nil {
		return "", err
	}
	defer func() {
		if err := e.fatal(); err != nil {
			response, resultErr = "", errors.Join(resultErr, err)
		}
	}()
	v, _, err := canonicalize(body, requestLimits)
	if err != nil || v.kind != kindObject {
		return "", transport.ErrInvalid
	}
	if k, ok := v.member("kind").text(); !ok || k != RequestKind {
		return "", transport.ErrInvalid
	}
	if rid, ok := v.member("request_id").text(); !ok || rid != id {
		return refusalEnvelope(id, CodeRequestInvalid, "request_id differs from the connection correlation ID"), nil
	}
	res, err := e.rt.Handle(context.Background(), peer, body)
	if err != nil {
		if MustExit(err) {
			return "", errors.Join(transport.ErrFatal, err)
		}
		return RefusalResponse(id, err), nil
	}
	return resultEnvelope(id, res), nil
}

// sanitize keeps the wire subset's string repertoire and bounds the length.
func sanitize(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < maxDetailBytes; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			c = '_'
		}
		b = append(b, c)
	}
	return string(b)
}

// RefusalResponse encodes a definite pre-effect refusal with the same bounded
// envelope as Extension. Callers must handle fatal exit duties before using it;
// id is the transport-validated connection correlation ID.
func RefusalResponse(id string, err error) string {
	code := CodeOf(err)
	if code == "" {
		code = CodeRequestInvalid
	}
	detail := ""
	var r *Refusal
	if errors.As(err, &r) && r != nil {
		detail = r.Detail
	}
	return refusalEnvelope(id, code, detail)
}

func refusalEnvelope(id string, code Code, detail string) string {
	return `{"error":{"code":"` + sanitize(string(code)) + `","message":"` + sanitize(detail) + `"},"ok":false,"request_id":"` + id + `","v":1}`
}

func resultEnvelope(id string, res *Result) string {
	return `{"ok":true,"request_id":"` + id + `","result":{"effect_id":"` + sanitize(res.EffectID) + `","state":"` + sanitize(res.State) + `"},"v":1}`
}

// Attach is the activation gate. It returns an extension only when the
// configuration explicitly enables M0 AND the policy source reports an
// installed, valid, non-removed policy. Every other case refuses: m0_closed
// means the daemon continues capacity-only; any other code is a startup
// failure. With DefaultSeams the policy source refuses, so Attach is closed.
func Attach(configPath string, seams Seams, read func(string) ([]byte, error), exit ...func() error) (extension *Extension, resultErr error) {
	e := NewExtension(nil, exit...)
	defer func() {
		if err := e.fatal(); err != nil {
			if e.rt != nil {
				err = errors.Join(err, e.Close())
			}
			extension, resultErr = nil, errors.Join(resultErr, err)
		}
	}()
	b, err := read(configPath)
	if err != nil {
		return nil, &Refusal{Code: CodeRequestInvalid, Detail: "m0 configuration unreadable", Cause: err}
	}
	cfg, err := ParseConfig(b)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, refuse(CodeClosed, "m0 not selected by configuration")
	}
	seams = seams.complete()
	inst, err := seams.Policy.Current()
	if err != nil {
		return nil, &Refusal{Code: CodeClosed, Detail: "policy source reports no installed policy: " + err.Error(), Cause: err}
	}
	if err := inst.validate(); err != nil {
		return nil, &Refusal{Code: CodeClosed, Detail: "installed policy invalid: " + err.Error(), Cause: err}
	}
	if inst.Removed {
		return nil, refuse(CodeClosed, "installed policy is removed")
	}
	if err := platformSupported(); err != nil {
		return nil, err
	}
	// The same umask the transport daemon sets; the ledger opens first.
	syscall.Umask(daemonUmask)
	umask := uint32(daemonUmask)
	jc := journal.Config{
		Directory:                       cfg.StateDir,
		Records:                         "records",
		Inputs:                          "inputs",
		DeclaredUmask:                   &umask,
		SupportedLocalStorage:           cfg.SupportedLocalStorage,
		RecoveryPrerequisiteEstablished: cfg.RecoveryAttested,
		ConsumerBudget:                  daemonConsumerBudget,
	}
	var rt *Runtime
	switch cfg.Entry {
	case EntryInitialize:
		rt, err = Initialize(jc, seams)
	case EntryEstablished:
		rt, err = OpenEstablished(jc, seams)
	default:
		return nil, invalid("member constant: entry")
	}
	if err != nil {
		return nil, err
	}
	e.rt = rt
	e.callers = append([]transport.Principal(nil), cfg.Callers...)
	e.config = append([]byte(nil), b...)
	return e, nil
}
