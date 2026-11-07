//go:build linux

package demolaunch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A pre-name create failure cannot acquire timing or pin a guessed request on
// restart. The fake helper retains its first ID; it is not the deployed helper.
func TestMissingIntentContinuation(t *testing.T) {
	for _, variant := range []string{"missing", "torn", "intact"} {
		t.Run(variant, func(t *testing.T) {
			intact := variant == "intact"
			h := fixture(t, "first-id")
			assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
			if !intact {
				h.e.store.createFault = func(n string) error {
					if n == "stop-"+gen {
						return syscall.EIO
					}
					return nil
				}
			}
			r := h.e.handle(stopBytes(10))
			assertState(t, r, "unresolved", "processes_remain")
			if !intact && r.Timing != "unknown" {
				t.Fatalf("missing intent claimed timing: %+v", r)
			}
			if !intact {
				if variant != "torn" {
					absentFile(t, h, "stop-"+gen)
				}
			}
			h.e.store.createFault = nil
			if variant == "torn" {
				must(t, h.e.store.create("stop-"+gen, nil))
			}
			e := rebuild(t, h)
			changed := bytes.Replace(stopBytes(11), []byte("stop-1"), []byte("stop-2"), 1)
			before := h.calls.Load()
			r = e.handle(changed)
			if intact {
				assertState(t, r, "refused", "stop_conflict")
				if h.calls.Load() != before {
					t.Fatal("intact intent changed helper arguments")
				}
			} else {
				assertState(t, r, "unresolved", "request_id_conflict")
				if h.calls.Load() != before+1 || r.Timing != "unknown" {
					t.Fatalf("unknown intent guess not forwarded: %+v", r)
				}
				if variant == "torn" {
					if len(fileBytes(t, h, "stop-"+gen)) != 0 {
						t.Fatal("torn intent overwritten")
					}
				} else {
					absentFile(t, h, "stop-"+gen)
				}
			}
			r = e.handle(stopBytes(10))
			assertState(t, r, "absent", "")
			if r.Timing != "unknown" || !r.Closed {
				t.Fatalf("restart claimed timing: %+v", r)
			}
			before = h.calls.Load()
			assertState(t, e.handle(stopBytes(10)), "absent", "")
			assertState(t, e.handle(changed), "refused", "stop_conflict")
			if h.calls.Load() != before {
				t.Fatal("terminal guess not replayed")
			}
			if !intact {
				if variant != "torn" {
					absentFile(t, h, "stop-"+gen)
				}
				again := rebuild(t, h)
				r = again.handle(stopBytes(10))
				assertState(t, r, "unresolved", "stopped_uncertain")
			}
		})
	}
}

// Missing and torn intents accept only closed, non-timely stopped facts. They
// cannot replay unknown arguments; an occupied completion remains uncertain.
func TestUnknownIntentStoppedStartup(t *testing.T) {
	for _, torn := range []bool{false, true} {
		for _, variant := range []string{"valid", "timely", "open"} {
			t.Run(jsonNumber(boolInt(torn))+variant, func(t *testing.T) {
				h := fixture(t, "good")
				assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
				if torn {
					must(t, h.e.store.create("stop-"+gen, nil))
				}
				record := stoppedRecord{ExecutionID: h.e.attempts["demo"].ExecutionID, Timing: "unknown", Closed: true}
				if variant == "timely" {
					record.Timing = "timely"
				}
				if variant == "open" {
					record.Closed = false
				}
				must(t, h.e.store.create("stopped-"+gen, marshal(record)))
				e, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
				if variant != "valid" {
					if !errors.Is(err, ErrInvalid) {
						t.Fatalf("invalid unknown-intent completion accepted: %v", err)
					}
					return
				}
				if err != nil || !e.closed {
					t.Fatalf("closed fact blocked startup: %v", err)
				}
				e.run = h.e.run
				r := e.handle(stopBytes(10))
				assertState(t, r, "unresolved", "stopped_uncertain")
				if r.Timing != "unknown" {
					t.Fatalf("closed fact invented timing: %+v", r)
				}
			})
		}
	}
}

