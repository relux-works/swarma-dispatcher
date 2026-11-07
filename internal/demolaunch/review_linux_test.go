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
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Real reader controls exercise the owner/group/type/mode/link predicate.
// Ownership is tested by changing the expected UID, not by requiring chown/root.
func TestReceiptFileGuards(t *testing.T) {
	for _, variant := range []string{"valid", "owner", "group", "mode", "directory", "fifo", "symlink", "hardlink", "oversize"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			id := "EX-0123456789abcdef0123456789abcdef"
			dir := filepath.Join(h.root, gen)
			must(t, os.Mkdir(dir, 0700))
			name := filepath.Join(dir, id+".json")
			body := []byte(`{"execution_id":"` + id + `","epoch":1,"pid":12,"start_time":33,"boot_id":"` + boot + `","session_id":12}`)
			switch variant {
			case "directory":
				must(t, os.Mkdir(name, 0644))
			case "fifo":
				must(t, syscall.Mkfifo(name, 0644))
			case "symlink":
				must(t, os.WriteFile(filepath.Join(dir, "target"), body, 0644))
				must(t, os.Chmod(filepath.Join(dir, "target"), 0644))
				must(t, os.Symlink("target", name))
			default:
				must(t, os.WriteFile(name, body, 0644))
				must(t, os.Chmod(name, 0644))
			}
			switch variant {
			case "owner":
				h.e.receiptOwner++
			case "group":
				h.e.receiptGroup++
			case "mode":
				must(t, os.Chmod(name, 0600))
			case "hardlink":
				must(t, os.Link(name, filepath.Join(dir, "alias")))
			case "oversize":
				must(t, os.WriteFile(name, bytes.Repeat([]byte{'x'}, 4097), 0644))
				must(t, os.Chmod(name, 0644))
			}
			b, err := h.e.receiptBytes(h.e.accounts["demo"], id)
			if variant == "valid" {
				if err != nil || !bytes.Equal(b, body) {
					t.Fatal(string(b), err)
				}
			} else {
				want := ErrOwnership
				switch variant {
				case "owner":
					want = ErrIdentity
				case "symlink":
					want = syscall.ELOOP
				case "oversize":
					want = ErrConfig
				}
				if !errors.Is(err, want) || b != nil {
					t.Fatal("receipt refusal", variant, string(b), err, "want", want)
				}
			}
			// Exercise owner independently of the directory-owner check, using fstat.
			if variant == "valid" {
				f, e := os.Open(name)
				must(t, e)
				s, e := identity(f)
				f.Close()
				must(t, e)
				if !receiptMetadata(s, s.uid, s.gid) || receiptMetadata(s, s.uid+1, s.gid) || receiptMetadata(s, s.uid, s.gid+1) {
					t.Fatal("metadata identity guard")
				}
			}
		})
	}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// The one-shot child writes atomically and exits immediately. Waiting for its
// exit before the real reader exercises the old exit-before-receipt ordering.
func TestReceiptAfterObservedExit(t *testing.T) {
	h := fixture(t, "good")
	real := h.e.readReceipt
	first := true
	h.e.readReceipt = func(a account, id string) ([]byte, error) {
		h.e.mu.Lock()
		ch := h.e.launchChildren[a.Name]
		h.e.mu.Unlock()
		waitChild(t, ch, time.Now().Add(30*time.Second))
		if first {
			first = false
			return nil, os.ErrNotExist
		}
		return real(a, id)
	}
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	absentFile(t, h, "closed")
}

// Late evidence is never promoted, and EIO is not ENOENT. These exercise removal
// of the post-read deadline check and treating all read errors as absence.
func TestReceiptObservationFailures(t *testing.T) {
	for _, variant := range []string{"valid", "late", "read-error"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			real := h.e.readReceipt
			late := false
			h.e.now = func() time.Time {
				n := time.Now()
				if late {
					return n.Add(time.Hour)
				}
				return n
			}
			h.e.readReceipt = func(a account, id string) ([]byte, error) {
				b, e := real(a, id)
				if e == nil {
					if variant == "late" {
						late = true
					}
					if variant == "read-error" {
						return nil, syscall.EIO
					}
				}
				return b, e
			}
			r := h.e.handle(launchBytes("demo"))
			want := ""
			if variant == "late" {
				want = "receipt_timeout"
			}
			if variant == "read-error" {
				want = "receipt_mismatch"
			}
			if want == "" {
				assertState(t, r, "receipt-seen", "")
				absentFile(t, h, "closed")
			} else {
				assertState(t, r, "unknown", want)
				fileBytes(t, h, "closed")
			}
			if h.calls.Load() != 1 {
				t.Fatal("runner count")
			}
		})
	}
}

