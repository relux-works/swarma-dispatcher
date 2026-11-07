//go:build linux

package demolaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const gen = "0123456789abcdef0123456789abcdef"
const gen2 = "1123456789abcdef0123456789abcdef"
const boot = "12345678-1234-1234-1234-123456789abc"

// The test executable is the real child: stdin EOF, pipes and process exits are
// genuine. This proves dispatcher behavior only, not sudo or launcher behavior.
func TestFakeChild(t *testing.T) {
	if os.Getenv("DEMO_FAKE_CHILD") != "1" {
		return
	}
	mode, root := os.Getenv("DEMO_FAKE_MODE"), os.Getenv("DEMO_FAKE_ROOT")
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 81921))
	if err != nil || len(b) > 81920 {
		os.Exit(31)
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(b, &input) != nil {
		os.Exit(32)
	}
	op := ""
	json.Unmarshal(input["op"], &op)
	if err = os.WriteFile(filepath.Join(root, op+".input"), b, 0600); err != nil {
		os.Exit(33)
	}
	call := os.Getenv("DEMO_FAKE_CALL")
	record := struct {
		Path  string `json:"path"`
		Stdin string `json:"stdin"`
	}{os.Getenv("DEMO_FAKE_PATH"), string(b)}
	if err = os.WriteFile(filepath.Join(root, "call-"+call+".json"), marshal(record), 0600); err != nil {
		os.Exit(38)
	}
	if (op == "launch.start" && record.Path != "/usr/local/libexec/swarma/swarma-user-launcher") || (op == "exec.stop" && record.Path != "/usr/local/libexec/swarma/swarma-user-manager") {
		os.Exit(37)
	}
	if mode == "hold-stop" && op == "exec.stop" {
		fakeWait(root, "release-stop")
	}
	if op == "launch.start" {
		if mode == "exit" {
			os.Exit(9)
		}
		if mode == "timeout" {
			fakeWait(root, "release-launch")
			os.Exit(0)
		}
		var execution string
		json.Unmarshal(input["execution_id"], &execution)
		rec := `{"execution_id":"` + execution + `","epoch":1,"pid":123,"start_time":456,"boot_id":"` + boot + `","session_id":123}`
		if mode == "mismatch" {
			rec = strings.Replace(rec, execution, "EX-00000000000000000000000000000000", 1)
		}
		if mode == "extra" {
			rec = strings.TrimSuffix(rec, "}") + `,"extra":true}`
		}
		if mode == "flood" {
			block := bytes.Repeat([]byte{'x'}, 32768)
			for range 80 {
				if _, err = os.Stdout.Write(block); err != nil {
					os.Exit(34)
				}
				if _, err = os.Stderr.Write(block); err != nil {
					os.Exit(35)
				}
			}
		}
		var generation string
		json.Unmarshal(input["generation"], &generation)
		dir := filepath.Join(root, generation)
		if err = os.MkdirAll(dir, 0700); err != nil {
			os.Exit(36)
		}
		tmp := filepath.Join(dir, execution+".tmp")
		if err = os.WriteFile(tmp, []byte(rec), 0644); err != nil {
			os.Exit(36)
		}
		f, x := os.Open(tmp)
		if x != nil {
			os.Exit(36)
		}
		x = f.Sync()
		f.Close()
		if x != nil {
			os.Exit(36)
		}
		if err = os.Rename(tmp, filepath.Join(dir, execution+".json")); err != nil {
			os.Exit(36)
		}
		if mode == "hold-launch" || mode == "after-output" {
			fakeWait(root, "release-launch")
		}
		if mode == "after-output" {
			os.Stdout.Write(bytes.Repeat([]byte{'z'}, 2<<20))
			os.Stderr.Write(bytes.Repeat([]byte{'z'}, 2<<20))
		}
		os.Exit(0)
	}
	var args struct {
		ExecutionID string `json:"execution_id"`
	}
	json.Unmarshal(input["args"], &args)
	var id string
	json.Unmarshal(input["request_id"], &id)
	result := `{"request_id":"` + id + `","ok":true,"state":"active","result":{"execution_id":"` + args.ExecutionID + `","stopped_at":"2026-01-01T00:00:00Z","receipt":{"signalled":1,"killed":0,"remaining":0}}}`
	switch mode {
	case "remaining":
		result = strings.Replace(result, `"remaining":0`, `"remaining":1`, 1)
	case "false", "false-nonzero":
		result = `{"request_id":"` + id + `","ok":false,"error":{"code":"target_not_owned","message":"refused"}}`
	case "malformed":
		result = `{`
	case "empty":
		result = ""
	case "nonzero":
		os.Stdout.WriteString(result)
		os.Exit(7)
	case "slow":
		fakeWait(root, "release-stop")
	case "overflow":
		result = strings.Repeat("x", 16385)
	}
	os.Stdout.WriteString(result)
	if mode == "false-nonzero" {
		os.Exit(1)
	}
	os.Exit(0)
}
func fixtureDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.Abs("../../.temp")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(base, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "demo-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}

type harness struct {
	e      *engine
	root   string
	cancel context.CancelFunc
	calls  atomic.Int32
	mode   string
}

func fixture(t *testing.T, mode string) *harness {
	t.Helper()
	root := fixtureDir(t)
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := openState(state, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	accounts := []account{{"demo", 30000, 30000, gen, "demo-service"}, {"second", 30019, 30019, gen2, "demo-service"}}
	e, err := newEngine(ctx, store, accounts, serverConfig{Executor: "demo-executor"})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	e.receiptRoot = root
	e.receiptOwner = uint32(os.Geteuid())
	e.receiptGroup = uint32(os.Getegid())
	h := &harness{e: e, root: root, cancel: cancel, mode: mode}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e.run = func(path string) *exec.Cmd {
		call := h.calls.Add(1)
		if path != launcherPath && path != managerPath {
			t.Error("foreign runner path", path)
		}
		c := exec.Command(exe, "-test.run=^TestFakeChild$")
		c.Env = []string{"DEMO_FAKE_CHILD=1", "DEMO_FAKE_MODE=" + h.mode, "DEMO_FAKE_ROOT=" + root, "DEMO_FAKE_PATH=" + path, "DEMO_FAKE_CALL=" + strconv.Itoa(int(call)), "GORACE=atexit_sleep_ms=0"}
		return c
	}

	t.Cleanup(func() {
		// Release only fixture children; all waits have a finite failure deadline.
		os.WriteFile(filepath.Join(root, "release-launch"), nil, 0600)
		os.WriteFile(filepath.Join(root, "release-stop"), nil, 0600)
		until := time.Now().Add(30 * time.Second)
		for _, ch := range e.launchChildren {
			waitChild(t, ch, until)
		}
		for _, s := range e.stops {
			if s.child != nil {
				waitChild(t, s.child, until)
			}
		}
		cancel()
		store.dir.Close()
	})
	return h
}
func waitChild(t *testing.T, ch *child, until time.Time) {
	t.Helper()
	for _, done := range []chan struct{}{ch.done, ch.outputDone} {
		select {
		case <-done:
		case <-time.After(time.Until(until)):
			t.Error("fake child exceeded fixture deadline")
			return
		}
	}
}
func launchBytes(name string) []byte {
	return []byte(`{"op":"demo.launch","request_id":"r","account":"` + name + `","plan":"AAE="}`)
}
func stopBytes(grace int) []byte {
	return []byte(`{"op":"demo.stop","request_id":"stop-1","account":"demo","grace_seconds":` + jsonNumber(grace) + `}`)
}
func assertState(t *testing.T, r response, state, code string) {
	t.Helper()
	if r.State != state || r.Code != code {
		t.Fatalf("got %+v; want %s/%s", r, state, code)
	}
}
func fileBytes(t *testing.T, h *harness, name string) []byte {
	t.Helper()
	b, e := h.e.store.read(name)
	if e != nil {
		t.Fatal(name, e)
	}
	return b
}
func absentFile(t *testing.T, h *harness, name string) {
	t.Helper()
	if _, e := h.e.store.read(name); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("unexpected file", name, e)
	}
}

// A real successful child is the positive control for persistent refusal and
// restart closure. The original marker bytes survive, and no second child runs.
func TestLaunchMarkerRestart(t *testing.T) {
	h := fixture(t, "good")
	r := h.e.handle(launchBytes("demo"))
	assertState(t, r, "receipt-seen", "")
	if r.Receipt.ExecutionID == "" || r.Receipt.Epoch != 1 {
		t.Fatal(r)
	}
	marker := fileBytes(t, h, "attempt-"+gen)
	assertState(t, h.e.handle(launchBytes("demo")), "refused", "attempt_exists")
	assertState(t, h.e.handle(launchBytes("second")), "refused", "attempt_unstopped")
	restarted, err := newEngine(h.e.ctx, h.e.store, []account{h.e.accounts["demo"], h.e.accounts["second"]}, serverConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.closed {
		t.Fatal("restart admitted unresolved attempt")
	}
	fileBytes(t, h, "closed")
	assertState(t, restarted.handle(launchBytes("demo")), "refused", "attempt_exists")
	assertState(t, restarted.handle(launchBytes("second")), "refused", "admission_closed")
	if h.calls.Load() != 1 || !bytes.Equal(marker, fileBytes(t, h, "attempt-"+gen)) {
		t.Fatal("duplicate effect or changed marker")
	}
	var sent map[string]json.RawMessage
	b, err := os.ReadFile(filepath.Join(h.root, "launch.start.input"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(b, &sent) != nil || len(sent) != 8 || string(sent["op"]) != `"launch.start"` || string(sent["epoch"]) != "1" || string(sent["plan"]) != `"AAE="` || string(sent["generation"]) != `"`+gen+`"` {
		t.Fatal("launcher envelope", string(b))
	}
}

// Intended create and fsync failures retain debt and never reach the runner;
// success is exercised nearby, including actual file and directory fsync.
func TestMarkerFailureNoChild(t *testing.T) {
	for _, fault := range []string{"file_sync", "dir_sync", "existing", "symlink"} {
		t.Run(fault, func(t *testing.T) {
			h := fixture(t, "good")
			real := h.e.store.sync
			switch fault {
			case "file_sync", "dir_sync":
				calls := 0
				h.e.store.sync = func(f *os.File) error {
					calls++
					if fault == "file_sync" && calls == 1 || fault == "dir_sync" && calls == 2 {
						return syscall.EIO
					}
					return real(f)
				}
			case "existing":
				if err := h.e.store.create("attempt-"+gen, []byte("sentinel")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("sentinel", filepath.Join(h.root, "state", "attempt-"+gen)); err != nil {
					t.Fatal(err)
				}
			}
			assertState(t, h.e.handle(launchBytes("demo")), "refused", "marker_uncertain")
			if h.calls.Load() != 0 || !h.e.closed {
				t.Fatal("marker failure launched")
			}
			if _, err := os.Lstat(filepath.Join(h.root, "state", "attempt-"+gen)); err != nil {
				t.Fatal("marker discarded", err)
			}
			if fault == "existing" && string(fileBytes(t, h, "attempt-"+gen)) != "sentinel" {
				t.Fatal("marker overwritten")
			}
		})
	}
}

// Timeout, wrong identity, extra receipt member and early exit preserve the
// attempt, latch closed, and do not claim readiness or signal a process.
func TestLaunchUnknown(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{{"good", ""}, {"timeout", "receipt_timeout"}, {"mismatch", "receipt_mismatch"}, {"extra", "receipt_mismatch"}, {"exit", "launcher_exit"}} {
		t.Run(tc.mode, func(t *testing.T) {
			h := fixture(t, tc.mode)
			if tc.mode == "timeout" {
				expireReceiptAfterInput(h)
			}
			r := h.e.handle(launchBytes("demo"))
			if tc.code == "" {
				assertState(t, r, "receipt-seen", "")
				absentFile(t, h, "closed")
			} else {
				assertState(t, r, "unknown", tc.code)
				fileBytes(t, h, "closed")
				absentFile(t, h, "stopped-"+gen)
			}
			fileBytes(t, h, "attempt-"+gen)
			if h.calls.Load() != 1 {
				t.Fatal("runner calls")
			}
		})
	}
}

// More than twice the cap on BOTH real pipes must drain before the child can
// write its receipt; this catches a cap implemented by ceasing to read.
func TestLogsKeepDraining(t *testing.T) {
	h := fixture(t, "flood")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
	for _, stream := range []string{"stdout", "stderr"} {
		name := filepath.Join(h.root, "state", "output-"+gen+"-"+stream+".log")
		info, err := os.Stat(name)
		if err != nil || info.Size() != 1048576 {
			t.Fatal(stream, info, err)
		}
		if string(fileBytes(t, h, "output-"+gen+"-"+stream+".truncated")) != "true" {
			t.Fatal("truncation missing")
		}
	}
}

// Only matching ok:true/remaining:0 establishes absence. Exit zero alone,
// malformed bytes, helper refusal, remaining processes and deadline expiry do not.
func TestStopResults(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{{"good", ""}, {"remaining", "processes_remain"}, {"false", "target_not_owned"}, {"false-nonzero", "target_not_owned"}, {"overflow", "helper_result_invalid"}, {"malformed", "helper_result_invalid"}, {"empty", "helper_result_invalid"}, {"nonzero", "helper_exit"}, {"slow", "stop_timeout"}} {
		t.Run(tc.mode, func(t *testing.T) {
			h := fixture(t, "good")
			assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
			h.mode = tc.mode
			if tc.mode == "slow" {
				h.e.stopWait = 5 * time.Second
			}
			r := h.e.handle(stopBytes(10))
			if tc.code == "" {
				assertState(t, r, "absent", "")
				fileBytes(t, h, "stopped-"+gen)
				if r.Timing != "timely" {
					t.Fatal(r)
				}
			} else {
				assertState(t, r, "unresolved", tc.code)
				fileBytes(t, h, "closed")
				absentFile(t, h, "stopped-"+gen)
			}
			if tc.mode == "slow" && r.Timing != "missed" {
				t.Fatal(r)
			}
			b, err := os.ReadFile(filepath.Join(h.root, "exec.stop.input"))
			if err != nil {
				t.Fatal(err)
			}
			var sent map[string]json.RawMessage
			json.Unmarshal(b, &sent)
			if len(sent) != 4 || string(sent["op"]) != `"exec.stop"` || string(sent["v"]) != "1" {
				t.Fatal(string(b))
			}
			var args map[string]json.RawMessage
			json.Unmarshal(sent["args"], &args)
			if len(args) != 3 || string(args["generation"]) != `"`+gen+`"` || string(args["grace_seconds"]) != "10" {
				t.Fatal(string(b))
			}
		})
	}
}

// Retry keeps identical arguments and original deadline; changed grace refuses
// without another helper, and restart cannot invent a new monotonic budget.
func TestStopRetryAndRestart(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	h.mode = "remaining"
	assertState(t, h.e.handle(stopBytes(10)), "unresolved", "processes_remain")
	original := h.e.stops["demo"].deadline
	intent := fileBytes(t, h, "stop-"+gen)
	count := h.calls.Load()
	assertState(t, h.e.handle(stopBytes(11)), "refused", "stop_conflict")
	if h.calls.Load() != count {
		t.Fatal("conflict invoked helper")
	}
	h.mode = "good"
	assertState(t, h.e.handle(stopBytes(10)), "absent", "")
	if !h.e.stops["demo"].deadline.Equal(original) || !bytes.Equal(intent, fileBytes(t, h, "stop-"+gen)) {
		t.Fatal("retry renewed intent/deadline")
	}
	count = h.calls.Load()
	assertState(t, h.e.handle(stopBytes(10)), "absent", "")
	if h.calls.Load() != count {
		t.Fatal("terminal replay called helper")
	}
	restarted, err := newEngine(h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
	if err != nil {
		t.Fatal(err)
	}
	assertState(t, restarted.handle(stopBytes(11)), "refused", "stop_conflict")
	r := restarted.handle(stopBytes(10))
	assertState(t, r, "absent", "")
	if r.Timing != "timely" {
		t.Fatal(r)
	}
}

// Pending child waits release the state mutex, and a second launch is refused
// before any effect. Invalid intake has zero filesystem/runner effects.
func TestConcurrentAdmissionAndInvalidEffects(t *testing.T) {
	h := fixture(t, "timeout")
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var expired atomic.Bool
	h.e.now = func() time.Time {
		n := time.Now()
		if expired.Load() {
			return n.Add(time.Hour)
		}
		return n
	}
	h.e.readReceipt = func(account, string) ([]byte, error) {
		close(entered)
		<-release
		expired.Store(true)
		return nil, os.ErrNotExist
	}
	for _, b := range [][]byte{[]byte(`{"op":"demo.launch","account":"demo","request_id":"r","plan":"","uid":30000}`), stopBytes(0)} {
		assertState(t, h.e.handle(b), "refused", "request_invalid")
		absentFile(t, h, "attempt-"+gen)
	}
	done := make(chan response, 1)
	go func() { done <- h.e.handle(launchBytes("demo")) }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("launch did not reach receipt reader")
	}
	if h.calls.Load() != 1 {
		t.Fatal("launch not reached")
	}
	status := h.e.status("demo")
	if status.Attempt == nil {
		t.Fatal(status)
	}
	assertState(t, h.e.handle(launchBytes("second")), "refused", "launch_in_flight")
	assertState(t, h.e.handle(stopBytes(10)), "refused", "launch_in_flight")
	release <- struct{}{}
	select {
	case r := <-done:
		assertState(t, r, "unknown", "receipt_timeout")
	case <-time.After(30 * time.Second):
		t.Fatal("state lock held across wait")
	}
	if h.calls.Load() != 1 {
		t.Fatal("concurrent child")
	}
}

// An unresolved launch remains stoppable while closed. Conversely, restart
// after a verified stopped record does not fabricate an unstopped crash latch.
func TestClosedStopAndStoppedRestart(t *testing.T) {
	h := fixture(t, "timeout")
	expireReceiptAfterInput(h)
	assertState(t, h.e.handle(launchBytes("demo")), "unknown", "receipt_timeout")
	h.mode = "good"
	r := h.e.handle(stopBytes(10))
	assertState(t, r, "absent", "")
	if !r.Closed {
		t.Fatal("stop cleared irreversible latch")
	}
	control := fixture(t, "good")
	assertState(t, control.e.handle(launchBytes("demo")), "receipt-seen", "")
	assertState(t, control.e.handle(stopBytes(10)), "absent", "")
	restarted, err := newEngine(control.e.ctx, control.e.store, []account{control.e.accounts["demo"], control.e.accounts["second"]}, serverConfig{})
	if err != nil || restarted.closed || !restarted.stopped["demo"] {
		t.Fatal("stopped restart", err)
	}
	absentFile(t, control, "closed")
}

// Literal helper results independently bind request/execution IDs and exact
// nested keys. None of these mismatches can create a stopped record.
func TestStopResultIdentity(t *testing.T) {
	execution := "EX-0123456789abcdef0123456789abcdef"
	good := `{"request_id":"s","ok":true,"state":"active","result":{"execution_id":"` + execution + `","stopped_at":"2026-01-01T00:00:00Z","receipt":{"signalled":0,"killed":0,"remaining":0}}}`
	if code := stopResult([]byte(good), "s", execution); code != "" {
		t.Fatal(code)
	}
	for _, bad := range []string{
		strings.Replace(good, `"request_id":"s"`, `"request_id":"wrong"`, 1),
		strings.Replace(good, execution, "EX-ffffffffffffffffffffffffffffffff", 1),
		strings.Replace(good, `"remaining":0`, `"remaining":0,"remaining":0`, 1),
		strings.Replace(good, `"remaining":0`, `"remaining":0,"extra":0`, 1),
		strings.Replace(good, `"remaining":0`, `"remaining":-1`, 1),
		good + `{}`,
	} {
		if code := stopResult([]byte(bad), "s", execution); code != "helper_result_invalid" {
			t.Fatal(bad, code)
		}
	}
}

// Refusal replies must not report an open latch merely because no effect ran.
func TestRefusalPreservesClosedFact(t *testing.T) {
	h := fixture(t, "good")
	h.e.mu.Lock()
	err := h.e.closeAdmission("fixture")
	h.e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	r := h.e.handle(launchBytes("demo"))
	assertState(t, r, "refused", "admission_closed")
	if !r.Closed || h.calls.Load() != 0 {
		t.Fatal("false open fact", r)
	}
	absentFile(t, h, "attempt-"+gen)
}

// File handshakes synchronize the fake without assuming a scheduling latency.
func fakeWait(root, name string) {
	end := time.Now().Add(30 * time.Second)
	for time.Now().Before(end) {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	os.Exit(39)
}

// Keep real fsync and stdin delivery outside the timeout witness. Only once the
// fake has read its document does the test advance the launch observation clock.
func expireReceiptAfterInput(h *harness) {
	var expired atomic.Bool
	real := h.e.readReceipt
	h.e.now = func() time.Time {
		n := time.Now()
		if expired.Load() {
			return n.Add(time.Hour)
		}
		return n
	}
	h.e.readReceipt = func(a account, id string) ([]byte, error) {
		if _, err := os.Stat(filepath.Join(h.root, "launch.start.input")); err == nil {
			expired.Store(true)
		}
		return real(a, id)
	}
}