// Both marker and latch can fail before naming. A later request retries the
// latch; an existing attempt keeps startup closed even while storage fails.
func TestLatchRecovery(t *testing.T) {
	for _, variant := range []string{"marker", "both", "startup", "startup-torn", "unknown", "stop"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			if strings.HasPrefix(variant, "startup") || variant == "stop" {
				assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
			}
			h.e.store.createFault = func(n string) error {
				if variant == "marker" && strings.HasPrefix(n, "attempt-") {
					return syscall.EIO
				}
				if variant == "both" && (n == "closed" || strings.HasPrefix(n, "attempt-")) {
					return syscall.EIO
				}
				if (strings.HasPrefix(variant, "startup") || variant == "unknown") && n == "closed" {
					return syscall.EIO
				}
				if variant == "stop" && (n == "closed" || strings.HasPrefix(n, "stop-")) {
					return syscall.EIO
				}
				return nil
			}
			switch variant {
			case "marker", "both":
				code := "marker_uncertain"
				if variant == "both" {
					code = "latch_uncertain"
				}
				assertState(t, h.e.handle(launchBytes("demo")), "refused", code)
				if h.calls.Load() != 0 {
					t.Fatal("marker fault called runner")
				}
			case "startup", "startup-torn":
				if variant == "startup-torn" {
					must(t, h.e.store.create("output-"+gen+"-stdout.truncated", nil))
				}
				oldEngine := h.e
				loaded, err := testEngine(t, oldEngine.ctx, oldEngine.store, []account{oldEngine.accounts["demo"], oldEngine.accounts["second"]}, serverConfig{})
				if err != nil {
					t.Fatalf("startup latch failure removed stop: %v", err)
				}
				loaded.run = oldEngine.run
				loaded.readReceipt = oldEngine.readReceipt
				loaded.receiptWait = oldEngine.receiptWait
				h.e = loaded
				if !h.e.closed || h.e.latchWritten {
					t.Fatal("startup latch failure removed stop")
				}
				r := h.e.handle(stopBytes(10))
				if r.State != "absent" || r.Code != "latch_uncertain" || r.Timing != "unknown" {
					t.Fatalf("startup stop unavailable: %+v", r)
				}
			case "unknown":
				assertState(t, h.e.unknown("fixture"), "unknown", "latch_uncertain")
			case "stop":
				r := h.e.handle(stopBytes(10))
				if r.State != "absent" || r.Code != "latch_uncertain" || r.Timing != "unknown" {
					t.Fatalf("intent uncertainty hidden: %+v", r)
				}
			}
			if variant == "both" {
				if rebuilt := rebuild(t, h); rebuilt.closed {
					t.Fatal("no-evidence marker failure invented restart latch")
				}
			}
			if variant == "marker" {
				fileBytes(t, h, "closed")
				e := rebuild(t, h)
				assertState(t, e.handle(launchBytes("second")), "refused", "admission_closed")
				if h.calls.Load() != 0 {
					t.Fatal("marker latch restart called runner")
				}
			}
			if variant != "marker" {
				if r := h.e.handle([]byte(`{"op":"demo.status","account":"demo"}`)); r.Code != "latch_uncertain" || !r.Closed {
					t.Fatalf("status hid latch uncertainty: %+v", r)
				}
			}
			h.e.store.createFault = nil
			r := h.e.handle([]byte(`{"op":"demo.status","account":"demo"}`))
			if !r.Closed || r.Code != "" {
				t.Fatalf("healed latch not retried: %+v", r)
			}
			fileBytes(t, h, "closed")
			e := rebuild(t, h)
			before := h.calls.Load()
			assertState(t, e.handle(launchBytes("second")), "refused", "admission_closed")
			if h.calls.Load() != before {
				t.Fatal("restart latch invoked runner")
			}
		})
	}
}

// An intact product-written latch without any attempts must close a rebuilt
// engine; the empty-state positive control admits a launch.
func TestValidLatchStartup(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(jsonNumber(boolInt(closed)), func(t *testing.T) {
			h := fixture(t, "good")
			if closed {
				must(t, h.e.closeAdmission("fixture"))
			}
			e := rebuild(t, h)
			e.readReceipt = h.e.readReceipt
			r := e.handle(launchBytes("demo"))
			if closed {
				if r.State != "refused" || r.Code != "admission_closed" || h.calls.Load() != 0 {
					t.Fatalf("valid latch reopened: %+v calls=%d", r, h.calls.Load())
				}
			} else {
				assertState(t, r, "receipt-seen", "")
			}
		})
	}
}

