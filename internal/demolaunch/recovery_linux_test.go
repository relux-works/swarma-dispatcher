//go:build linux

package demolaunch

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Identifier inspection covers all package declarations, including fixtures.
// It prevents direct fixed-runner references outside the three inspected sites;
// it is a source guard, not a sandbox against arbitrary hostile code.
func TestFixtureRunnerIsolation(t *testing.T) {
	files, err := filepath.Glob("*.go")
	must(t, err)
	for _, name := range files {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		must(t, err)
		for _, decl := range f.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			allowed := fn != nil && ((name == "runner_linux.go" && fn.Name.Name == "fixedRunner") || (name == "engine_linux.go" && fn.Name.Name == "newEngine") || (name == "safe_engine_linux_test.go" && fn.Name.Name == "TestProductionDefaults"))
			ast.Inspect(decl, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && id.Name == "fixedRunner" && !allowed {
					t.Errorf("forbidden runner reference in %s", name)
				}
				if ok && id.Name == "newEngine" && strings.HasSuffix(name, "_test.go") && !allowed {
					t.Errorf("production constructor in %s", name)
				}
				return true
			})
		}
	}
}

func rebuild(t *testing.T, h *harness) *engine {
	t.Helper()
	e, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"], h.e.accounts["second"]}, serverConfig{})
	if err != nil {
		t.Fatalf("restart state rejected: %v", err)
	}
	e.run = h.e.run
	return e
}
func directoryBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	entries, err := os.ReadDir(root)
	must(t, err)
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(root, entry.Name()))
		must(t, err)
		result[entry.Name()] = string(b)
	}
	return result
}

// A terminal replay, status and conflicting retry leave every state byte and
// filename unchanged; this is a finite fixture witness, not a filesystem proof.
func TestTerminalStateBytes(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
	assertState(t, h.e.handle(stopBytes(10)), "absent", "")
	before := directoryBytes(t, filepath.Join(h.root, "state"))
	assertState(t, h.e.handle(stopBytes(10)), "absent", "")
	assertState(t, h.e.handle(stopBytes(11)), "refused", "stop_conflict")
	assertState(t, h.e.status("demo"), "facts", "")
	assertState(t, h.e.handle(launchBytes("demo")), "refused", "attempt_exists")
	if !reflect.DeepEqual(before, directoryBytes(t, filepath.Join(h.root, "state"))) || h.calls.Load() != 2 {
		t.Fatal("terminal operation mutated state or called child")
	}
}

// Cancellation before admission has no effects. Cancellation during a real
// receipt read is shutdown, even when the child has exited or a receipt exists.
func TestShutdownLaunchClassification(t *testing.T) {
	for _, variant := range []string{"valid", "before", "receipt", "missing"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "hold-launch")
			if variant == "before" {
				h.cancel()
			}
			if variant == "receipt" || variant == "missing" {
				real := h.e.readReceipt
				h.e.readReceipt = func(a account, id string) ([]byte, error) {
					b, err := real(a, id)
					if err == nil {
						h.cancel()
						if variant == "missing" {
							return nil, os.ErrNotExist
						}
					}
					return b, err
				}
			}
			r := h.e.handle(launchBytes("demo"))
			switch variant {
			case "valid":
				assertState(t, r, "receipt-seen", "")
				absentFile(t, h, "closed")
			case "before":
				assertState(t, r, "refused", "shutdown")
				if h.calls.Load() != 0 || r.Closed {
					t.Fatalf("shutdown classification assertion: %+v", r)
				}
				absentFile(t, h, "attempt-"+gen)
				absentFile(t, h, "closed")
			default:
				assertState(t, r, "unknown", "shutdown")
				if !strings.Contains(string(fileBytes(t, h, "closed")), `"reason":"shutdown"`) {
					t.Fatal("wrong durable reason")
				}
			}
		})
	}
}

