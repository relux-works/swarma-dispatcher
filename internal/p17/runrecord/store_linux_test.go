//go:build linux && (amd64 || arm64)

package runrecord_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
)

// cfg builds a journal fixture on real storage; it skips (UNRUN) when the
// temporary directory is neither ext4 nor XFS.
func cfg(t *testing.T) journal.Config {
	t.Helper()
	d := t.TempDir()
	must(t, os.Chmod(d, 0o700))
	for _, n := range []string{"records", "inputs"} {
		must(t, os.Mkdir(filepath.Join(d, n), 0o700))
		must(t, os.Chmod(filepath.Join(d, n), 0o700))
	}
	var fs syscall.Statfs_t
	must(t, syscall.Statfs(d, &fs))
	if fs.Type != 0xEF53 && fs.Type != 0x58465342 {
		t.Skipf("real storage UNRUN: fixture filesystem type %#x is neither ext4 nor XFS", fs.Type)
	}
	m := uint32(0o077)
	return journal.Config{Directory: d, Records: "records", Inputs: "inputs", DeclaredUmask: &m, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 64 << 20}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

const (
	luid = uint32(1000)
	cuid = uint32(2001)
)

func launcherInc() *incarnation.Incarnation {
	return &incarnation.Incarnation{PID: 4242, UID: luid, StartTicks: 7, BootID: "boot-1"}
}

func base(key string) *runrecord.Record {
	return &runrecord.Record{
		Version: runrecord.Version, Kind: runrecord.Kind, Key: key, State: runrecord.Binding,
		LauncherUID: luid, LauncherGeneration: "gen-l", StationRegistration: "station-1", InputPrincipal: "input-1",
		Origin: fakes.ValidOrigin(), LaunchNonce: key, SourceIncarnation: "inc-1", PreparedID: "prep-1",
		BindRequestID: "bind-1", ChildUID: cuid, Launcher: launcherInc(),
	}
}

func open(t *testing.T) (journal.Config, *runrecord.Store) {
	t.Helper()
	c := cfg(t)
	st, err := runrecord.Initialize(c)
	must(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return c, st
}

func toBound(r *runrecord.Record) error { r.State = runrecord.Bound; r.RunID = "run-1"; return nil }
func toEnding(r *runrecord.Record) error {
	r.State = runrecord.Ending
	r.EndRequestID = "end-1"
	r.EndRequestBody = []byte(`{"id":"end-1"}`)
	return nil
}
func toEnded(r *runrecord.Record) error {
	r.State = runrecord.Ended
	r.EndAcknowledged = true
	return nil
}

// Claim (durable run record): the full lifecycle binding -> bound -> child
// -> released -> ending -> ended commits each transition, each committed
// state is materialized as the record file, and after Close and Open the
// record is restored byte-for-byte and fenced.
func TestLifecycleAndReplay(t *testing.T) {
	c, st := open(t)
	must(t, st.Create(base("run-a")))
	_, err := st.Update("run-a", toBound)
	must(t, err)
	child := incarnation.Incarnation{PID: 77, UID: cuid, StartTicks: 9, BootID: "boot-1"}
	_, err = st.Update("run-a", func(r *runrecord.Record) error { r.Child = &child; return nil })
	must(t, err)
	_, err = st.Update("run-a", func(r *runrecord.Record) error { r.InputReleased = true; return nil })
	must(t, err)
	_, err = st.Update("run-a", toEnding)
	must(t, err)
	_, err = st.Update("run-a", func(r *runrecord.Record) error { r.TeardownDone = true; return nil })
	must(t, err)
	final, err := st.Update("run-a", toEnded)
	must(t, err)
	if st.Fenced("run-a") {
		t.Fatal("live record fenced")
	}
	onDisk, err := os.ReadFile(filepath.Join(c.Directory, "records", runrecord.RecordName("run-a")))
	must(t, err)
	want, _ := json.Marshal(final)
	if !bytes.Equal(onDisk, want) {
		t.Fatalf("record file differs:\n%s\n%s", onDisk, want)
	}
	must(t, st.Close())
	st2, err := runrecord.Open(c)
	must(t, err)
	defer st2.Close()
	got, ok := st2.Get("run-a")
	if !ok || !st2.Fenced("run-a") {
		t.Fatal("record not restored or not fenced")
	}
	gotJSON, _ := json.Marshal(got)
	if !bytes.Equal(gotJSON, want) || got.State != runrecord.Ended || got.InputReleased != true || got.Child == nil || *got.Child != child {
		t.Fatalf("restored differs:\n%s\n%s", gotJSON, want)
	}
	if keys := st2.Keys(); len(keys) != 1 || keys[0] != "run-a" {
		t.Fatalf("keys %v", keys)
	}
}

// Claim (atomic transitions, negative with positive control beside each):
// every forbidden edge and every immutable-field change is refused with
// ErrTransition and leaves the stored record unchanged, while the allowed
// edge from the same state commits.
func TestForbiddenTransitions(t *testing.T) {
	_, st := open(t)
	type step struct {
		name    string
		from    []func(*runrecord.Record) error
		mutate  func(*runrecord.Record) error
		allowed func(*runrecord.Record) error
	}
	steps := []step{
		{"binding->ending", nil, toEnding, toBound},
		{"binding->ended", nil, func(r *runrecord.Record) error { r.State = runrecord.Ended; return nil }, toBound},
		{"bound->binding", []func(*runrecord.Record) error{toBound}, func(r *runrecord.Record) error { r.State = runrecord.Binding; r.RunID = ""; return nil }, toEnding},
		{"bound->ended", []func(*runrecord.Record) error{toBound}, func(r *runrecord.Record) error { toEnding(r); return toEnded(r) }, toEnding},
		{"ending->bound", []func(*runrecord.Record) error{toBound, toEnding}, func(r *runrecord.Record) error {
			r.State = runrecord.Bound
			r.EndRequestID = ""
			r.EndRequestBody = nil
			return nil
		}, toEnded},
		{"ended terminal", []func(*runrecord.Record) error{toBound, toEnding, toEnded}, func(r *runrecord.Record) error { r.TeardownDone = true; return nil }, nil},
		{"refused terminal", []func(*runrecord.Record) error{func(r *runrecord.Record) error { r.State = runrecord.Refused; return nil }}, toBound, nil},
		{"origin changed", nil, func(r *runrecord.Record) error {
			toBound(r)
			r.Origin = []byte("null")
			r.ParentRun = "run-0"
			return nil
		}, toBound},
		{"nonce changed", nil, func(r *runrecord.Record) error { toBound(r); r.LaunchNonce = "other"; r.Key = "other"; return nil }, toBound},
		{"station changed", nil, func(r *runrecord.Record) error { toBound(r); r.StationRegistration = "station-2"; return nil }, toBound},
		{"input principal changed", nil, func(r *runrecord.Record) error { toBound(r); r.InputPrincipal = "input-2"; return nil }, toBound},
		{"bind id changed", nil, func(r *runrecord.Record) error { toBound(r); r.BindRequestID = "bind-2"; return nil }, toBound},
		{"child uid changed", nil, func(r *runrecord.Record) error { toBound(r); r.ChildUID = cuid + 1; return nil }, toBound},
		{"launcher incarnation changed", nil, func(r *runrecord.Record) error { toBound(r); r.Launcher.StartTicks++; return nil }, toBound},
		{"source incarnation changed", nil, func(r *runrecord.Record) error { toBound(r); r.SourceIncarnation = "inc-2"; return nil }, toBound},
		{"prepared id changed", nil, func(r *runrecord.Record) error { toBound(r); r.PreparedID = "prep-2"; return nil }, toBound},
		{"run id changed", []func(*runrecord.Record) error{toBound}, func(r *runrecord.Record) error { r.RunID = "run-2"; return nil }, toEnding},
		{"child changed", []func(*runrecord.Record) error{toBound, func(r *runrecord.Record) error {
			r.Child = &incarnation.Incarnation{PID: 1, UID: cuid, StartTicks: 1, BootID: "b"}
			return nil
		}}, func(r *runrecord.Record) error { r.Child.PID = 2; return nil }, toEnding},
		{"release revoked", []func(*runrecord.Record) error{toBound, func(r *runrecord.Record) error {
			r.Child = &incarnation.Incarnation{PID: 1, UID: cuid, StartTicks: 1, BootID: "b"}
			r.InputReleased = true
			return nil
		}}, func(r *runrecord.Record) error { r.InputReleased = false; return nil }, toEnding},
		{"release while ending", []func(*runrecord.Record) error{toBound, func(r *runrecord.Record) error {
			r.Child = &incarnation.Incarnation{PID: 1, UID: cuid, StartTicks: 1, BootID: "b"}
			return nil
		}, toEnding}, func(r *runrecord.Record) error { r.InputReleased = true; return nil }, toEnded},
		{"end request changed", []func(*runrecord.Record) error{toBound, toEnding}, func(r *runrecord.Record) error { r.EndRequestBody = []byte(`{"id":"end-2"}`); return nil }, toEnded},
		{"ack revoked", []func(*runrecord.Record) error{toBound, toEnding, toEnded}, func(r *runrecord.Record) error { r.State = runrecord.Ending; r.EndAcknowledged = false; return nil }, nil},
	}
	for i, s := range steps {
		key := "run-" + string(rune('a'+i))
		must(t, st.Create(base(key)))
		for _, f := range s.from {
			_, err := st.Update(key, f)
			must(t, err)
		}
		before, _ := st.Get(key)
		_, err := st.Update(key, s.mutate)
		if !errors.Is(err, runrecord.ErrTransition) && !errors.Is(err, runrecord.ErrRecord) {
			t.Fatalf("%s: accepted or wrong error: %v", s.name, err)
		}
		after, _ := st.Get(key)
		b1, _ := json.Marshal(before)
		b2, _ := json.Marshal(after)
		if !bytes.Equal(b1, b2) {
			t.Fatalf("%s: stored record changed on refusal", s.name)
		}
		if s.allowed != nil {
			if _, err := st.Update(key, s.allowed); err != nil {
				t.Fatalf("%s: positive control refused: %v", s.name, err)
			}
		}
	}
}

// Claim (record shape): Validate refuses each listed malformed record and
// accepts the base record; Create refuses a non-binding record and a
// duplicate key; Update refuses an unknown key; a closed store refuses.
func TestValidateAndStoreGuards(t *testing.T) {
	_, st := open(t)
	if err := base("ok").Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*runrecord.Record){
		"kind":                    func(r *runrecord.Record) { r.Kind = "other" },
		"key differs from nonce":  func(r *runrecord.Record) { r.Key = "x" },
		"nonce charset":           func(r *runrecord.Record) { r.Key = "A/b"; r.LaunchNonce = "A/b" },
		"state":                   func(r *runrecord.Record) { r.State = "held" },
		"no generation":           func(r *runrecord.Record) { r.LauncherGeneration = "" },
		"no bind id":              func(r *runrecord.Record) { r.BindRequestID = "" },
		"no launcher incarnation": func(r *runrecord.Record) { r.Launcher = nil },
		"launcher uid mismatch":   func(r *runrecord.Record) { r.Launcher.UID = luid + 1 },
		"child uid equals L":      func(r *runrecord.Record) { r.ChildUID = luid },
		"origin not json":         func(r *runrecord.Record) { r.Origin = []byte("{") },
		"root with parent":        func(r *runrecord.Record) { r.ParentRun = "run-0" },
		"child without parent":    func(r *runrecord.Record) { r.Origin = []byte("null") },
		"root without prepare":    func(r *runrecord.Record) { r.PreparedID = "" },
		"binding with run id":     func(r *runrecord.Record) { r.RunID = "run-1" },
		"bound without run id":    func(r *runrecord.Record) { r.State = runrecord.Bound },
		"ending without end":      func(r *runrecord.Record) { r.State = runrecord.Ending; r.RunID = "run-1" },
		"ended without ack": func(r *runrecord.Record) {
			r.State = runrecord.Ended
			r.RunID = "run-1"
			r.EndRequestID = "e"
			r.EndRequestBody = []byte("x")
		},
		"released without child": func(r *runrecord.Record) { r.State = runrecord.Bound; r.RunID = "run-1"; r.InputReleased = true },
		"child uid mismatch": func(r *runrecord.Record) {
			r.State = runrecord.Bound
			r.RunID = "run-1"
			r.Child = &incarnation.Incarnation{PID: 1, UID: cuid + 1, StartTicks: 1, BootID: "b"}
		},
	}
	for name, mut := range bad {
		r := base("ok")
		mut(r)
		if err := r.Validate(); !errors.Is(err, runrecord.ErrRecord) {
			t.Fatalf("%s: accepted: %v", name, err)
		}
	}
	// Child record positive control.
	child := base("child")
	child.Origin = []byte("null")
	child.ParentRun = "run-parent"
	child.SourceIncarnation = ""
	child.PreparedID = ""
	if err := child.Validate(); err != nil {
		t.Fatalf("child record: %v", err)
	}
	bound := base("b")
	toBound(bound)
	if err := st.Create(bound); !errors.Is(err, runrecord.ErrRecord) {
		t.Fatalf("create non-binding: %v", err)
	}
	must(t, st.Create(base("dup")))
	if err := st.Create(base("dup")); !errors.Is(err, runrecord.ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := st.Update("none", toBound); !errors.Is(err, runrecord.ErrMissing) {
		t.Fatalf("missing: %v", err)
	}
	must(t, st.Close())
	if err := st.Create(base("late")); !errors.Is(err, runrecord.ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
}

type acceptAll struct{}

func (acceptAll) Consume(uint64, []byte, *journal.Recovery) error { return nil }
func (acceptAll) Finish(*journal.Recovery) error                  { return nil }

// Claim (replay, negative): a foreign or torn transition frame in the
// journal ends recovery with ErrReplay instead of being adopted; the same
// journal without it opens (positive control in TestLifecycleAndReplay).
func TestReplayRejectsForeignFrame(t *testing.T) {
	c, st := open(t)
	must(t, st.Create(base("run-a")))
	must(t, st.Close())
	raw, err := journal.OpenEstablished(c, acceptAll{})
	must(t, err)
	if _, err := raw.Append([]byte(`{"v":1,"kind":"p17-run-transition/1","record":{"v":1,"kind":"p17-run-record/1","key":"run-a","state":"ended"}}`)); err != nil {
		t.Fatal(err)
	}
	must(t, raw.Close())
	if _, err := runrecord.Open(c); !errors.Is(err, runrecord.ErrReplay) {
		t.Fatalf("foreign frame adopted: %v", err)
	}
}

// Claim (H6 table): every record state maps to its stated action; a bound
// record whose recorded launcher is alive is owned, one whose launcher
// liveness is unreadable is undetermined, and ended, refused, nil and
// invalid records are never adopted.
func TestDecide(t *testing.T) {
	bound := base("r")
	toBound(bound)
	spawned := bound.Clone()
	spawned.Child = &incarnation.Incarnation{PID: 1, UID: cuid, StartTicks: 1, BootID: "b"}
	released := spawned.Clone()
	released.InputReleased = true
	ending := released.Clone()
	toEnding(ending)
	ended := ending.Clone()
	toEnded(ended)
	refused := base("r")
	refused.State = runrecord.Refused
	invalid := base("r")
	invalid.Kind = "x"
	cases := []struct {
		name  string
		rec   *runrecord.Record
		alive bool
		err   error
		want  runrecord.RestartAction
	}{
		{"binding", base("r"), false, nil, runrecord.ActionHold},
		{"binding alive", base("r"), true, nil, runrecord.ActionHold},
		{"bound no child dead", bound, false, nil, runrecord.ActionEndAndKill},
		{"bound spawned unreleased dead", spawned, false, nil, runrecord.ActionEndAndKill},
		{"bound released dead", released, false, nil, runrecord.ActionEndThenSweep},
		{"bound released alive", released, true, nil, runrecord.ActionOwned},
		{"bound no child alive", bound, true, nil, runrecord.ActionOwned},
		{"bound undetermined", released, false, errors.New("unreadable"), runrecord.ActionUndetermined},
		{"ending", ending, false, nil, runrecord.ActionRetryEnd},
		{"ended", ended, false, nil, runrecord.ActionNeverAdopt},
		{"refused", refused, false, nil, runrecord.ActionNeverAdopt},
		{"nil", nil, false, nil, runrecord.ActionNeverAdopt},
		{"invalid", invalid, false, nil, runrecord.ActionNeverAdopt},
	}
	for _, c := range cases {
		if got := runrecord.Decide(c.rec, c.alive, c.err); got != c.want {
			t.Fatalf("%s: %s want %s", c.name, got, c.want)
		}
	}
}