// Replay and continuation survive restart. These exercise early returns for an
// expired/lost clock and loss of the persisted terminal response.
func TestStopContinuationAndDurableReplay(t *testing.T) {
	for _, variant := range []string{"timely", "missed", "unknown"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
			if variant != "timely" {
				h.mode = "remaining"
				assertState(t, h.e.handle(stopBytes(13)), "unresolved", "processes_remain")
				h.mode = "good"
			}
			var originalInput []byte
			if variant != "timely" {
				var err error
				originalInput, err = os.ReadFile(filepath.Join(h.root, "exec.stop.input"))
				must(t, err)
			}
			e := h.e
			if variant == "missed" {
				e.stops["demo"].deadline = time.Now().Add(-time.Hour)
			}
			if variant == "unknown" {
				var err error
				e, err = testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
				must(t, err)
				e.run = h.e.run
			}
			before := h.calls.Load()
			r := e.handle(stopBytes(13))
			assertState(t, r, "absent", "")
			if r.Timing != variant || h.calls.Load() != before+1 || r.Closed != (variant != "timely") {
				t.Fatalf("stop continuation assertion: %+v calls=%d", r, h.calls.Load())
			}
			if originalInput != nil {
				again, err := os.ReadFile(filepath.Join(h.root, "exec.stop.input"))
				must(t, err)
				if !bytes.Equal(originalInput, again) {
					t.Fatal("continuation changed helper document")
				}
			}
			intent := fileBytes(t, h, "stop-"+gen)
			record := fileBytes(t, h, "stopped-"+gen)
			restarted, err := testEngine(t, context.Background(), e.store, []account{e.accounts["demo"]}, serverConfig{})
			must(t, err)
			restarted.run = func(string) *exec.Cmd { t.Fatal("replay invoked runner"); return nil }
			replay := restarted.handle(stopBytes(13))
			assertState(t, replay, "absent", "")
			if !reflect.DeepEqual(replay, r) || !bytes.Equal(record, fileBytes(t, h, "stopped-"+gen)) || !bytes.Equal(intent, fileBytes(t, h, "stop-"+gen)) {
				t.Fatalf("stop replay assertion: %+v", replay)
			}
			assertState(t, restarted.handle(stopBytes(14)), "refused", "stop_conflict")
			if variant == "timely" {
				absentFile(t, h, "closed")
			} else {
				fileBytes(t, h, "closed")
			}
		})
	}
}

// Literal bindings exercise refusal of path swaps, lost executor/request/grace fields, and
// replacement of newEngine's fixedRunner with another command constructor.
func TestPrivilegedBindings(t *testing.T) {
	h := fixture(t, "good")
	fresh, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
	must(t, err)
	if fresh.receiptWait != 10*time.Second || fresh.stopWait != 45*time.Second {
		t.Fatal("default runner/budgets changed")
	}
	if sudoPath != "/usr/bin/sudo" || launcherPath != "/usr/local/libexec/swarma/swarma-user-launcher" || managerPath != "/usr/local/libexec/swarma/swarma-user-manager" {
		t.Fatal("compiled paths changed")
	}
	r := h.e.handle([]byte(`{"op":"demo.launch","request_id":"launch-binding","account":"demo","plan":"AAE="}`))
	assertState(t, r, "receipt-seen", "")
	assertState(t, h.e.handle([]byte(`{"op":"demo.stop","request_id":"stop-binding","account":"demo","grace_seconds":17}`)), "absent", "")
	for i, expected := range []struct{ path, stdin string }{
		{"/usr/local/libexec/swarma/swarma-user-launcher", `{"v":1,"op":"launch.start","request_id":"launch-binding","generation":"0123456789abcdef0123456789abcdef","execution_id":"` + r.Receipt.ExecutionID + `","epoch":1,"executor":"demo-executor","plan":"AAE="}`},
		{"/usr/local/libexec/swarma/swarma-user-manager", `{"v":1,"op":"exec.stop","request_id":"stop-binding","args":{"generation":"0123456789abcdef0123456789abcdef","execution_id":"` + r.Receipt.ExecutionID + `","grace_seconds":17}}`},
	} {
		b, err := os.ReadFile(filepath.Join(h.root, "call-"+jsonNumber(i+1)+".json"))
		must(t, err)
		var got struct{ Path, Stdin string }
		must(t, json.Unmarshal(b, &got))
		if got.Path != expected.path || got.Stdin != expected.stdin {
			t.Fatalf("call=%d got=%+v want=%+v", i, got, expected)
		}
	}
}