// A real child remains idle across multiple 250ms polls, then writes both pipes.
func TestIdleDrainer(t *testing.T) {
	h := fixture(t, "idle-output")
	r := h.e.handle(launchBytes("demo"))
	if r.State != "receipt-seen" {
		t.Fatalf("idle child lost output: %+v", r)
	}
	waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
	for _, stream := range []string{"stdout", "stderr"} {
		if string(fileBytes(t, h, "output-"+gen+"-"+stream+".log")) != "after idle "+stream {
			t.Fatalf("idle output incomplete: %s", stream)
		}
	}
}

// Startup enforces metadata and size for every protected record family. Bytes
// are otherwise valid, so parseable-wrong checks do not mask the size predicate.
func TestStartupRecordMetadata(t *testing.T) {
	for _, kind := range []string{"attempt-", "stop-", "stopped-", "flag", "log"} {
		for _, fault := range []string{"valid", "mode", "link", "size", "size-torn"} {
			t.Run(kind+fault, func(t *testing.T) {
				h := fixture(t, "good")
				assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
				assertState(t, h.e.handle(stopBytes(10)), "absent", "")
				waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
				name := kind + gen
				if kind == "flag" {
					name = "output-" + gen + "-stdout.truncated"
					must(t, h.e.store.create(name, []byte("true")))
				}
				if kind == "log" {
					name = "output-" + gen + "-stdout.log"
				}
				path := filepath.Join(h.root, "state", name)
				switch fault {
				case "mode":
					must(t, os.Chmod(path, 0644))
				case "link":
					must(t, os.Link(path, filepath.Join(h.root, "alias")))
				case "size", "size-torn":
					b := fileBytes(t, h, name)
					limit := 4096
					if kind == "log" {
						limit = logCap
					}
					b = append(b, bytes.Repeat([]byte(" "), limit+1-len(b))...)
					if fault == "size-torn" {
						b = bytes.Repeat([]byte("{"), limit+1)
					}
					must(t, os.WriteFile(path, b, 0600))
				}
				_, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
				if fault == "valid" {
					must(t, err)
					return
				}
				want := ErrOwnership
				if fault == "size" || fault == "size-torn" {
					want = ErrLimit
					if kind == "log" {
						want = ErrInvalid
					}
				}
				if !errors.Is(err, want) {
					t.Fatalf("startup metadata guard %s/%s: got %v want %v", kind, fault, err, want)
				}
			})
		}
	}
}

// An inode left by failed fsync is not durable merely because create returns
// EEXIST. A healed request syncs that file and directory without rewriting it.
func TestLatchRetryExistingName(t *testing.T) {
	h := fixture(t, "good")
	real := h.e.store.sync
	h.e.store.sync = func(*os.File) error { return syscall.EIO }
	if err := h.e.closeAdmission("fixture"); !errors.Is(err, syscall.EIO) {
		t.Fatalf("latch sync fault hidden: %v", err)
	}
	original := fileBytes(t, h, "closed")
	r := h.e.handle([]byte(`{"op":"demo.status","account":"demo"}`))
	if r.Code != "latch_uncertain" || h.e.latchWritten {
		t.Fatalf("existing latch claimed durability: %+v", r)
	}
	var directory []bool
	h.e.store.sync = func(f *os.File) error {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		directory = append(directory, info.IsDir())
		return real(f)
	}
	r = h.e.handle([]byte(`{"op":"demo.status","account":"demo"}`))
	if r.Code != "" || !r.Closed || !h.e.latchWritten || len(directory) != 2 || directory[0] || !directory[1] {
		t.Fatalf("existing latch retry not synced: %+v sync=%v", r, directory)
	}
	if !bytes.Equal(original, fileBytes(t, h, "closed")) {
		t.Fatal("latch retry rewrote evidence")
	}
}
