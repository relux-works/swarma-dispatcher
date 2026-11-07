//go:build linux

package demolaunch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Exact config fields and principal formats are checked without deployment
// ownership; each mutation has the same valid server/client control.
func TestConfigResidueGuards(t *testing.T) {
	for _, client := range []bool{false, true} {
		for _, fault := range []string{"valid", "version", "relative", "quote", "same-uid", "case-key"} {
			t.Run(jsonNumber(boolInt(client))+fault, func(t *testing.T) {
				c := cfgFixture()
				if client {
					for _, k := range []string{"service_account", "executor", "receipt_root", "state_dir"} {
						delete(c, k)
					}
				}
				caller := c["caller"].(map[string]any)
				switch fault {
				case "version":
					c["v"] = 2
				case "relative":
					caller["dir"] = "relative"
				case "quote":
					caller["dir"] = `/home/a"b`
				case "same-uid":
					caller["uid"] = c["service_uid"]
				case "case-key":
					caller["Dir"] = caller["dir"]
					delete(caller, "dir")
				}
				var err error
				if client {
					_, err = parseClient(marshal(c))
				} else {
					_, err = parseServer(marshal(c))
				}
				if fault == "valid" {
					if err != nil {
						t.Fatalf("valid config refused: %v", err)
					}
				} else if !errors.Is(err, ErrConfig) {
					t.Fatalf("config residue guard accepted %s client=%v: %v", fault, client, err)
				}
			})
		}
	}
}

// Depth is driven through object before any typed-schema refusal can mask it;
// request member and execution-ID tests use otherwise valid literal documents.
func TestProtocolResidueGuards(t *testing.T) {
	for _, depth := range []int{7, 8} {
		b := []byte(`{"x":` + strings.Repeat("[", depth) + `1` + strings.Repeat("]", depth) + `}`)
		_, err := object(b, 4096, "x", "")
		if depth == 7 && err != nil {
			t.Fatalf("valid depth refused: %v", err)
		}
		if depth == 8 && !errors.Is(err, ErrInvalid) {
			t.Fatalf("JSON depth guard accepted: %v", err)
		}
	}
	for _, extra := range []bool{false, true} {
		b := `{"op":"demo.launch","request_id":"r","account":"demo","plan":""}`
		if extra {
			b = strings.TrimSuffix(b, "}") + `,"grace_seconds":10}`
		}
		_, err := parseRequest([]byte(b))
		if !extra && err != nil {
			t.Fatalf("valid launch refused: %v", err)
		}
		if extra && !errors.Is(err, ErrInvalid) {
			t.Fatalf("launch member guard accepted: %v", err)
		}
	}
	for _, execution := range []string{"EX-0123456789abcdef0123456789abcdef", "EX-0123456789ABCDEF0123456789abcdef", "EX-0123456789abcdeg0123456789abcdef"} {
		b := []byte(`{"execution_id":"` + execution + `","epoch":1,"pid":1,"start_time":1,"boot_id":"12345678-1234-1234-1234-123456789abc","session_id":1}`)
		_, err := parseReceipt(b, execution)
		if execution == "EX-0123456789abcdef0123456789abcdef" {
			if err != nil {
				t.Fatalf("valid execution refused: %v", err)
			}
		} else if !errors.Is(err, ErrInvalid) {
			t.Fatalf("execution format accepted: %v", err)
		}
	}
}