// Capture failures after the reply remain durable, visible facts while the
// real child writes beyond pipe capacity. Intended witness for silent post-receipt log loss.
func TestPostReceiptOutputLoss(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(jsonNumber(boolInt(fail)), func(t *testing.T) {
			h := fixture(t, "after-output")
			assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
			ch := h.e.launchChildren["demo"]
			if fail {
				ch.stdout.mu.Lock()
				ch.stdout.writeFile = func([]byte) (int, error) { return 0, syscall.EIO }
				ch.stdout.mu.Unlock()
			}
			must(t, os.WriteFile(filepath.Join(h.root, "release-launch"), nil, 0600))
			waitChild(t, ch, time.Now().Add(30*time.Second))
			if ch.err != nil {
				t.Fatal("child write failed", ch.err)
			}
			if _, err := os.Stat(filepath.Join(h.root, "output-complete")); err != nil {
				t.Fatal("child did not finish output", err)
			}
			r := h.e.status("demo")
			if fail {
				if !reflect.DeepEqual(r.OutputLoss, []string{"stdout:output_lost"}) {
					t.Fatalf("output loss assertion: %+v", r)
				}
				if string(fileBytes(t, h, "output-"+gen+"-stdout.lost")) != `"output_lost"` {
					t.Fatal("loss not durable")
				}
			} else if len(r.OutputLoss) != 0 {
				t.Fatalf("output loss assertion: %+v", r)
			}
			restarted, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
			must(t, err)
			if !reflect.DeepEqual(restarted.status("demo").OutputLoss, r.OutputLoss) {
				t.Fatal("loss forgotten")
			}
		})
	}
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Valid controls and each durable failure row assert exact codes, runner counts
// and retained markers. Intended witness for omission of create/sync failure handling.
func TestEffectFailureRows(t *testing.T) {
	for _, variant := range []string{"valid", "log_unavailable", "launcher_io", "stop_intent_uncertain", "stopped_uncertain", "latch_uncertain", "helper_io"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			if variant == "log_unavailable" {
				must(t, h.e.store.create("output-"+gen+"-stdout.log", []byte("original")))
			}
			if variant == "launcher_io" {
				h.e.run = func(string) *exec.Cmd {
					h.calls.Add(1)
					return exec.Command(filepath.Join(h.root, "absent-executable"))
				}
			}
			r := h.e.handle(launchBytes("demo"))
			if variant == "log_unavailable" || variant == "launcher_io" {
				assertState(t, r, "unknown", variant)
				fileBytes(t, h, "closed")
				fileBytes(t, h, "attempt-"+gen)
				want := int32(0)
				if variant == "log_unavailable" && string(fileBytes(t, h, "output-"+gen+"-stdout.log")) != "original" {
					t.Fatal("existing log modified")
				}
				if variant == "launcher_io" {
					want = 1
				}
				if h.calls.Load() != want {
					t.Fatal(h.calls.Load())
				}
				return
			}
			assertState(t, r, "receipt-seen", "")
			waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
			real := h.e.store.sync
			h.e.store.sync = func(f *os.File) error {
				name := filepath.Base(f.Name())
				if variant == "stop_intent_uncertain" && name == "stop-"+gen || variant == "stopped_uncertain" && name == "stopped-"+gen || variant == "latch_uncertain" && name == "closed" {
					return syscall.EIO
				}
				return real(f)
			}
			if variant == "latch_uncertain" {
				h.mode = "remaining"
			}
			if variant == "helper_io" {
				h.e.run = func(string) *exec.Cmd { h.calls.Add(1); return exec.Command(filepath.Join(h.root, "absent-helper")) }
			}
			r = h.e.handle(stopBytes(10))
			if variant == "valid" || variant == "stop_intent_uncertain" {
				assertState(t, r, "absent", "")
				if variant == "stop_intent_uncertain" {
					if !r.Closed || h.calls.Load() != 2 {
						t.Fatal(r, h.calls.Load())
					}
					return
				}
				absentFile(t, h, "closed")
			} else {
				assertState(t, r, "unresolved", variant)
				if !r.Closed {
					t.Fatal(r)
				}
				fileBytes(t, h, "closed")
			}
			fileBytes(t, h, "attempt-"+gen)
			fileBytes(t, h, "stop-"+gen)
			want := int32(2)
			if h.calls.Load() != want {
				t.Fatal(h.calls.Load())
			}
			if variant != "valid" && variant != "stopped_uncertain" {
				absentFile(t, h, "stopped-"+gen)
			}
		})
	}
}