// The launch ID cannot name the same generation's stop. A distinct ID still
// reaches the helper; refusal adds no intent or other state bytes.
func TestLaunchStopIDCollision(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
	before := directoryBytes(t, filepath.Join(h.root, "state"))
	bad := bytes.Replace(stopBytes(10), []byte("stop-1"), []byte("r"), 1)
	assertState(t, h.e.handle(bad), "refused", "request_id_conflict")
	if h.calls.Load() != 1 || !reflect.DeepEqual(before, directoryBytes(t, filepath.Join(h.root, "state"))) {
		t.Fatal("collision had effects")
	}
	assertState(t, h.e.handle(stopBytes(10)), "absent", "")
}

// Real protected files with zero/invalid-JSON payloads preserve startup and
// spend the generation. Parseable corruption is covered separately below.
func TestTornRecovery(t *testing.T) {
	for _, suffix := range []string{"stop-", "stopped-", "attempt-", ".truncated", ".lost"} {
		for _, payload := range []string{"", "{"} {
			t.Run(suffix+"/"+map[bool]string{true: "empty", false: "partial"}[payload == ""], func(t *testing.T) {
				h := fixture(t, "good")
				if suffix != "attempt-" {
					assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
					waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
				}
				name := suffix + gen
				if strings.HasPrefix(suffix, ".") {
					name = "output-" + gen + "-stdout" + suffix
				}
				must(t, h.e.store.create(name, []byte(payload)))
				e := rebuild(t, h)
				if !e.closed || e.stopped["demo"] || !reflect.DeepEqual(e.status("demo").Torn, []string{name}) {
					t.Fatalf("torn recovery status assertion: %+v", e.status("demo"))
				}
				assertState(t, e.handle(launchBytes("demo")), "refused", "attempt_exists")
				if suffix == "attempt-" {
					assertState(t, e.handle(stopBytes(10)), "refused", "attempt_identity_unknown")
				} else {
					r := e.handle(stopBytes(10))
					if suffix == "stopped-" {
						assertState(t, r, "unresolved", "stopped_uncertain")
					} else {
						assertState(t, r, "absent", "")
					}
					if suffix == "stop-" && r.Timing != "unknown" {
						t.Fatalf("torn recovery assertion: %+v", r)
					}
				}
				if string(fileBytes(t, h, name)) != payload {
					t.Fatal("torn name repaired")
				}
			})
		}
	}
	// Intact evidence loss is an explicit departure: it does not itself latch.
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	assertState(t, h.e.handle(stopBytes(10)), "absent", "")
	must(t, h.e.store.create("output-"+gen+"-stdout.lost", []byte(`"output_lost"`)))
	if rebuild(t, h).closed {
		t.Fatal("intact loss closed admission")
	}
}

// An exclusive torn intent cannot block the helper. Same-process retries retain
// arguments with unknown timing; a restart accepts a valid guessed retry.
func TestIntentDurabilityDoesNotGateStop(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	must(t, h.e.store.create("stop-"+gen, nil))
	h.mode = "remaining"
	assertState(t, h.e.handle(stopBytes(10)), "unresolved", "processes_remain")
	deadline := h.e.stops["demo"].deadline
	assertState(t, h.e.handle(stopBytes(10)), "unresolved", "processes_remain")
	assertState(t, h.e.handle(stopBytes(11)), "refused", "stop_conflict")
	if h.calls.Load() != 3 || !h.e.stops["demo"].deadline.Equal(deadline) || !h.e.closed {
		t.Fatal("intent retry changed")
	}
	e := rebuild(t, h)
	h.mode = "good"
	changed := bytes.Replace(stopBytes(11), []byte("stop-1"), []byte("stop-2"), 1)
	r := e.handle(changed)
	assertState(t, r, "absent", "")
	if r.Timing != "unknown" || !r.Closed || h.calls.Load() != 4 || len(fileBytes(t, h, "stop-"+gen)) != 0 {
		t.Fatal(r)
	}
}