// The client sees valid wire frames and exact reply members; only total JSON
// length differs at the 16KiB boundary. This is not privileged deployment.
func TestReplyByteBoundary(t *testing.T) {
	for _, size := range []int{16384, 16385} {
		t.Run(jsonNumber(size), func(t *testing.T) {
			a, b := socketPair(t)
			done := make(chan error, 1)
			go func() {
				defer wakeClose(b)
				w, err := newSocketIO(b, time.Now())
				if err != nil {
					done <- err
					return
				}
				if _, err = w.readFrame(); err != nil {
					done <- err
					return
				}
				if err = w.writeFrame(`{"protocol":"swarma-demo-dispatcher/1"}`); err != nil {
					done <- err
					return
				}
				if _, err = w.readFrame(); err != nil {
					done <- err
					return
				}
				reply := `{"state":"facts","closed":false}`
				done <- w.writeFrame(reply + strings.Repeat(" ", size-len(reply)))
			}()
			reply, err := clientExchange(a, time.Now(), []byte(`{"op":"demo.status","account":"demo"}`))
			if size == 16384 {
				if err != nil || len(reply) != size {
					t.Fatalf("valid reply boundary refused: %v bytes=%d", err, len(reply))
				}
			} else if !errors.Is(err, ErrInvalid) || reply != "" {
				t.Fatalf("reply byte bound accepted: %v bytes=%d", err, len(reply))
			}
			wakeClose(a)
			select {
			case err := <-done:
				must(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("reply fixture stalled")
			}
		})
	}
}

type countedSnapshot struct {
	r io.Reader
	n int
}

func (r *countedSnapshot) Read(b []byte) (int, error) { n, e := r.r.Read(b); r.n += n; return n, e }

// Oversize refusal must stop reading at limit+1, not drain the finite source.
func TestProtectedIntakeBound(t *testing.T) {
	for _, size := range []int{8, 64} {
		r := &countedSnapshot{r: bytes.NewReader(bytes.Repeat([]byte{'x'}, size))}
		b, err := readSnapshot(r, 8)
		if size == 8 {
			if err != nil || len(b) != 8 || r.n != 8 {
				t.Fatalf("valid intake refused: %v bytes=%d", err, r.n)
			}
		} else if !errors.Is(err, ErrConfig) || b != nil || r.n != 9 {
			t.Fatalf("protected intake overread: err=%v read=%d", err, r.n)
		}
	}
}

// Real directory enumeration tests the finite scan bound independently of the
// later unknown-name guard, which also refuses overfull production state.
func TestStateEnumerationBound(t *testing.T) {
	h := fixture(t, "good")
	for i := 0; i < 255; i++ {
		must(t, h.e.store.create("fixture-"+jsonNumber(i), nil))
	}
	names, err := h.e.store.names()
	if err != nil || len(names) != 255 {
		t.Fatalf("enumeration control: %v count=%d", err, len(names))
	}
	must(t, h.e.store.create("fixture-255", nil))
	names, err = h.e.store.names()
	if !errors.Is(err, ErrLimit) || names != nil {
		t.Fatalf("state enumeration bound accepted: %v count=%d", err, len(names))
	}
}

// Expected-owner injection tests actual protected-file reads without chown.
func TestStateReadOwner(t *testing.T) {
	h := fixture(t, "good")
	must(t, h.e.store.create("probe", []byte("sentinel")))
	b, err := h.e.store.read("probe")
	if err != nil || string(b) != "sentinel" {
		t.Fatalf("owner control: %v", err)
	}
	h.e.store.uid++
	b, err = h.e.store.read("probe")
	if !errors.Is(err, ErrOwnership) || b != nil {
		t.Fatalf("state owner accepted: %v", err)
	}
	h.e.store.uid--
	if string(fileBytes(t, h, "probe")) != "sentinel" {
		t.Fatal("owner refusal modified evidence")
	}
}

// Missing, intact and torn facts have distinct startup/status observations;
// intact intent recovers the execution identity of a torn attempt for stop.
func TestTornResidueRecovery(t *testing.T) {
	for _, variant := range []string{"valid-latch", "torn-latch", "orphan-torn", "identity"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			switch variant {
			case "valid-latch":
				must(t, h.e.store.create("closed", []byte(`{"reason":"fixture"}`)))
			case "torn-latch":
				must(t, h.e.store.create("closed", nil))
			case "orphan-torn":
				must(t, h.e.store.create("stopped-"+gen, nil))
			case "identity":
				must(t, h.e.store.create("attempt-"+gen, nil))
				must(t, h.e.store.create("stop-"+gen, []byte(`{"request_id":"stop-1","execution_id":"EX-0123456789abcdef0123456789abcdef","grace_seconds":10,"time":"2026-01-01T00:00:00Z"}`)))
			}
			e, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
			if err != nil {
				t.Fatalf("torn recovery unavailable: %v", err)
			}
			if !e.closed {
				t.Fatal("torn recovery reopened")
			}
			if variant == "torn-latch" && !reflect.DeepEqual(e.status("demo").Torn, []string{"closed"}) {
				t.Fatalf("torn latch omitted from status: %+v", e.status("demo"))
			}
			if variant == "valid-latch" && len(e.status("demo").Torn) != 0 {
				t.Fatal("valid latch called torn")
			}
			if variant == "identity" {
				e.run = h.e.run
				r := e.handle(stopBytes(10))
				if r.State != "absent" || r.Timing != "unknown" {
					t.Fatalf("torn attempt lost recoverable identity: %+v", r)
				}
			}
		})
	}
}

