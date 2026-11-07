//go:build linux

package demolaunch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
)

type stopRecord struct {
	intent   stopIntent
	deadline time.Time // Zero after restart: the original monotonic clock is lost.
	active   bool
	child    *child
	terminal *response
	latchErr error
}
type engine struct {
	torn                            []string
	tornIntents                     map[string]bool
	mu                              sync.Mutex
	ctx                             context.Context
	store                           *stateStore
	accounts                        map[string]account
	attempts                        map[string]attempt
	stopped                         map[string]bool
	stops                           map[string]*stopRecord
	closed, latchWritten, launching bool
	launchChildren                  map[string]*child
	run                             runner
	executor                        string
	receiptRoot                     string
	receiptOwner                    uint32
	receiptGroup                    uint32
	now                             func() time.Time
	receiptWait                     time.Duration
	stopWait                        time.Duration
	readReceipt                     func(account, string) ([]byte, error)
	readFile                        func(io.Reader, int) ([]byte, error)
}

func newEngine(ctx context.Context, store *stateStore, accounts []account, cfg serverConfig) (*engine, error) {
	return newEngineWithRunner(ctx, store, accounts, cfg, fixedRunner)
}
func newEngineWithRunner(ctx context.Context, store *stateStore, accounts []account, cfg serverConfig, run runner) (*engine, error) {
	e := &engine{tornIntents: map[string]bool{}, ctx: ctx, store: store, accounts: map[string]account{}, attempts: map[string]attempt{}, stopped: map[string]bool{}, stops: map[string]*stopRecord{}, launchChildren: map[string]*child{}, run: run, readFile: readSnapshot, now: time.Now, executor: cfg.Executor, receiptRoot: cfg.ReceiptRoot, receiptWait: 10 * time.Second, stopWait: 45 * time.Second}
	for _, a := range accounts {
		e.accounts[a.Name] = a
	}
	e.readReceipt = e.receiptBytes
	if err := e.load(); err != nil {
		return nil, err
	}
	return e, nil
}
func (e *engine) handle(b []byte) (reply response) {
	e.mu.Lock()
	if e.closed && !e.latchWritten {
		_ = e.closeAdmission("latch_retry")
	}
	e.mu.Unlock()
	defer func() { e.mu.Lock(); reply.Closed = e.closed; e.mu.Unlock() }()
	r, err := parseRequest(b)
	if err != nil {
		return refusal(err.Error())
	}
	if _, ok := e.accounts[r.Account]; !ok {
		return refusal("account_unknown")
	}
	switch r.Op {
	case "demo.launch":
		return e.launch(r, b)
	case "demo.stop":
		return e.stop(r)
	default:
		return e.status(r.Account)
	}
}
func (e *engine) status(name string) response {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := response{State: "facts", Closed: e.closed, Stopped: e.stopped[name], OutputLoss: e.outputLoss(name), Torn: append([]string(nil), e.torn...)}
	if e.closed && !e.latchWritten {
		r.Code = "latch_uncertain"
	}
	if a, ok := e.attempts[name]; ok {
		copy := a
		r.Attempt = &copy
	}
	return r
}
func (e *engine) launch(r request, raw []byte) response {
	e.mu.Lock()
	if _, ok := e.attempts[r.Account]; ok {
		e.mu.Unlock()
		return refusal("attempt_exists")
	}
	if e.closed {
		e.mu.Unlock()
		return refusal("admission_closed")
	}
	if e.launching {
		e.mu.Unlock()
		return refusal("launch_in_flight")
	}
	for name := range e.attempts {
		if !e.stopped[name] {
			e.mu.Unlock()
			return refusal("attempt_unstopped")
		}
	}
	for _, s := range e.stops {
		if s.active {
			e.mu.Unlock()
			return refusal("stop_in_flight")
		}
	}
	// A successful stop cannot authorize unlimited live sudo handles/drainers.
	for _, ch := range e.launchChildren {
		select {
		case <-ch.done:
		default:
			e.mu.Unlock()
			return refusal("child_unreaped")
		}
		select {
		case <-ch.outputDone:
		default:
			e.mu.Unlock()
			return refusal("output_pending")
		}
	}
	var nonce [16]byte
	rand.Read(nonce[:])
	digest := sha256.Sum256(raw)
	at := attempt{RequestID: r.RequestID, Digest: hex.EncodeToString(digest[:]), ExecutionID: "EX-" + hex.EncodeToString(nonce[:]), Time: time.Now().UTC().Format(time.RFC3339Nano)}
	a := e.accounts[r.Account]
	if e.ctx.Err() != nil {
		e.mu.Unlock()
		return refusal("shutdown")
	}
	if err := e.store.create("attempt-"+a.Generation, marshal(at)); err != nil {
		e.closed = true
		code := "marker_uncertain"
		if e.closeAdmission(code) != nil {
			code = "latch_uncertain"
		}
		e.mu.Unlock()
		return refusal(code)
	}
	e.attempts[r.Account] = at
	e.launching = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.launching = false; e.mu.Unlock() }()
	deadline := e.now().Add(e.receiptWait)
	out, err := e.logCapture(a.Generation, "stdout")
	if err != nil {
		return e.unknown("log_unavailable")
	}
	stderr, err := e.logCapture(a.Generation, "stderr")
	if err != nil {
		out.file.Close()
		return e.unknown("log_unavailable")
	}
	payload := marshal(struct {
		V           int    `json:"v"`
		Op          string `json:"op"`
		RequestID   string `json:"request_id"`
		Generation  string `json:"generation"`
		ExecutionID string `json:"execution_id"`
		Epoch       int    `json:"epoch"`
		Executor    string `json:"executor"`
		Plan        string `json:"plan"`
	}{1, "launch.start", r.RequestID, a.Generation, at.ExecutionID, 1, e.executor, r.Plan})
	if len(payload) > 80*1024 {
		out.file.Close()
		stderr.file.Close()
		return e.unknown("launcher_request_limit")
	}
	ch, err := startChild(e.ctx, e.run, launcherPath, payload, deadline, out, stderr)
	if ch != nil {
		e.mu.Lock()
		e.launchChildren[r.Account] = ch
		e.mu.Unlock()
	} else {
		out.file.Close()
		stderr.file.Close()
	}
	if err != nil {
		return e.unknown("launcher_io")
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		if e.ctx.Err() != nil {
			return e.unknown("shutdown")
		}
		exited := false
		select {
		case <-ch.done:
			exited = true
		default:
		}
		if !e.now().Before(deadline) {
			return e.unknown("receipt_timeout")
		}
		if _, _, x := out.snapshot(); x != nil {
			return e.unknown("log_unavailable")
		}
		if _, _, x := stderr.snapshot(); x != nil {
			return e.unknown("log_unavailable")
		}
		b, err := e.readReceipt(a, at.ExecutionID)
		if e.ctx.Err() != nil {
			return e.unknown("shutdown")
		}
		if err == nil {
			rec, err := parseReceipt(b, at.ExecutionID)
			if err != nil {
				return e.unknown("receipt_mismatch")
			}
			if !e.now().Before(deadline) {
				return e.unknown("receipt_timeout")
			}
			return response{State: "receipt-seen", Receipt: rec}
		}
		if !errors.Is(err, os.ErrNotExist) {
			return e.unknown("receipt_mismatch")
		}
		if exited {
			if e.ctx.Err() != nil {
				return e.unknown("shutdown")
			}
			return e.unknown("launcher_exit")
		}
		select {
		case <-ch.done:
			// Loop back for one final bounded receipt observation before classifying exit.
		case <-timer.C:
			return e.unknown("receipt_timeout")
		case <-e.ctx.Done():
			return e.unknown("shutdown")
		case <-ticker.C:
		}
	}
}
func (e *engine) unknown(code string) response {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.closeAdmission(code); err != nil {
		code = "latch_uncertain"
	}
	return response{State: "unknown", Code: code, Closed: true}
}
func (e *engine) receiptBytes(a account, execution string) ([]byte, error) {
	d, err := pinDirectory(e.receiptRoot+"/"+a.Generation, e.receiptOwner)
	if err != nil {
		return nil, err
	}
	defer d.close()
	f, err := openAt(d.last(), execution+".json", syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := identity(f)
	if err != nil || !receiptMetadata(before, e.receiptOwner, e.receiptGroup) {
		return nil, ErrOwnership
	}
	b, err := e.readFile(f, 4096)
	if err != nil {
		return nil, err
	}
	after, err := identity(f)
	if err != nil || !before.attrs(after) || before.size != after.size || after.size != int64(len(b)) {
		return nil, ErrOwnership
	}
	named, err := atIdentity(d, execution+".json")
	if err != nil || !after.attrs(named) {
		return nil, ErrOwnership
	}
	if err = d.recheck(); err != nil {
		return nil, err
	}
	return b, nil
}
func (e *engine) stop(r request) response {
	callDeadline := time.Now().Add(e.stopWait)
	e.mu.Lock()
	at, ok := e.attempts[r.Account]
	if !ok {
		e.mu.Unlock()
		return refusal("attempt_missing")
	}
	if at.ExecutionID == "" {
		e.mu.Unlock()
		return refusal("attempt_identity_unknown")
	}
	if r.RequestID == at.RequestID {
		e.mu.Unlock()
		return refusal("request_id_conflict")
	}
	// Do not race a not-yet-issued launcher with a stop. Once launch returns
	// unknown or receipt-seen, stop is available regardless of admission latch.
	if e.launching {
		e.mu.Unlock()
		return refusal("launch_in_flight")
	}
	s := e.stops[r.Account]
	// An unknown-intent guess reserves only its live call, not future guesses.
	if s != nil && e.tornIntents[r.Account] && s.terminal == nil && !s.active {
		if s.child != nil {
			select {
			case <-s.child.done:
			default:
				e.mu.Unlock()
				return refusal("stop_in_flight")
			}
			select {
			case <-s.child.outputDone:
			default:
				e.mu.Unlock()
				return refusal("stop_in_flight")
			}
		}
		delete(e.stops, r.Account)
		s = nil
	}
	if s != nil && (s.intent.RequestID != r.RequestID || s.intent.Grace != r.Grace) {
		e.mu.Unlock()
		return refusal("stop_conflict")
	}
	if s != nil && s.active {
		e.mu.Unlock()
		return refusal("stop_in_flight")
	}
	if s != nil && s.terminal != nil {
		ret := *s.terminal
		e.mu.Unlock()
		return ret
	}
	a := e.accounts[r.Account]
	var callLatchErr error
	if s == nil {
		// One caller-scoped stop ID must not target a different account.
		for _, other := range e.stops {
			if other.intent.RequestID == r.RequestID {
				e.mu.Unlock()
				return refusal("stop_conflict")
			}
		}
		now := time.Now()
		s = &stopRecord{intent: stopIntent{r.RequestID, at.ExecutionID, r.Grace, now.UTC().Format(time.RFC3339Nano)}, deadline: callDeadline}
		if e.tornIntents[r.Account] {
			// No intact original intent survived startup. Never persist a guess.
			s.deadline = time.Time{}
		} else if err := e.store.create("stop-"+a.Generation, marshal(s.intent)); err != nil {
			// This process retains its original arguments, but cannot claim time.
			s.deadline = time.Time{}
			callLatchErr = e.closeAdmission("stop_intent_uncertain")
		}
		e.stops[r.Account] = s
	}
	if s.child != nil {
		select {
		case <-s.child.done:
		default:
			e.mu.Unlock()
			return refusal("stop_in_flight")
		}
		select {
		case <-s.child.outputDone:
		default:
			e.mu.Unlock()
			return refusal("stop_in_flight")
		}
	}
	// The original deadline classifies the effect; it is never replenished.
	// A continuation has a separate finite call budget even when the original
	// monotonic deadline is missed or lost. The helper owns its original grace.
	if !s.deadline.IsZero() && time.Now().Before(s.deadline) && s.deadline.Before(callDeadline) {
		callDeadline = s.deadline
	}
	if timing(s.deadline) != "timely" {
		callLatchErr = errors.Join(callLatchErr, e.closeAdmission("stop_time_uncertain"))
	}
	s.latchErr = callLatchErr
	s.active = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); s.active = false; e.mu.Unlock() }()
	if !time.Now().Before(callDeadline) {
		return e.unresolved("stop_timeout", s)
	}
	payload := marshal(struct {
		V         int    `json:"v"`
		Op        string `json:"op"`
		RequestID string `json:"request_id"`
		Args      struct {
			Generation  string `json:"generation"`
			ExecutionID string `json:"execution_id"`
			Grace       int    `json:"grace_seconds"`
		} `json:"args"`
	}{V: 1, Op: "exec.stop", RequestID: r.RequestID, Args: struct {
		Generation  string `json:"generation"`
		ExecutionID string `json:"execution_id"`
		Grace       int    `json:"grace_seconds"`
	}{a.Generation, at.ExecutionID, r.Grace}})
	if len(payload) > 16*1024 {
		return e.unresolved("helper_request_limit", s)
	}
	out, stderr := &capture{limit: 16384}, &capture{limit: 16384}
	ch, err := startChild(e.ctx, e.run, managerPath, payload, callDeadline, out, stderr)
	e.mu.Lock()
	s.child = ch
	e.mu.Unlock()
	if err != nil {
		return e.unresolved("helper_io", s)
	}
	timer := time.NewTimer(time.Until(callDeadline))
	defer timer.Stop()
	for _, done := range []chan struct{}{ch.done, ch.outputDone} {
		select {
		case <-done:
		case <-timer.C:
			return e.unresolved("stop_timeout", s)
		case <-e.ctx.Done():
			return e.unresolved("shutdown", s)
		}
	}
	b, overflow, err := out.snapshot()
	if err != nil || overflow {
		return e.unresolved("helper_result_invalid", s)
	}
	code := stopResult(b, r.RequestID, at.ExecutionID)
	if code != "" {
		return e.unresolved(code, s)
	}
	if ch.err != nil {
		return e.unresolved("helper_exit", s)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	verdict := timing(s.deadline)
	latchErr := callLatchErr
	if verdict != "timely" {
		latchErr = errors.Join(latchErr, e.closeAdmission("stop_time_uncertain"))
	}
	record := stoppedRecord{ExecutionID: at.ExecutionID, Timing: verdict, Closed: e.closed}
	if latchErr != nil {
		record.Code = "latch_uncertain"
	}
	if err = e.store.create("stopped-"+a.Generation, marshal(record)); err != nil {
		code := "stopped_uncertain"
		if e.closeAdmission(code) != nil {
			code = "latch_uncertain"
		}
		return response{State: "unresolved", Code: code, Timing: verdict, Closed: true}
	}
	e.stopped[r.Account] = true
	result := response{State: "absent", Timing: verdict, Closed: e.closed, Stopped: true}
	if latchErr != nil {
		result.Code = "latch_uncertain"
	}
	s.terminal = &result
	return result
}
func timing(deadline time.Time) string {
	if deadline.IsZero() {
		return "unknown"
	}
	if time.Now().After(deadline) {
		return "missed"
	}
	return "timely"
}
func (e *engine) unresolved(code string, s *stopRecord) response {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.closeAdmission(code); err != nil || s.latchErr != nil {
		code = "latch_uncertain"
	}
	return response{State: "unresolved", Code: code, Timing: timing(s.deadline), Closed: true}
}

