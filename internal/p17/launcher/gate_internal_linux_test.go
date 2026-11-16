//go:build linux && (amd64 || arm64)

package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
)

// Claim (H3 plant, "no input before a durable bound record"): a Run whose
// record is still binding, constructed here with package access because no
// production path hands one out, refuses Spawn and ReleaseInput with
// ErrNotBound and calls neither closure; the same Run refuses with ErrFenced
// once the record is bound but loaded fenced at restart. Positive control:
// TestInputGates in the external test.
func TestGatesRefuseBindingAndFenced(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"records", "inputs"} {
		if err := os.Mkdir(filepath.Join(d, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(d, &fs); err != nil {
		t.Fatal(err)
	}
	if fs.Type != 0xEF53 && fs.Type != 0x58465342 {
		t.Skipf("real storage UNRUN: fixture filesystem type %#x is neither ext4 nor XFS", fs.Type)
	}
	m := uint32(0o077)
	cfg := journal.Config{Directory: d, Records: "records", Inputs: "inputs", DeclaredUmask: &m, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 64 << 20}
	st, err := runrecord.Initialize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	uid := uint32(os.Getuid())
	rec := &runrecord.Record{Version: runrecord.Version, Kind: runrecord.Kind, Key: "plant", State: runrecord.Binding,
		LauncherUID: uid, LauncherGeneration: "g", StationRegistration: "s", InputPrincipal: "i",
		Origin: fakes.ValidOrigin(), LaunchNonce: "plant", SourceIncarnation: "inc", PreparedID: "prep",
		BindRequestID: "bind", ChildUID: uid + 1, Launcher: &incarnation.Incarnation{PID: 1, UID: uid, StartTicks: 1, BootID: "b"}}
	if err := st.Create(rec); err != nil {
		t.Fatal(err)
	}
	l, err := New(Config{LauncherUID: uid, Station: "s"}, Deps{Source: &source.Client{}, Records: st, Incarnations: incarnation.Proc{}})
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{l: l, Key: "plant", ChildUID: uid + 1}
	called := false
	if err := run.Spawn(func(uint32) (incarnation.Incarnation, error) { called = true; return incarnation.Incarnation{}, nil }); !errors.Is(err, ErrNotBound) || called {
		t.Fatalf("spawn on binding: %v called=%v", err, called)
	}
	if err := run.ReleaseInput(func() error { called = true; return nil }); !errors.Is(err, ErrNotBound) || called {
		t.Fatalf("release on binding: %v called=%v", err, called)
	}
	if _, err := st.Update("plant", func(r *runrecord.Record) error { r.State = runrecord.Bound; r.RunID = "run-plant"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := runrecord.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	l2, err := New(Config{LauncherUID: uid, Station: "s"}, Deps{Source: &source.Client{}, Records: st2, Incarnations: incarnation.Proc{}})
	if err != nil {
		t.Fatal(err)
	}
	run = &Run{l: l2, Key: "plant", ChildUID: uid + 1}
	if err := run.Spawn(func(uint32) (incarnation.Incarnation, error) { called = true; return incarnation.Incarnation{}, nil }); !errors.Is(err, ErrFenced) || called {
		t.Fatalf("spawn on fenced: %v called=%v", err, called)
	}
}
