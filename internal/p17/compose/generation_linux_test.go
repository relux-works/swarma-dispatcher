//go:build linux && (amd64 || arm64)

package compose_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func privateDirectory(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "generation-")
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, os.RemoveAll(dir)) })
	requireNoError(t, os.Chmod(dir, 0700))
	return dir
}

type rootFixture struct {
	launcher       *launcher.Launcher
	store          *runrecord.Store
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
		requireNoError(t, os.Mkdir(filepath.Join(state, name), 0700))
	}
	q := &sweep.Quarantine{}
	requireNoError(t, q.Start(state, sweep.Fresh))
	t.Cleanup(func() { requireNoError(t, q.Close()) })
	umask := uint32(0077)
	store, err := runrecord.Initialize(journal.Config{Directory: state, Records: "records", Inputs: "inputs", DeclaredUmask: &umask, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 4 << 20})
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, store.Close()) })
	control, closeControl, err := fakes.ControlDir(privateDirectory(t), nil)
	requireNoError(t, err)
	t.Cleanup(closeControl)
	proc, err := fakes.NewProcTree(privateDirectory(t), "fixture-boot")
	requireNoError(t, err)
	requireNoError(t, proc.Add(os.Getpid(), [4]uint32{uid, uid, uid, uid}, 1))
	f := &rootFixture{store: store, source: &fakes.SourceScript{}, keymaker: &fakes.KeymakerScript{}}
	f.sourceDialer = &fakes.SourceDialer{Script: f.source, Peer: source.Credential{UID: uid, PID: int32(os.Getpid())}}
	f.keymakerDialer = &fakes.KeymakerDialer{Script: f.keymaker}
	generations := source.StaticGenerations{uid: "11111111111111111111111111111111"}
	client := &source.Client{Pins: source.Pins{Root: filepath.Dir(control), ControlDir: control, SourceUID: uid, SourceGeneration: "11111111111111111111111111111111"}, Dialer: f.sourceDialer, Wire: fakes.SourceWire{}, Generations: generations}
	f.launcher, err = launcher.New(launcher.Config{Installation: sweep.Fresh, LauncherUID: uid, Station: "1", RequestTimeout: time.Second, ProcRoot: proc.Root, ProcMountInfo: proc.MountInfoPath()}, launcher.Deps{Placement: isolation.Fixed(30002), Source: client, Generations: generations, Keymaker: f.keymakerDialer, KeymakerWire: fakes.KeymakerWire{}, Records: store, Incarnations: proc.Incarnations(), Quarantine: q, Killer: &fakes.Killer{Tree: proc}})
	requireNoError(t, err)
	for _, report := range f.launcher.Restart(context.Background()) {
		if report.Err != nil {
			t.Fatal(report.Err)
		}
	}
	return f
}

func (f *rootFixture) begin(generation string) (*launcher.Run, error) {
	return f.launcher.Begin(context.Background(), runorigin.EventRef{RoomID: "!room:example", EventID: "$retained-event"}, keymaker.Principal{UID: 30002, Generation: generation})
}

// Claim: a canonical numeric generation binds through the same root clients, while valid M0 generation spellings refuse exactly at Source input generation before either service is contacted.
func TestRootGenerationDomain(t *testing.T) {
	for _, generation := range []string{
		"cccccccccccccccccccccccccccccccc",
		"11111111111111111111111111111111",
		"00000000000000000000000000000001",
	} {
		t.Run(generation, func(t *testing.T) {
			control := newRootFixture(t)
			run, err := control.begin("1")
			requireNoError(t, err)
			if run == nil || len(control.keymaker.Binds()) != 1 || len(control.store.Keys()) != 1 {
				t.Fatal("numeric positive control did not bind")
			}
			_, prepares, validates, _ := control.source.Counts()
			if prepares != 1 || validates != 1 {
				t.Fatal("numeric positive control did not cross Source")
			}
			_, err = run.End(context.Background(), 0, true, nil)
			requireNoError(t, err)

			negative := newRootFixture(t)
			run, err = negative.begin(generation)
			var refusal *codes.Refusal
			if run != nil || !errors.As(err, &refusal) || refusal.Code != "run_origin_refused" || refusal.Op != "Source input generation" {
				t.Fatalf("expected run_origin_refused at Source input generation, got run=%v err=%v", run != nil, err)
			}
			verdict, verdictErr := codes.Exit(err, 0, false)
			requireNoError(t, verdictErr)
			if verdict.Status != 16 {
				t.Fatalf("generation refusal exit=%d, want 16", verdict.Status)
			}
			sourceDials, _ := negative.sourceDialer.Dials()
			if sourceDials != 0 || negative.keymakerDialer.Dials() != 0 || len(negative.store.Keys()) != 0 {
				t.Fatal("generation refusal contacted a service or created a record")
			}
		})
	}
}

// Claim: M0 accepts the 32-character generation spellings refused by root Begin, but rejects the numeric spelling that root Begin accepts.
func TestM0GenerationDomain(t *testing.T) {
	b, err := os.ReadFile("../../../fixtures/m0-launch/policy.valid.json")
	requireNoError(t, err)
	for _, generation := range []string{"cccccccccccccccccccccccccccccccc", "11111111111111111111111111111111", "00000000000000000000000000000001"} {
		var policy map[string]any
		requireNoError(t, json.Unmarshal(b, &policy))
		policy["worker"].(map[string]any)["generation"] = generation
		valid, err := json.Marshal(policy)
		requireNoError(t, err)
		parsed, err := m0.DecodePolicy(valid)
		requireNoError(t, err)
		if parsed.Worker.UID != 30002 || parsed.Worker.Generation != generation {
			t.Fatal("M0 valid generation changed")
		}
		policy["worker"].(map[string]any)["generation"] = "1"
		invalid, err := json.Marshal(policy)
		requireNoError(t, err)
		_, err = m0.DecodePolicy(invalid)
		var refusal *m0.Refusal
		if !errors.As(err, &refusal) || refusal.Code != "request_invalid" || refusal.Detail != "member spelling: generation" {
			t.Fatalf("expected request_invalid for numeric M0 generation, got %v", err)
		}
	}
}
