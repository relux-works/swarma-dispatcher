//go:build linux && (amd64 || arm64)

package runrecord_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
)

type frameLog struct{ frames [][]byte }

func (l *frameLog) Consume(_ uint64, p []byte, _ *journal.Recovery) error {
	l.frames = append(l.frames, append([]byte(nil), p...))
	return nil
}
func (*frameLog) Finish(*journal.Recovery) error { return nil }

// Claim (no synthetic genesis): Initialize writes no journal; the first
// Create initializes it, and journal sequence 1 is that record's real
// binding transition, with exactly one frame per transition afterwards.
func TestFirstFrameIsFirstRealTransition(t *testing.T) {
	c := cfg(t)
	st, err := runrecord.Initialize(c)
	must(t, err)
	if _, err := os.Stat(filepath.Join(c.Directory, "journal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal exists before the first real transition: %v", err)
	}
	if len(st.Keys()) != 0 {
		t.Fatal("fresh store has records")
	}
	if _, err := st.Update("run-a", toBound); !errors.Is(err, runrecord.ErrMissing) {
		t.Fatalf("update on a fresh store: %v", err)
	}
	must(t, st.Create(base("run-a")))
	_, err = st.Update("run-a", toBound)
	must(t, err)
	must(t, st.Close())
	log := &frameLog{}
	raw, err := journal.OpenEstablished(c, log)
	must(t, err)
	must(t, raw.Close())
	if len(log.frames) != 2 {
		t.Fatalf("journal has %d frames for 2 transitions", len(log.frames))
	}
	var first struct {
		Kind   string            `json:"kind"`
		Record *runrecord.Record `json:"record"`
	}
	must(t, json.Unmarshal(log.frames[0], &first))
	if first.Kind != "p17-run-transition/1" || first.Record == nil || first.Record.Key != "run-a" || first.Record.State != runrecord.Binding {
		t.Fatalf("sequence 1 is not the first real transition: %s", log.frames[0])
	}
}

// Claim (no synthetic genesis, negative): a journal whose sequence 1 is a
// synthetic store-genesis frame is refused at replay with ErrReplay; the
// positive control beside it is a journal whose sequence 1 is a real binding
// transition, which opens with that record fenced.
func TestReplayRefusesSyntheticGenesis(t *testing.T) {
	c := cfg(t)
	raw, err := journal.Initialize(c, []byte(`{"v":1,"kind":"p17-run-store/1"}`))
	must(t, err)
	must(t, raw.Close())
	if _, err := runrecord.Open(c, ownerFor(c)); !errors.Is(err, runrecord.ErrReplay) {
		t.Fatalf("synthetic genesis adopted: %v", err)
	}
	c2 := cfg(t)
	st, err := runrecord.Initialize(c2)
	must(t, err)
	must(t, st.Create(base("run-a")))
	must(t, st.Close())
	st2, err := runrecord.Open(c2, ownerFor(c2))
	must(t, err)
	defer st2.Close()
	if r, ok := st2.Get("run-a"); !ok || r.State != runrecord.Binding || !st2.Fenced("run-a") {
		t.Fatal("positive control: real first transition not restored")
	}
}

// Claim (initialization is explicit and never retried): a fresh store that
// never committed a record leaves no established state, so Open refuses it;
// a first Create whose journal initialization fails reports ErrCommit
// (durability uncertain), creates no record, and the store then refuses every
// later transition with ErrClosed instead of initializing again. Positive
// control: the same Create on a valid configuration succeeds.
func TestInitializationFailureIsSticky(t *testing.T) {
	c := cfg(t)
	st, err := runrecord.Initialize(c)
	must(t, err)
	must(t, st.Close())
	if _, err := runrecord.Open(c, ownerFor(c)); err == nil {
		t.Fatal("a store without any committed record opened as established")
	}
	bad := cfg(t)
	bad.Records = "missing-records"
	if _, err := runrecord.Initialize(bad); !errors.Is(err, runrecord.ErrConfig) {
		t.Fatalf("setup error not refused at Initialize: %v", err)
	}
	bad = cfg(t)
	st, err = runrecord.Initialize(bad)
	must(t, err)
	// A race after validation still makes the first append's failure sticky.
	must(t, os.Remove(filepath.Join(bad.Directory, bad.Records)))
	err = st.Create(base("run-a"))
	if !errors.Is(err, runrecord.ErrCommit) || !runrecord.Uncertain(err) || len(st.Keys()) != 0 {
		t.Fatalf("failed initialization: %v keys=%v", err, st.Keys())
	}
	if err := st.Create(base("run-b")); !errors.Is(err, runrecord.ErrClosed) || !runrecord.Uncertain(err) {
		t.Fatalf("initialization retried: %v", err)
	}
	good := cfg(t)
	st, err = runrecord.Initialize(good)
	must(t, err)
	defer st.Close()
	must(t, st.Create(base("run-a")))
}
