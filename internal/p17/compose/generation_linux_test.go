//go:build linux && (amd64 || arm64)

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

func privateDirectory(t *testing.T) string {
	t.Helper()
	// Reuse the owner's protected TMPDIR/RUNNER_TEMP/checkout selector rather
	// than introduce a second temporary-directory policy for this fixture.
	dir := privateDir(t, "", "generation-")
	t.Cleanup(func() { must(t, os.RemoveAll(dir)) })
	return dir
}

type rootFixture struct {
	state          string
	client         *source.Client
	launcher       *launcher.Launcher
	store          *runrecord.Store
	quarantine     *sweep.Quarantine
	source         *fakes.SourceScript
	sourceDialer   *fakes.SourceDialer
	keymaker       *fakes.KeymakerScript
	keymakerDialer *fakes.KeymakerDialer
}

func newRootFixture(t *testing.T) *rootFixture {
	t.Helper()
	previous := syscall.Umask(0077)
	t.Cleanup(func() { syscall.Umask(previous) })
	uid := uint32(os.Getuid())
	state := privateDirectory(t)
	for _, name := range []string{"records", "inputs"} {
		must(t, os.Mkdir(filepath.Join(state, name), 0700))
	}
	q := &sweep.Quarantine{}
	must(t, q.Start(state, sweep.Fresh))
	t.Cleanup(func() { must(t, q.Close()) })
	umask := uint32(0077)
	store, err := runrecord.Initialize(journal.Config{Directory: state, Records: "records", Inputs: "inputs", DeclaredUmask: &umask, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 4 << 20})
	must(t, err)
	t.Cleanup(func() { must(t, store.Close()) })
	control, closeControl, err := fakes.ControlDir(privateDirectory(t), nil)
	must(t, err)
	t.Cleanup(closeControl)
	if n := len(filepath.Join(control, "origin.sock")); n >= 100 {
		t.Fatalf("fixture socket path must be under 100 bytes, got %d", n)
	}
	proc, err := fakes.NewProcTree(privateDirectory(t), "fixture-boot")
	must(t, err)
	must(t, proc.Add(os.Getpid(), [4]uint32{uid, uid, uid, uid}, 1))
	f := &rootFixture{state: state, store: store, quarantine: q, source: &fakes.SourceScript{}, keymaker: &fakes.KeymakerScript{}}
	f.sourceDialer = &fakes.SourceDialer{Script: f.source, Peer: source.Credential{UID: uid, PID: int32(os.Getpid())}}
	f.keymakerDialer = &fakes.KeymakerDialer{Script: f.keymaker}
	generations := source.StaticGenerations{uid: "11111111111111111111111111111111"}
	client := &source.Client{Pins: source.Pins{Root: filepath.Dir(control), ControlDir: control, SourceUID: uid, SourceGeneration: "11111111111111111111111111111111"}, Dialer: f.sourceDialer, Wire: fakes.SourceWire{}, Generations: generations}
	f.client = client
	f.launcher, err = launcher.New(launcher.Config{Installation: sweep.Fresh, LauncherUID: uid, Station: "1", RequestTimeout: time.Second, ProcRoot: proc.Root, ProcMountInfo: proc.MountInfoPath()}, launcher.Deps{Placement: isolation.Fixed(30002), Source: client, Generations: generations, Keymaker: f.keymakerDialer, KeymakerWire: fakes.KeymakerWire{}, Records: store, Incarnations: proc.Incarnations(), Quarantine: q, Killer: &fakes.Killer{Tree: proc}})
	must(t, err)
	for _, report := range f.launcher.Restart(context.Background()) {
		must(t, report.Err)
	}
	return f
}

func (f *rootFixture) begin(generation string) (*launcher.Run, error) {
	return f.launcher.Begin(context.Background(), runorigin.EventRef{RoomID: "!room:example", EventID: "$retained-event"}, keymaker.Principal{UID: 30002, Generation: generation})
}

func (f *rootFixture) ledgerIdentity(t *testing.T) os.FileInfo {
	t.Helper()
	// Hold the original inode open so an atomic lease/write/return cannot
	// reuse its identity and masquerade as a read-only refusal.
	h, err := os.Open(filepath.Join(f.state, sweep.QuarantineName))
	must(t, err)
	t.Cleanup(func() { must(t, h.Close()) })
	info, err := h.Stat()
	must(t, err)
	return info
}

func rootBindAndEnd(t *testing.T, generation string) {
	t.Helper()
	newRootFixture(t).bindAndEnd(t, generation)
}