// The singleton guards are in-process only. A rebuilt engine can issue the
// same effect while the old helper is alive; its timing is unknown and closed.
func TestRestartHelperOverlap(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	h.mode = "hold-stop"
	h.e.stopWait = 5 * time.Second
	assertState(t, h.e.handle(stopBytes(13)), "unresolved", "stop_timeout")
	old := h.e.stops["demo"].child
	select {
	case <-old.done:
		t.Fatal("first helper exited")
	default:
	}
	original, err := os.ReadFile(filepath.Join(h.root, "call-2.json"))
	must(t, err)
	e := rebuild(t, h)
	e.stopWait = 5 * time.Second
	r := e.handle(stopBytes(13))
	assertState(t, r, "unresolved", "stop_timeout")
	if r.Timing != "unknown" || !r.Closed || h.calls.Load() != 3 {
		t.Fatal(r, h.calls.Load())
	}
	again, err := os.ReadFile(filepath.Join(h.root, "call-3.json"))
	must(t, err)
	if !bytes.Equal(original, again) {
		t.Fatal("restart changed arguments")
	}
	must(t, os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600))
	waitChild(t, e.stops["demo"].child, time.Now().Add(30*time.Second))
}

// Blocking the command constructor exposes the active guard before a child
// handle exists. No repeated request may invoke a second constructor.
func TestStopActiveBeforeChild(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	real := h.e.run
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.e.run = func(p string) *exec.Cmd { once.Do(func() { close(entered) }); <-release; return real(p) }
	done := make(chan response, 1)
	go func() { done <- h.e.handle(stopBytes(10)) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("helper constructor absent")
	}
	for range 32 {
		retry := make(chan response, 1)
		go func() { retry <- h.e.handle(stopBytes(10)) }()
		select {
		case r := <-retry:
			assertState(t, r, "refused", "stop_in_flight")
		case <-time.After(2 * time.Second):
			close(release)
			<-retry
			<-done
			t.Fatal("active helper guard admitted overlapping constructor")
		}
	}
	close(release)
	select {
	case r := <-done:
		assertState(t, r, "absent", "")
	case <-time.After(30 * time.Second):
		t.Fatal("helper blocked")
	}
	if h.calls.Load() != 2 {
		t.Fatal(h.calls.Load())
	}
}

// A child that never reads cannot hold stdin beyond five seconds. The fixture
// releases it later; dispatcher shutdown does not signal it.
func TestChildStdinDeadline(t *testing.T) {
	// Literal deadline arithmetic catches a six-second budget without reducing
	// the real-child scheduling margin under race instrumentation.
	now := time.Now()
	for _, seconds := range []int{2, 40} {
		want := 5 * time.Second
		if seconds == 2 {
			want = 2 * time.Second
		}
		if got := childIntakeDeadline(now, now.Add(time.Duration(seconds)*time.Second)); got.Sub(now) != want {
			t.Fatalf("stdin elapsed deadline changed: %s", got.Sub(now))
		}
	}
	h := fixture(t, "never-read")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := time.AfterFunc(10*time.Second, func() { os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600) })
	defer release.Stop()
	started := time.Now()
	ch, err := startChild(ctx, h.e.run, managerPath, bytes.Repeat([]byte{'x'}, 1<<20), time.Now().Add(40*time.Second), &capture{limit: 16384}, &capture{limit: 16384})
	elapsed := time.Since(started)
	// A scheduled release bounds mutants that remove the intake deadline too.
	if !errors.Is(err, os.ErrDeadlineExceeded) || elapsed < 4*time.Second || elapsed > 8*time.Second {
		t.Errorf("stdin elapsed %s: %v", elapsed, err)
	}
	must(t, os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600))
	if ch != nil {
		waitChild(t, ch, time.Now().Add(30*time.Second))
	}
}