// Live capture failure and unreadable durable evidence are not interchangeable
// with an intact output_lost fact. Synthetic captures use no child goroutine.
func TestLossStatusResidues(t *testing.T) {
	for _, kind := range []string{"valid", "unreadable", "live"} {
		t.Run(kind, func(t *testing.T) {
			h := fixture(t, "good")
			want := "stdout:output_lost"
			if kind == "live" {
				h.e.launchChildren["demo"] = &child{stdout: &capture{err: io.ErrUnexpectedEOF}, stderr: &capture{}}
				want = "stdout:loss_unpersisted"
				defer delete(h.e.launchChildren, "demo")
			} else {
				must(t, h.e.store.create("output-"+gen+"-stdout.lost", []byte(`"output_lost"`)))
				if kind == "unreadable" {
					must(t, os.Chmod(filepath.Join(h.root, "state", "output-"+gen+"-stdout.lost"), 0644))
					want = "stdout:evidence_unavailable"
				}
			}
			got := h.e.status("demo").OutputLoss
			if !reflect.DeepEqual(got, []string{want}) {
				t.Fatalf("loss status confused evidence: got=%v want=%s", got, want)
			}
		})
	}
}

// An existing malformed latch cannot gain a durable acknowledgement; its inode
// and bytes remain untouched. A protected existing latch is the valid control.
func TestExistingLatchMetadata(t *testing.T) {
	for _, bad := range []bool{false, true} {
		h := fixture(t, "good")
		must(t, h.e.store.create("closed", []byte("sentinel")))
		if bad {
			must(t, os.Chmod(filepath.Join(h.root, "state", "closed"), 0644))
		}
		err := h.e.closeAdmission("fixture")
		if bad {
			if !errors.Is(err, ErrOwnership) || h.e.latchWritten {
				t.Fatalf("bad existing latch acknowledged: %v", err)
			}
		} else if err != nil || !h.e.latchWritten {
			t.Fatalf("existing latch control: %v", err)
		}
		content, readErr := os.ReadFile(filepath.Join(h.root, "state", "closed"))
		info, statErr := os.Stat(filepath.Join(h.root, "state", "closed"))
		mode := os.FileMode(0600)
		if bad {
			mode = 0644
		}
		if readErr != nil || statErr != nil || string(content) != "sentinel" || info.Mode().Perm() != mode {
			t.Fatalf("latch metadata refusal changed evidence: read=%v stat=%v", readErr, statErr)
		}

	}
}

// Log creation sync errors prevent a usable capture; a clean sync succeeds.
func TestLogCreationSync(t *testing.T) {
	for _, fail := range []bool{false, true} {
		h := fixture(t, "good")
		real := h.e.store.sync
		if fail {
			h.e.store.sync = func(f *os.File) error {
				if strings.HasSuffix(f.Name(), ".log") {
					return syscall.EIO
				}
				return real(f)
			}
		}
		c, err := h.e.logCapture(gen, "stdout")
		if c != nil {
			c.file.Close()
		}
		if fail {
			if !errors.Is(err, syscall.EIO) || c != nil {
				t.Fatalf("log sync error ignored: %v", err)
			}
		} else if err != nil || c == nil {
			t.Fatalf("log sync control: %v", err)
		}
	}
}