// Resource-state controls exercise the reaping/drainer/admission guards.
// Synthetic channels isolate output-pending; real child pipes are covered above.
func TestAdmissionResourceRows(t *testing.T) {
	for _, variant := range []string{"valid", "child_unreaped", "output_pending", "stop_in_flight"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
			assertState(t, h.e.handle(stopBytes(10)), "absent", "")
			waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
			ch := h.e.launchChildren["demo"]
			done, out := ch.done, ch.outputDone
			if variant == "child_unreaped" {
				ch.done = make(chan struct{})
			}
			if variant == "output_pending" {
				ch.outputDone = make(chan struct{})
			}
			if variant == "stop_in_flight" {
				h.e.stops["demo"].active = true
			}
			before := h.calls.Load()
			r := h.e.handle(launchBytes("second"))
			ch.done = done
			ch.outputDone = out
			h.e.stops["demo"].active = false
			if variant == "valid" {
				assertState(t, r, "receipt-seen", "")
				if h.calls.Load() != before+1 {
					t.Fatal("no launch")
				}
			} else {
				assertState(t, r, "refused", variant)
				absentFile(t, h, "attempt-"+gen2)
				if h.calls.Load() != before {
					t.Fatal("forbidden launch")
				}
			}
		})
	}
	h := fixture(t, "good")
	assertState(t, h.e.handle(stopBytes(10)), "refused", "attempt_missing")
	assertState(t, h.e.handle(launchBytes("missing")), "refused", "account_unknown")
	assertState(t, h.e.handle([]byte(`{"op":"demo.status","account":"demo"}`)), "facts", "")
	if h.calls.Load() != 0 {
		t.Fatal("refusal effect")
	}
	absentFile(t, h, "attempt-"+gen)
}

// Top-level and nested member-set controls exercise refusal of permissive helper decoding;
// zero-exit, stdout overflow and nonzero ok:false use actual children nearby.
func TestHelperMemberSets(t *testing.T) {
	id := "EX-0123456789abcdef0123456789abcdef"
	good := `{"request_id":"s","ok":true,"state":"active","result":{"execution_id":"` + id + `","stopped_at":"2026-01-01T00:00:00Z","receipt":{"signalled":1,"killed":0,"remaining":0}}}`
	bad := `{"request_id":"s","ok":false,"error":{"code":"denied","message":"no"}}`
	if stopResult([]byte(good), "s", id) != "" || stopResult([]byte(bad), "s", id) != "denied" {
		t.Fatal("controls")
	}
	for _, b := range []string{strings.Replace(good, `"state":"active"`, `"state":""`, 1), strings.Replace(bad, `"code":"denied"`, `"code":""`, 1), strings.Replace(good, `"ok":true,`, `"ok":true,"error":{"code":"denied","message":"no"},`, 1), strings.Replace(good, `"state":"active",`, "", 1), strings.Replace(good, `"result":`, `"extra":1,"result":`, 1), strings.Replace(good, `"execution_id":`, `"extra":1,"execution_id":`, 1), strings.Replace(good, `"signalled":1,`, "", 1), strings.Replace(good, `"killed":0`, `"killed":-1`, 1), strings.Replace(good, `"stopped_at":"2026-01-01T00:00:00Z"`, `"stopped_at":"bad"`, 1), strings.Replace(bad, `"error":`, `"state":"active","error":`, 1), strings.Replace(bad, `"message":"no"`, `"message":"no","extra":1`, 1)} {
		if got := stopResult([]byte(b), "s", id); got != "helper_result_invalid" {
			t.Fatalf("helper member accepted: %s got=%s", b, got)
		}
	}
}