// The helper's documented nested result is used, never exit zero as absence.
func stopResult(b []byte, id, execution string) string {
	m, err := object(b, 16384, "request_id ok", "result state error")
	if err != nil {
		return "helper_result_invalid"
	}
	var rid string
	var ok bool
	if typed(m["request_id"], &rid) != nil || rid != id || typed(m["ok"], &ok) != nil {
		return "helper_result_invalid"
	}
	if !ok {
		if len(m) != 3 || m["error"] == nil {
			return "helper_result_invalid"
		}
		x, err := object(m["error"], 2048, "code message", "")
		var code, msg string
		if err != nil || typed(x["code"], &code) != nil || !validID(code) || typed(x["message"], &msg) != nil || len(msg) > 1024 {
			return "helper_result_invalid"
		}
		return code
	}
	if len(m) != 4 || m["error"] != nil || m["state"] == nil {
		return "helper_result_invalid"
	}
	var state string
	if typed(m["state"], &state) != nil || !validID(state) {
		return "helper_result_invalid"
	}
	result, err := object(m["result"], 4096, "execution_id stopped_at receipt", "")
	if err != nil {
		return "helper_result_invalid"
	}
	var eid, when string
	if typed(result["execution_id"], &eid) != nil || eid != execution || typed(result["stopped_at"], &when) != nil {
		return "helper_result_invalid"
	}
	if _, err = time.Parse(time.RFC3339Nano, when); err != nil {
		return "helper_result_invalid"
	}
	counts, err := object(result["receipt"], 1024, "signalled killed remaining", "")
	if err != nil {
		return "helper_result_invalid"
	}
	for _, key := range []string{"signalled", "killed", "remaining"} {
		var n uint64
		if typed(counts[key], &n) != nil {
			return "helper_result_invalid"
		}
		if key == "remaining" && n != 0 {
			return "processes_remain"
		}
	}
	return ""
}

func receiptMetadata(id fileID, owner, group uint32) bool {
	return id.uid == owner && id.gid == group && id.mode&syscall.S_IFMT == syscall.S_IFREG && id.mode&07777 == 0644 && id.links == 1
}