// Capture callbacks retain truncate/final-sync failure and invoke the loss hook;
// these are deterministic descriptor faults, not an ENOSPC qualification.
func TestCaptureFailureResidues(t *testing.T) {
	for _, fail := range []bool{false, true} {
		lost := 0
		c := &capture{limit: 1, onTruncate: func() error {
			if fail {
				return syscall.ENOSPC
			}
			return nil
		}, onLoss: func() error { lost++; return nil }}
		c.write([]byte("ab"))
		b, overflow, err := c.snapshot()
		if string(b) != "a" || !overflow || (fail && (!errors.Is(err, syscall.ENOSPC) || lost != 1)) || (!fail && (err != nil || lost != 0)) {
			t.Fatalf("truncation failure discarded: %v lost=%d", err, lost)
		}
		f, e := os.CreateTemp(fixtureDir(t), "capture")
		must(t, e)
		if fail {
			must(t, f.Close())
		}
		r, w, e := os.Pipe()
		must(t, e)
		must(t, w.Close())
		lost = 0
		c = &capture{file: f, limit: 1, onLoss: func() error { lost++; return nil }}
		drain(context.Background(), r, c)
		_, _, err = c.snapshot()
		if fail && (err == nil || lost != 1) || !fail && (err != nil || lost != 0) {
			t.Fatalf("final capture failure discarded: %v lost=%d", err, lost)
		}
	}
}

// Helper error fields retain their documented identifier and byte bounds;
// message length 1024 is a valid neighboring control.
func TestHelperErrorFormats(t *testing.T) {
	for _, row := range []struct {
		code  string
		n     int
		valid bool
	}{{"denied", 1024, true}, {"bad/code", 2, false}, {"denied", 1025, false}} {
		b := marshal(map[string]any{"request_id": "s", "ok": false, "error": map[string]string{"code": row.code, "message": strings.Repeat("x", row.n)}})
		got := stopResult(b, "s", "EX-0123456789abcdef0123456789abcdef")
		want := "helper_result_invalid"
		if row.valid {
			want = "denied"
		}
		if got != want {
			t.Fatalf("helper error format accepted: code=%q size=%d got=%s want=%s", row.code, row.n, got, want)
		}
	}
}

// A case-folded alias is accepted by typed JSON decoding but must not satisfy
// the roster's exact member set. This does not change or reconcile its schema.
func TestRosterExactCase(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		b := rosterFixture(30000)
		if wrong {
			b = bytes.Replace(b, []byte(`"uid"`), []byte(`"UID"`), 1)
		}
		_, err := parseRoster(b, "demo-service")
		if wrong {
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("roster member case accepted: %v", err)
			}
		} else if err != nil {
			t.Fatalf("roster case control: %v", err)
		}
	}
}

// Flag refusal is tested independently of ancillary bytes; actual kernel
// generation of an empty truncated control is outside this predicate witness.
func TestAncillaryFlagRefusal(t *testing.T) {
	for _, flags := range []int{0, syscall.MSG_CMSG_CLOEXEC, syscall.MSG_CTRUNC, syscall.MSG_CTRUNC | syscall.MSG_CMSG_CLOEXEC} {
		err := disposeControls(nil, flags)
		if flags&syscall.MSG_CTRUNC != 0 {
			if !errors.Is(err, ErrAncillary) {
				t.Fatalf("truncated control flag accepted: %v", err)
			}
		} else if err != nil {
			t.Fatalf("empty control refused: %v", err)
		}
	}
}

// A hard-linked lock has the correct owner, mode and named identity, so only
// the link-count guard distinguishes it from the admitted singleton control.
func TestLockHardLink(t *testing.T) {
	d := protectedFixture(t, 0711)
	old := syscall.Umask(0177)
	defer syscall.Umask(old)
	f, err := acquireLock(d, uint32(os.Geteuid()), uint32(os.Getegid()))
	must(t, err)
	must(t, f.Close())
	must(t, os.Link(filepath.Join(d.path, lockName), filepath.Join(d.path, "alias")))
	f, err = acquireLock(d, uint32(os.Geteuid()), uint32(os.Getegid()))
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, ErrOwnership) || f != nil {
		t.Fatalf("hard-linked lock accepted: %v", err)
	}
}

// The marker commits the SHA-256 of these literal wire bytes, independently of
// engine digest construction; the expected value is a fixed contract fixture.
func TestAttemptWireDigest(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle([]byte(`{"op":"demo.launch","request_id":"r","account":"demo","plan":"AAE="}`)), "receipt-seen", "")
	b := fileBytes(t, h, "attempt-"+gen)
	var a attempt
	must(t, typed(b, &a))
	if a.Digest != "a428aff9bff1b4e04d6dc12caa9856fe3e274a06fb7b583524c860baf820c409" {
		t.Fatalf("attempt wire digest changed: %s", a.Digest)
	}
}