// A real running helper occupies its stop slot, without holding engine.mu.
// The sequential positive control and cross-account conflict exercise
// active-child and caller-scoped stop-ID guards.
func TestStopConcurrencyAndIDScope(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	h.mode = "hold-stop"
	done := make(chan response, 1)
	go func() { done <- h.e.handle(stopBytes(10)) }()
	end := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(h.root, "exec.stop.input")); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("helper intake not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertState(t, h.e.handle(stopBytes(10)), "refused", "stop_in_flight")
	assertState(t, h.e.status("demo"), "facts", "")
	if h.calls.Load() != 2 {
		t.Fatal("concurrent helper")
	}
	must(t, os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600))
	select {
	case r := <-done:
		assertState(t, r, "absent", "")
	case <-time.After(30 * time.Second):
		t.Fatal("helper did not finish")
	}
	waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
	h.mode = "good"
	assertState(t, h.e.handle(launchBytes("second")), "receipt-seen", "")
	assertState(t, h.e.handle([]byte(`{"op":"demo.stop","request_id":"stop-1","account":"second"}`)), "refused", "stop_conflict")
	absentFile(t, h, "stop-"+gen2)
	assertState(t, h.e.handle([]byte(`{"op":"demo.stop","request_id":"stop-2","account":"second"}`)), "absent", "")
}

// Post-read metadata/name/ancestor changes cannot become evidence. The injected
// read action still reads the real descriptor before changing one fixture fact.
func TestReceiptPostReadChecks(t *testing.T) {
	for _, variant := range []string{"valid", "mode", "replace", "size", "ancestor"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			h.e.readFile = func(r io.Reader, limit int) ([]byte, error) {
				b, err := readSnapshot(r, limit)
				if err != nil {
					return b, err
				}
				f := r.(*os.File)
				name := filepath.Join(h.root, gen, f.Name())
				switch variant {
				case "mode":
					must(t, os.Chmod(name, 0600))
				case "replace":
					must(t, os.Rename(name, name+".old"))
					must(t, os.WriteFile(name, b, 0644))
					must(t, os.Chmod(name, 0644))
				case "size":
					f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
					must(t, err)
					_, err = f.Write([]byte(" "))
					must(t, err)
					must(t, f.Close())
				case "ancestor":
					must(t, os.Chmod(filepath.Join(h.root, gen), 0770))
				}
				return b, nil
			}
			r := h.e.handle(launchBytes("demo"))
			if variant == "valid" {
				assertState(t, r, "receipt-seen", "")
				absentFile(t, h, "closed")
			} else {
				assertState(t, r, "unknown", "receipt_mismatch")
				fileBytes(t, h, "closed")
			}
			if h.calls.Load() != 1 {
				t.Fatal("runner count")
			}
		})
	}
}

// A late verified absence must retain closed even if the first latch fsync
// fails. The persisted terminal response replays its uncertainty after restart.
func TestLateAbsenceLatchUncertainty(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	// Preserve the original intent with an expired in-memory monotonic deadline.
	at := h.e.attempts["demo"]
	intent := stopIntent{RequestID: "stop-1", ExecutionID: at.ExecutionID, Grace: 10, Time: "2026-01-01T00:00:00Z"}
	must(t, h.e.store.create("stop-"+gen, marshal(intent)))
	h.e.stops["demo"] = &stopRecord{intent: intent, deadline: time.Now().Add(-time.Hour)}
	real := h.e.store.sync
	failed := false
	h.e.store.sync = func(f *os.File) error {
		if f.Name() == "closed" {
			failed = true
			return syscall.EIO
		}
		return real(f)
	}
	r := h.e.handle(stopBytes(10))
	assertState(t, r, "absent", "latch_uncertain")
	if !failed || !r.Closed || r.Timing != "missed" {
		t.Fatal(r, failed)
	}
	fileBytes(t, h, "stopped-"+gen)
	h.e.store.sync = real
	restarted, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
	must(t, err)
	replay := restarted.handle(stopBytes(10))
	assertState(t, replay, "absent", "latch_uncertain")
	if !replay.Closed || replay.Timing != "missed" {
		t.Fatal(replay)
	}
}