// Intercept the actual deadline-setting call and forward the identical value to
// os.File.SetWriteDeadline. Fixed-clock cases pin the leg and operation cap;
// the nil-clock case brackets the applied value using real observation instants
// so a production-only offset is not masked. This observes the API argument,
// not kernel timing; TestChildStdinDeadline exercises the real blocked pipe.
func TestChildStdinDeadlineCallsite(t *testing.T) {
	for _, seconds := range []int{2, 40, 0} {
		h := fixture(t, "good")
		now := time.Now()
		var observed time.Time
		var appliedAt time.Time
		out := &capture{limit: 16384, stdinNow: func() time.Time { return now }, stdinDeadline: func(f *os.File, d time.Time) error {
			observed, appliedAt = d, time.Now()
			return f.SetWriteDeadline(d)
		}}
		limit := seconds
		if seconds == 0 {
			out.stdinNow = nil
			limit = 40
		}
		ch, err := startChild(h.e.ctx, h.e.run, managerPath, []byte(`{}`), now.Add(time.Duration(limit)*time.Second), out, &capture{limit: 16384})
		if ch != nil {
			waitChild(t, ch, time.Now().Add(30*time.Second))
		}
		must(t, err)
		if seconds == 0 {
			if observed.Before(now.Add(5*time.Second)) || observed.After(appliedAt.Add(5*time.Second)) {
				t.Fatalf("child stdin production deadline changed: applied=%s start=%s observed-at=%s", observed, now, appliedAt)
			}
			continue
		}
		want := 5 * time.Second
		if seconds == 2 {
			want = 2 * time.Second
		}
		if observed.Sub(now) != want {
			t.Fatalf("child stdin callsite deadline changed: got %s want %s", observed.Sub(now), want)
		}
	}
}

// Each writable ancestor bit is refused on initial pinning, with no recheck to
// mask a missing predicate. The same path with mode 0700 is accepted.
func TestAncestorInitialBits(t *testing.T) {
	root := fixtureDir(t)
	parent := filepath.Join(root, "parent")
	leaf := filepath.Join(parent, "leaf")
	must(t, os.Mkdir(parent, 0700))
	must(t, os.Mkdir(leaf, 0700))
	for _, mode := range []os.FileMode{0700, 0720, 0702} {
		must(t, os.Chmod(parent, mode))
		d, err := pinDirectory(leaf, uint32(os.Geteuid()))
		if mode == 0700 {
			must(t, err)
			d.close()
		} else {
			if d != nil {
				d.close()
			}
			if !errors.Is(err, ErrIdentity) {
				t.Fatalf("ancestor writable mode accepted: %v %v", mode, err)
			}
		}
	}
	must(t, os.Chmod(parent, 0700))
}

// A closed terminal record restores the in-memory and durable latch even when
// an external actor removed the closed file; software itself never removes it.
func TestStoppedRestoresMissingLatch(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	h.mode = "remaining"
	assertState(t, h.e.handle(stopBytes(10)), "unresolved", "processes_remain")
	h.mode = "good"
	assertState(t, h.e.handle(stopBytes(10)), "absent", "")
	must(t, os.Remove(filepath.Join(h.root, "state", "closed")))
	e := rebuild(t, h)
	if !e.closed {
		t.Fatal("closed fact lost")
	}
	fileBytes(t, h, "closed")
}