func (f *rootFixture) bindAndEnd(t *testing.T, generation string) {
	t.Helper()
	before := f.ledgerIdentity(t)
	run, err := f.begin(generation)
	must(t, err)
	after, statErr := os.Stat(filepath.Join(f.state, sweep.QuarantineName))
	must(t, statErr)
	if os.SameFile(before, after) {
		t.Fatal("valid Begin did not exercise the durable lease witness")
	}
	if run == nil || len(f.keymaker.Binds()) != 1 || len(f.store.Keys()) != 1 {
		t.Fatal("32-hex positive did not bind")
	}
	_, prepares, validates, _ := f.source.Counts()
	inputs := f.source.Inputs()
	if prepares != 1 || validates != 1 || len(inputs) != 1 || inputs[0].Generation != generation {
		t.Fatal("Source principal was not the original generation")
	}
	_, bind, err := fakes.ParseBind(f.keymaker.Binds()[0])
	must(t, err)
	if bind.InputPrincipal.Generation != generation || bind.InputPrincipal.UID != 30002 {
		t.Fatal("Keymaker principal drift")
	}
	out, err := run.End(context.Background(), 0, true, nil)
	must(t, err)
	if !out.Acknowledged {
		t.Fatal("positive end was not acknowledged")
	}
}

// Claim: valid nonzero 32-lowercase-hex tokens bind unchanged; malformed, whitespace, LF and mixed-case spellings refuse with the exact operation before any lease/contact/record, beside a live lease/bind/end witness.
func TestRunOriginGenerationContract(t *testing.T) {
	valid := []string{strings.Repeat("c", 32), strings.Repeat("1", 32), strings.Repeat("0", 31) + "1"}
	for _, generation := range valid {
		rootBindAndEnd(t, generation)
	}
	for n, row := range []struct{ name, generation string }{
		{"decimal", "1"}, {"short", strings.Repeat("c", 31)}, {"long", strings.Repeat("c", 33)},
		{"uppercase", strings.Repeat("C", 32)}, {"all-zero", strings.Repeat("0", 32)},
		{"non-hex", strings.Repeat("g", 32)}, {"empty", ""},
		{"leading-space", " " + strings.Repeat("c", 32)}, {"trailing-space", strings.Repeat("c", 32) + " "},
		{"line-feed", strings.Repeat("c", 32) + "\n"}, {"mixed-case", strings.Repeat("c", 16) + "C" + strings.Repeat("c", 15)},
	} {
		t.Run(row.name, func(t *testing.T) {
			rootBindAndEnd(t, valid[n%len(valid)])
			f := newRootFixture(t)
			before := f.ledgerIdentity(t)
			run, err := f.begin(row.generation)
			var refusal *codes.Refusal
			if run != nil || !errors.As(err, &refusal) || refusal.Code != codes.OriginRefused || refusal.Op != "Source input generation" {
				t.Fatalf("wrong generation refusal: run=%v err=%v", run != nil, err)
			}
			verdict, verdictErr := codes.Exit(err, 0, false)
			must(t, verdictErr)
			if verdict.Status != 16 {
				t.Fatalf("generation refusal exit=%d", verdict.Status)
			}
			after, statErr := os.Stat(filepath.Join(f.state, sweep.QuarantineName))
			must(t, statErr)
			if !os.SameFile(before, after) {
				t.Fatal("invalid generation took a durable lease")
			}
			dials, _ := f.sourceDialer.Dials()
			if dials != 0 || f.keymakerDialer.Dials() != 0 || len(f.store.Keys()) != 0 || f.quarantine.Held(30002) {
				t.Fatal("invalid generation contacted services, wrote records or acquired a lease")
			}
		})
	}
}

// Claim: the former opposite-domain decimal control is now a negative for both M0 and root Begin, beside the same unchanged valid 32-hex principal in both layers.
func TestDecimalGenerationRefusedByBoth(t *testing.T) {
	b, err := os.ReadFile("../../../fixtures/m0-launch/policy.valid.json")
	must(t, err)
	var policy map[string]any
	must(t, json.Unmarshal(b, &policy))
	for _, generation := range []string{"cccccccccccccccccccccccccccccccc", "1"} {
		policy["worker"].(map[string]any)["generation"] = generation
		body, err := json.Marshal(policy)
		must(t, err)
		parsed, err := m0.DecodePolicy(body)
		if generation != "1" {
			must(t, err)
			if parsed.Worker.Generation != generation {
				t.Fatal("M0 changed generation")
			}
			rootBindAndEnd(t, generation)
			continue
		}
		var refusal *m0.Refusal
		if !errors.As(err, &refusal) || refusal.Code != m0.CodeRequestInvalid || refusal.Detail != "member spelling: generation" {
			t.Fatalf("M0 accepted decimal generation: %v", err)
		}
		f := newRootFixture(t)
		run, err := f.begin(generation)
		if run != nil || !codes.Is(err, codes.OriginRefused) {
			t.Fatalf("root accepted decimal generation: %v", err)
		}
	}
}
