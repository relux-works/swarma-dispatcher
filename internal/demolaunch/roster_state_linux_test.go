//go:build linux

package demolaunch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Durable request IDs obey the same grammar as intake, with valid 64-byte
// controls. No parser failure may return a partly populated record.
func TestDurableRequestIDGrammar(t *testing.T) {
	for _, id := range []string{"a", "A0._-", strings.Repeat("a", 64), ".a", "-a", "_a", "a:b", strings.Repeat("a", 65)} {
		valid := id == "a" || id == "A0._-" || id == strings.Repeat("a", 64)
		a, e := parseAttempt(marshal(attempt{RequestID: id, Digest: strings.Repeat("a", 64), ExecutionID: "EX-0123456789abcdef0123456789abcdef", Time: "2026-01-01T00:00:00Z"}))
		if (e == nil) != valid || (!valid && (!errors.Is(e, ErrInvalid) || a != (attempt{}))) {
			t.Fatalf("durable request ID accepted: attempt %q %v", id, e)
		}
		s, e := parseIntent(marshal(stopIntent{RequestID: id, ExecutionID: "EX-0123456789abcdef0123456789abcdef", Grace: 10, Time: "2026-01-01T00:00:00Z"}))
		if (e == nil) != valid || (!valid && (!errors.Is(e, ErrInvalid) || s != (stopIntent{}))) {
			t.Fatalf("durable request ID accepted: stop %q %v", id, e)
		}
	}
}

// A torn attempt cannot legitimize an empty stopped execution ID. An intact
// matching fact is the control; omitting closed refuses even when false would
// otherwise be a valid value. Loading must not rewrite either source record.
func TestStoppedRecordIdentity(t *testing.T) {
	for _, variant := range []string{"valid", "empty", "missing-closed"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			execution := "EX-0123456789abcdef0123456789abcdef"
			a := marshal(attempt{RequestID: "launch", Digest: strings.Repeat("a", 64), ExecutionID: execution, Time: "2026-01-01T00:00:00Z"})
			record := map[string]any{"execution_id": execution, "timing": "timely", "closed": false, "code": ""}
			if variant == "empty" {
				a = []byte(`{`)
				record["execution_id"] = ""
				record["timing"] = "unknown"
				record["closed"] = true
			}
			if variant == "missing-closed" {
				delete(record, "closed")
			}
			b := marshal(record)
			must(t, os.WriteFile(filepath.Join(h.root, "state", "attempt-"+gen), a, 0600))
			if variant != "empty" {
				intent := stopIntent{RequestID: "stop", ExecutionID: execution, Grace: 10, Time: "2026-01-01T00:00:00Z"}
				must(t, os.WriteFile(filepath.Join(h.root, "state", "stop-"+gen), marshal(intent), 0600))
			}
			must(t, os.WriteFile(filepath.Join(h.root, "state", "stopped-"+gen), b, 0600))
			e, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
			if variant == "valid" {
				must(t, err)
				if !e.status("demo").Stopped {
					t.Fatal("stopped control missing")
				}
			} else if e != nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("stopped record accepted: %s %v", variant, err)
			}
			if !bytes.Equal(fileBytes(t, h, "attempt-"+gen), a) || !bytes.Equal(fileBytes(t, h, "stopped-"+gen), b) || h.calls.Load() != 0 {
				t.Fatal("stopped validation changed source or launched")
			}
		})
	}
}

// Capture EIO with ENOSPC before naming .lost closes live admission and retries
// an unavailable latch on the next request. Intact loss alone stays open. All
// processes are fixture children; no privileged launcher or real storage fault.
func TestUnpersistedLossLatch(t *testing.T) {
	for _, variant := range []string{"persisted", "unpersisted", "retry"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "after-output")
			assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
			ch := h.e.launchChildren["demo"]
			c := ch.stdout
			c.mu.Lock()
			c.writeFile = func([]byte) (int, error) { return 0, syscall.EIO }
			if variant != "persisted" {
				c.onLoss = func() error { return syscall.ENOSPC }
			}
			c.mu.Unlock()
			real := h.e.store.createFault
			if variant == "retry" {
				h.e.mu.Lock()
				h.e.store.createFault = func(name string) error {
					if name == "closed" {
						return syscall.ENOSPC
					}
					return nil
				}
				h.e.mu.Unlock()
			}
			must(t, os.WriteFile(filepath.Join(h.root, "release-launch"), nil, 0600))
			waitChild(t, ch, time.Now().Add(30*time.Second))
			live := h.e.status("demo")
			wantLoss := "stdout:output_lost"
			if variant != "persisted" {
				wantLoss = "stdout:loss_unpersisted"
			}
			if live.Closed != (variant != "persisted") || !reflect.DeepEqual(live.OutputLoss, []string{wantLoss}) {
				t.Fatalf("unpersisted loss latch missing: %+v", live)
			}
			h.e.store.createFault = real
			before := h.calls.Load()
			if variant != "persisted" {
				assertState(t, h.e.handle(launchBytes("second")), "refused", "admission_closed")
				absentFile(t, h, "attempt-"+gen2)
				if h.calls.Load() != before {
					t.Fatal("unpersisted loss admitted launcher")
				}
				if !strings.Contains(string(fileBytes(t, h, "closed")), `"loss_unpersisted"`) {
					t.Fatal("unpersisted loss reason lost")
				}
				if got := h.e.status("demo"); !reflect.DeepEqual(got.OutputLoss, []string{"stdout:loss_unpersisted"}) {
					t.Fatal("live loss reason changed", got)
				}
			}
			// Stop removes the unrelated startup_unstopped reason as a possible mask.
			assertState(t, h.e.handle(stopBytes(10)), "absent", "")
			fresh := rebuild(t, h)
			if fresh.closed != (variant != "persisted") {
				t.Fatal("loss latch restart changed", fresh.closed)
			}
			if variant == "persisted" && !reflect.DeepEqual(fresh.status("demo").OutputLoss, []string{"stdout:output_lost"}) {
				t.Fatal("persisted loss control forgotten after restart")
			}
			if variant != "persisted" {
				before = h.calls.Load()
				assertState(t, fresh.handle(launchBytes("second")), "refused", "admission_closed")
				if h.calls.Load() != before {
					t.Fatal("restart admitted launcher")
				}
			}
		})
	}
}