// The real ENOSPC lane is opt-in and destructive only inside the supplied empty
// disposable filesystem. Normal suites retain the planted-torn witnesses above.
func TestDiskFullStopRecovery(t *testing.T) {
	root := os.Getenv("DEMOLAUNCH_ENOSPC_DIR")
	if root == "" {
		t.Skip("DEMOLAUNCH_ENOSPC_DIR must name an empty writable small disposable filesystem")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 || syscall.Access(root, 2) != nil {
		t.Skip("DEMOLAUNCH_ENOSPC_DIR is not an empty readable writable directory")
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(root, &fs) != nil || fs.Blocks*uint64(fs.Bsize) > 16<<20 {
		t.Skip("DEMOLAUNCH_ENOSPC_DIR must be on a filesystem no larger than 16 MiB")
	}
	h := fixture(t, "after-output")
	state := filepath.Join(root, "state")
	must(t, os.Mkdir(state, 0700))
	t.Cleanup(func() { os.RemoveAll(state) })
	store, err := openState(state, uint32(os.Geteuid()))
	must(t, err)
	defer store.dir.Close()
	h.e.store = store
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")

	filler := filepath.Join(root, "fill")
	f, err := os.OpenFile(filler, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(t, err)
	defer os.Remove(filler)
	block := make([]byte, 4096)
	for {
		_, err = f.Write(block)
		if err != nil {
			break
		}
	}
	f.Close()
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatal("expected real ENOSPC", err)
	}
	must(t, os.WriteFile(filepath.Join(h.root, "release-launch"), nil, 0600))
	waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
	if len(h.e.status("demo").OutputLoss) == 0 {
		t.Fatal("disk-full post-receipt loss missing")
	}
	h.mode = "good"
	for range 2 {
		r := h.e.handle(stopBytes(10))
		if r.State != "unresolved" || (r.Code != "stopped_uncertain" && r.Code != "latch_uncertain") || !r.Closed {
			t.Fatal(r)
		}
	}
	if h.calls.Load() != 3 {
		t.Fatal("ENOSPC blocked helper", h.calls.Load())
	}
	must(t, os.Remove(filler))
	r := h.e.handle(stopBytes(10))
	if r.State != "unresolved" || r.Code != "stopped_uncertain" {
		t.Fatal("torn completion must not be overwritten", r)
	}
	e := rebuild(t, h)
	r = e.handle(stopBytes(10))
	assertState(t, r, "unresolved", "stopped_uncertain")
	if r.Timing != "unknown" || !r.Closed || h.calls.Load() != 5 {
		t.Fatal(r)
	}
}

// A timed-out helper with already closed pipes isolates the child guard from
// the active and draining guards; a retry must not start another helper.
func TestStopLiveChildClosedPipes(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	h.mode = "closed-pipes"
	h.e.stopWait = 5 * time.Second
	assertState(t, h.e.handle(stopBytes(10)), "unresolved", "stop_timeout")
	ch := h.e.stops["demo"].child
	select {
	case <-ch.outputDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pipes did not close")
	}
	select {
	case <-ch.done:
		t.Fatal("helper not live")
	default:
	}
	assertState(t, h.e.handle(stopBytes(10)), "refused", "stop_in_flight")
	if h.calls.Load() != 2 {
		t.Fatal("second helper")
	}
}

// A different stop ID is refused before any second call; a same-ID continuation
// is the positive control and emits the original literal ID and arguments.
func TestStopRetryRequestID(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	h.mode = "remaining"
	assertState(t, h.e.handle(stopBytes(10)), "unresolved", "processes_remain")
	before := directoryBytes(t, filepath.Join(h.root, "state"))
	changed := bytes.Replace(stopBytes(10), []byte("stop-1"), []byte("stop-2"), 1)
	assertState(t, h.e.handle(changed), "refused", "stop_conflict")
	if h.calls.Load() != 2 || !reflect.DeepEqual(before, directoryBytes(t, filepath.Join(h.root, "state"))) {
		t.Fatal("ID conflict changed state")
	}
	assertState(t, h.e.handle(stopBytes(10)), "unresolved", "processes_remain")
	if h.calls.Load() != 3 {
		t.Fatal("valid retry refused")
	}
}

// Real elapsed waits defend both the default call budget and use of the original
// deadline. Fixture release timers bound mutants without retrying assertions.
func TestStopElapsedBudgets(t *testing.T) {
	for _, variant := range []string{"default", "retry"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
			low, high := 44*time.Second, 49*time.Second
			if variant == "retry" {
				h.e.stopWait = 8 * time.Second
				h.mode = "remaining"
				assertState(t, h.e.handle(stopBytes(10)), "unresolved", "processes_remain")
				deadline := h.e.stops["demo"].deadline
				time.Sleep(time.Until(deadline.Add(-3 * time.Second)))
				low, high = 2*time.Second, 5*time.Second
			}
			h.mode = "hold-stop"
			release := time.AfterFunc(high+2*time.Second, func() { os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600) })
			defer release.Stop()
			start := time.Now()
			r := h.e.handle(stopBytes(10))
			elapsed := time.Since(start)
			if r.State != "unresolved" || r.Code != "stop_timeout" || r.Timing != "missed" || elapsed < low || elapsed > high {
				t.Errorf("%s elapsed=%s reply=%+v", variant, elapsed, r)
			}
			must(t, os.WriteFile(filepath.Join(h.root, "release-stop"), nil, 0600))
		})
	}
}