// A timed-out call retains custody of its real helper and both pipes. Intended witness for
// deleting the child/output guards after active clears; completion then permits
// an identical late continuation, without refilling its original deadline.
func TestHelperResourcesAfterTimeout(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	h.mode = "hold-stop"
	h.e.stopWait = 5 * time.Second
	r := h.e.handle(stopBytes(10))
	assertState(t, r, "unresolved", "stop_timeout")
	if r.Timing != "missed" {
		t.Fatal(r)
	}
	deadline := h.e.stops["demo"].deadline
	intent := fileBytes(t, h, "stop-"+gen)
	assertState(t, h.e.handle(stopBytes(10)), "refused", "stop_in_flight")
	if h.calls.Load() != 2 {
		t.Fatal("overlapping helper")
	}
	must(t, os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600))
	ch := h.e.stops["demo"].child
	waitChild(t, ch, time.Now().Add(30*time.Second))
	output := ch.outputDone
	ch.outputDone = make(chan struct{})
	assertState(t, h.e.handle(stopBytes(10)), "refused", "stop_in_flight")
	ch.outputDone = output
	if h.calls.Load() != 2 {
		t.Fatal("overlapping output")
	}
	h.mode = "good"
	h.e.stopWait = 45 * time.Second
	r = h.e.handle(stopBytes(10))
	assertState(t, r, "absent", "")
	if r.Timing != "missed" || !r.Closed || h.calls.Load() != 3 || !deadline.Equal(h.e.stops["demo"].deadline) || !bytes.Equal(intent, fileBytes(t, h, "stop-"+gen)) {
		t.Fatal(r)
	}
}

// Capture error on either pipe before observation is not a successful launch.
// A real child plus the same successful reader is the neighboring control.
func TestCaptureFailureBeforeReceipt(t *testing.T) {
	for _, stream := range []string{"valid", "stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			h := fixture(t, "hold-launch")
			real := h.e.readReceipt
			first := true
			h.e.readReceipt = func(a account, id string) ([]byte, error) {
				if stream != "valid" && first {
					first = false
					ch := h.e.launchChildren[a.Name]
					c := ch.stdout
					if stream == "stderr" {
						c = ch.stderr
					}
					c.mu.Lock()
					c.writeFile = func([]byte) (int, error) { return 0, syscall.EIO }
					c.mu.Unlock()
					c.write([]byte("fixture output"))
					return nil, os.ErrNotExist
				}
				return real(a, id)
			}
			r := h.e.handle(launchBytes("demo"))
			if stream == "valid" {
				assertState(t, r, "receipt-seen", "")
				absentFile(t, h, "closed")
			} else {
				assertState(t, r, "unknown", "log_unavailable")
				fileBytes(t, h, "closed")
				fileBytes(t, h, "output-"+gen+"-"+stream+".lost")
			}
			if h.calls.Load() != 1 {
				t.Fatal("runner count")
			}
		})
	}
}

// A created loss marker with uncertain fsync is not represented as durable by
// live status. Successful fsync is covered by TestPostReceiptOutputLoss.
func TestOutputLossPersistenceUncertain(t *testing.T) {
	h := fixture(t, "after-output")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	ch := h.e.launchChildren["demo"]
	c := ch.stdout
	c.mu.Lock()
	c.writeFile = func([]byte) (int, error) { return 0, syscall.EIO }
	// Create real bytes but explicitly fail the durability result, without racing
	// the shared store's fsync function with the other real pipe drainer.
	c.onLoss = func() error {
		err := h.e.store.create("output-"+gen+"-stdout.lost", []byte(`"output_lost"`))
		return errors.Join(err, syscall.EIO)
	}
	c.mu.Unlock()
	must(t, os.WriteFile(filepath.Join(h.root, "release-launch"), nil, 0600))
	waitChild(t, ch, time.Now().Add(30*time.Second))
	if got := h.e.status("demo").OutputLoss; !reflect.DeepEqual(got, []string{"stdout:loss_unpersisted"}) {
		t.Fatal(got)
	}
	fileBytes(t, h, "output-"+gen+"-stdout.lost")
}