// This test changes process-wide descriptor flags, without parallel tests. It
// plants a real inheritable pipe: A refuses and names it; B opens all fixture
// resources after sealing and succeeds. Other tests require no inheritable fd.
// Runtime-created CLOEXEC descriptors are excluded from the child observation.
func TestComposedChildInheritance(t *testing.T) {
	t.Setenv("DEMO_PARENT_SECRET", "must-not-reach-child")
	r, w, err := os.Pipe()
	must(t, err)
	defer r.Close()
	defer w.Close()
	// Keep the sentinel away from fd 3, the ExtraFiles plant destination.
	fd := w.Fd()
	if fd <= 3 {
		t.Fatal("fixture requires stdio open and pipe writer above fd 3")
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFD, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFD, flags&^syscall.FD_CLOEXEC)
	if errno != 0 {
		t.Fatal(errno)
	}
	target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(fd)))
	must(t, err)
	offender := strconv.Itoa(int(fd)) + "=" + target
	evidence := func(h *harness, call string) string {
		b, err := os.ReadFile(filepath.Join(h.root, "inherited-"+call+".txt"))
		if err != nil {
			return "observation unavailable: " + err.Error()
		}
		return string(b)
	}
	a := fixture(t, "observe")
	reply := a.e.handle(launchBytes("demo"))
	got := evidence(a, "1")
	if reply.State != "unknown" || reply.Code != "launcher_exit" || !strings.Contains("\n"+got+"\n", "\n"+offender+"\n") {
		t.Fatalf("unsealed reply=%+v want offender=%s inherited:\n%s", reply, offender, got)
	}
	must(t, sealInheritedDescriptors())
	// Do not move any B opens above the seal: CLOEXEC plants must leak newly
	// opened state/log files, listener, accepted socket or explicit ExtraFiles.
	h := fixture(t, "observe")
	ep, _, _ := serveFixture(t, h, "valid")
	f, err := connectSocket(ep.dir, time.Now())
	must(t, err)
	defer wakeClose(f)
	wire, err := clientExchange(f, time.Now(), launchBytes("demo"))
	if err != nil || !strings.Contains(wire, `"state":"receipt-seen"`) {
		t.Fatalf("sealed reply=%s err=%v inherited:\n%s", wire, err, evidence(h, "1"))
	}
	h.e.mu.Lock()
	ch := h.e.launchChildren["demo"]
	h.e.mu.Unlock()
	waitChild(t, ch, time.Now().Add(30*time.Second))
	if ch.err != nil {
		t.Fatalf("child=%v inherited:\n%s", ch.err, evidence(h, "1"))
	}
	stopped := h.e.handle(stopBytes(10))
	if stopped.State != "absent" || stopped.Code != "" {
		t.Fatalf("stop=%+v inherited:\n%s", stopped, evidence(h, "2"))
	}
}

// The default ten-second receipt budget is measured on a real child with no
// receipt. The fake stays alive until explicitly released by the fixture.
func TestReceiptElapsedBudget(t *testing.T) {
	h := fixture(t, "timeout")
	start := time.Now()
	r := h.e.handle(launchBytes("demo"))
	elapsed := time.Since(start)
	assertState(t, r, "unknown", "receipt_timeout")
	if elapsed < 9*time.Second || elapsed > 13*time.Second {
		t.Fatal("receipt budget", elapsed)
	}
}
