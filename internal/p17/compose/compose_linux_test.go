//go:build linux && (amd64 || arm64)

package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/m0hook"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

// Claim: each composed service client's witness observes one client dial after socket-path/pin checks, independently of the zero-contact generation control.
func TestComposedClientWitnesses(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	d := launcherDeps(f.cfg.P17, 30002, f.deps, s.store, s.quarantine)
	// This is a client contact witness with a valid numeric principal, not a
	// conversion of the worker tuple or a composed successful dispatch.
	prepared, err := d.Source.PrepareFor(context.Background(), "fixture-contact", runorigin.EventRef{RoomID: "!room:example", EventID: "$retained-event"}, runorigin.Principal{UID: 30002, Generation: 1})
	must(t, err)
	must(t, prepared.Close())
	if n, paths := f.source.Dials(); n != 1 || !reflect.DeepEqual(paths, []string{filepath.Join(f.cfg.P17.SourceControlDir, "origin.sock")}) {
		t.Fatalf("Source witness did not observe its composed client: %d %v", n, paths)
	}
	conn, err := d.Keymaker.Dial(context.Background())
	must(t, err)
	must(t, conn.Close())
	select {
	case <-f.keymakerAccepted:
	case <-time.After(2 * time.Second):
		t.Fatal("Keymaker witness did not observe its composed client")
	}
	if f.keymakerDials.Load() != 1 {
		t.Fatal("Keymaker witness must count exactly one contact")
	}
}

// Claim: the real composition passes the earlier M0 authentication, installation, time, journal and restart gates, forwards the unchanged worker tuple, then refuses exactly at Source input generation with zero service dials, records and inner starts.
func TestComposedGenerationBoundary(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	body, err := s.Serve(30001, dispatchID, f.frame)
	wireRefusal(t, body, err, "m0_start_invalidated")
	attempt, ok := s.hook.Last(f.events.request.EffectID)
	var refusal *codes.Refusal
	if !ok || f.events.calls != 1 || !errors.As(attempt.Err, &refusal) || refusal.Code != "run_origin_refused" || refusal.Op != "Source input generation" {
		t.Fatalf("expected exact generation boundary, got %v", attempt.Err)
	}
	if f.events.request.Worker.UID != 30002 || f.events.request.Worker.Generation != "cccccccccccccccccccccccccccccccc" || f.stops.verifications != 1 || f.stops.requests != 0 {
		t.Fatal("an earlier gate, unchanged identity, or readiness-only probe was bypassed")
	}
	f.assertNoContacts(s)
	if !reflect.DeepEqual(f.gate.classes, []string{"start", "start"}) {
		t.Fatalf("startup/dispatch classes: %v", f.gate.classes)
	}
	body, err = s.Serve(30001, dispatchID, f.frame)
	wireRefusal(t, body, err, "m0_start_invalidated")
	if f.events.calls != 1 {
		t.Fatal("ordinary generation refusal repeated the launcher")
	}
	f.assertNoContacts(s)
	if !reflect.DeepEqual(f.gate.classes, []string{"start", "start", "start"}) {
		t.Fatal("already-recorded replay skipped Verify(start)")
	}
	if !bytes.Equal(s.ConfigBytes(), f.config) {
		t.Fatal("attached snapshot drift")
	}
	copy := s.ConfigBytes()
	copy[0] = 'x'
	if !bytes.Equal(s.ConfigBytes(), f.config) {
		t.Fatal("ConfigBytes aliases the parsed snapshot")
	}
	var nilExtension *m0.Extension
	if nilExtension.ConfigBytes() != nil {
		t.Fatal("nil ConfigBytes must be nil")
	}
}

// Claim: default composition and direct M0 attach remain capacity-only with no P17 files; every default method refuses by name beside its pinned fixture control.
func TestDefaultsStillClosed(t *testing.T) {
	f := newFixture(t)
	before, err := os.ReadDir(f.state)
	must(t, err)
	s, err := Attach(context.Background(), "/configuration/m0.json", m0.DefaultSeams(), DefaultDeps(), f.read)
	if s != nil || m0.CodeOf(err) != "m0_closed" {
		t.Fatalf("default composition opened: %v", err)
	}
	e, err := m0.Attach("/configuration/m0.json", m0.DefaultSeams(), f.read)
	if e != nil || m0.CodeOf(err) != "m0_closed" {
		t.Fatalf("default M0 opened: %v", err)
	}
	after, err := os.ReadDir(f.state)
	must(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("closed defaults changed P17 installation state")
	}
	d := DefaultDeps()
	m := m0.DefaultSeams()
	ctx := context.Background()
	rows := []struct {
		name            string
		closed, control func() error
	}{
		{"static gate", func() error { return d.Static.Verify("start") }, func() error { return f.gate.Verify("start") }},
		{"static gate", func() error { return d.Static.Verify("end") }, func() error { return f.gate.Verify("end") }},
		{"stop identity", d.Stops.VerifyPinned, f.stops.VerifyPinned},
		{"M0 emergency-stop identity", func() error { _, e := d.Stops.StopRequest(m0.StartRequest{}); return e }, func() error { _, e := f.stops.StopRequest(m0.StartRequest{}); return e }},
		{"retained event", func() error { _, e := d.Events.Event(m0.StartRequest{}); return e }, func() error { _, e := f.events.Event(m0.StartRequest{}); return e }},
		{"child interface", func() error { _, e := d.ChildInterface.Launch(m0.StartRequest{}); return e }, func() error { _, e := f.deps.ChildInterface.Launch(m0.StartRequest{}); return e }},
		{"child incarnation", func() error { _, e := d.ChildIncarnations.Child(m0.StartRequest{}, m0.StartReceipt{}); return e }, func() error { _, e := f.deps.ChildIncarnations.Child(m0.StartRequest{}, m0.StartReceipt{}); return e }},
		{"generation", func() error { _, e := d.Generations.Current(uint32(os.Getuid())); return e }, func() error { _, e := f.deps.Generations.Current(uint32(os.Getuid())); return e }},
		{"work-product export", func() error { _, e := d.Export.ExportStatus("fixture"); return e }, func() error { _, e := f.deps.Export.ExportStatus("fixture"); return e }},
		{"operator release", func() error { _, e := d.Release.Released("fixture", 30002); return e }, func() error { _, e := f.deps.Release.Released("fixture", 30002); return e }},
		{"policy source:", func() error { _, e := m.Policy.Current(); return e }, func() error { _, e := f.seams.Policy.Current(); return e }},
		{"clock:", func() error { _, e := m.Clock.Now(); return e }, func() error { _, e := f.seams.Clock.Now(); return e }},
		{"generation reader:", func() error { _, e := m.Generation.Current(30002); return e }, func() error { _, e := f.seams.Generation.Current(30002); return e }},
		{"runner argv:", func() error { _, e := m.Runner.Argv(nil, 0); return e }, func() error { _, e := f.seams.Runner.Argv(nil, 0); return e }},
		{"executor entry:", func() error { _, e := m.Entry.Compose(nil, m0.Execution{}, m0.Timing{}); return e }, func() error { _, e := f.seams.Entry.Compose(nil, m0.Execution{}, m0.Timing{}); return e }},
		{"launcher:", func() error { _, e := m.Launcher.Start(ctx, m0.StartRequest{}, nil); return e }, func() error { _, e := f.inner.Start(ctx, m0.StartRequest{}, nil); return e }},
		{"launcher:", func() error { _, e := m.Launcher.Stop(ctx, m0.StopRequest{}); return e }, func() error { _, e := f.inner.Stop(ctx, m0.StopRequest{}); return e }},
		{"launcher:", func() error { _, e := m.Launcher.Observe(ctx, m0.EffectQuery{}); return e }, func() error { _, e := f.inner.Observe(ctx, m0.EffectQuery{}); return e }},
		{"uid sweep killer", func() error { return d.Killer.KillAll(ctx, 30002) }, func() error { return f.deps.Killer.KillAll(ctx, 30002) }},
	}
	for _, row := range rows {
		if err := row.closed(); err == nil || !strings.Contains(err.Error(), row.name) {
			t.Errorf("default %s opened or lost its name: %v", row.name, err)
		}
		must(t, row.control())
	}
	if _, ok := d.Static.(UnpinnedStaticGate); !ok {
		t.Fatal("default static gate type opened")
	}
	if _, ok := d.Stops.(UnpinnedStopIdentity); !ok {
		t.Fatal("default stop identity type opened")
	}
	f.open()
}

// Claim: stop readiness is a pre-admission pin, not a synthetic stop request, and static start/end gates are checked once at boot and before each dispatch/cancel.
func TestAdmissionAndStaticClasses(t *testing.T) {
	f := newFixture(t)
	f.deps.Stops = UnpinnedStopIdentity{}
	if s, err := f.attach(); s != nil || m0.CodeOf(err) != "m0_closed" {
		t.Fatalf("unpinned stop identity admitted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.state, ledgerMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stop gate ran after boot side effects")
	}
	f.gate.classes = nil
	f.deps.Stops = f.stops
	s := f.open()
	if f.stops.requests != 0 || f.stops.verifications != 1 {
		t.Fatal("admission consumed or invented stop metadata")
	}
	f.gate.err = errors.New("fixture closed window")
	body, err := s.Serve(30001, dispatchID, f.frame)
	wireRefusal(t, body, err, string(m0.CodeClosed))
	if f.events.calls != 0 {
		t.Fatal("dispatch bypassed static gate")
	}
	body, err = s.Serve(30001, cancelID, f.cancel)
	wireRefusal(t, body, err, string(m0.CodeClosed))
	if f.inner.stops != 0 {
		t.Fatal("cancel bypassed end gate")
	}
	f.gate.err = nil
	body, err = s.Serve(30001, dispatchID, f.frame)
	wireRefusal(t, body, err, "m0_start_invalidated")
	if f.events.calls != 1 {
		t.Fatal("pinned gates did not reach generation boundary")
	}
	if !reflect.DeepEqual(f.gate.classes, []string{"start", "start", "end", "start"}) {
		t.Fatalf("gate classes %v", f.gate.classes)
	}
}

// Claim: only bodies accepted by M0's own decoder are classified, matching its dispatch/cancel operation; ordinary verifier refusals are definite and fatal ones remain response-free.
func TestStaticGateDecoder(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	f.gate.err = errors.New("fixture verifier refusal")
	for _, body := range [][]byte{f.frame, f.cancel} {
		r, err := m0.DecodeRequest(body)
		must(t, err)
		class := map[m0.Op]string{"dispatch": ClassStart, "cancel": ClassEnd}[r.Op]
		f.gate.classes = nil
		response, err := s.Serve(30001, r.RequestID, body)
		wireRefusal(t, response, err, string(m0.CodeClosed))
		if !reflect.DeepEqual(f.gate.classes, []string{class}) || f.events.calls != 0 || f.inner.stops != 0 {
			t.Fatalf("classification differed from M0: %s %v", r.Op, f.gate.classes)
		}
	}
	duplicate := bytes.Replace(f.frame, []byte(`"op":"dispatch"`), []byte(`"op":"cancel","op":"dispatch"`), 1)
	if _, err := m0.DecodeRequest(duplicate); err == nil {
		t.Fatal("duplicate control was not refused by M0")
	}
	f.gate.classes = nil
	response, err := s.Serve(30001, dispatchID, duplicate)
	if response != "" || !errors.Is(err, transport.ErrInvalid) || len(f.gate.classes) != 0 {
		t.Fatalf("duplicate body was classified: %q %v %v", response, err, f.gate.classes)
	}
	f.gate.err = transport.ErrFatal
	response, err = s.Serve(30001, dispatchID, f.frame)
	if response != "" || !errors.Is(err, transport.ErrFatal) {
		t.Fatalf("fatal verifier became a definite ordinary refusal: %q %v", response, err)
	}
}

// Claim: P17 journal initialization and established recovery require their own attestations even when both M0 attestations are true; each true P17 neighbor is accepted.
func TestP17JournalAttestations(t *testing.T) {
	for _, member := range []string{"supported_local_storage", "recovery_attested"} {
		t.Run(member, func(t *testing.T) {
			control := newFixture(t)
			s := control.open()
			f := newFixture(t)
			if member == "recovery_attested" {
				must(t, s.store.Create(control.record()))
				must(t, s.Close())
				facts, err := (installation{dir: control.state}).prepare()
				must(t, err)
				if !facts.journalEstablished {
					t.Fatal("positive record fact missing")
				}
				// Open the actual established P17 journal with its true attestation.
				mask := uint32(0177)
				owner := &sweep.Quarantine{}
				must(t, owner.Start(control.state, sweep.Established))
				defer owner.Close()
				store, err := runrecord.Open(journal.Config{Directory: control.state, Records: "records", Inputs: "inputs", DeclaredUmask: &mask, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 4 << 20}, owner)
				must(t, err)
				must(t, store.Close())
				prior := f.open()
				must(t, prior.store.Create(f.record()))
				must(t, prior.Close())
			}
			var config map[string]any
			must(t, json.Unmarshal(f.config, &config))
			config["p17"].(map[string]any)[member] = false
			f.config = encode(t, config)
			if config[member] != true {
				t.Fatal("control must keep the independent M0 attestation true")
			}
			opened, err := f.attach()
			if opened != nil || err == nil {
				t.Fatalf("P17 inherited the M0 attestation for %s: %v", member, err)
			}
			if member == "supported_local_storage" {
				if !errors.Is(err, runrecord.ErrConfig) || !strings.Contains(err.Error(), "local storage attestation") {
					t.Fatalf("wrong storage refusal: %v", err)
				}
			} else if journal.ClassOf(err) != journal.InvalidState || !strings.Contains(err.Error(), "recovery attestation required") {
				t.Fatalf("wrong recovery refusal: %v", err)
			}
		})
	}
}

// Claim: composition supplies exactly keymaker.UnixDialer with configuration pins, its own pinned Source adapter/dialer, and fixed placement from the installed worker rather than configuration.
func TestPinnedClientConstruction(t *testing.T) {
	f := newFixture(t)
	s := f.open()
	d := launcherDeps(f.cfg.P17, 30002, DefaultDeps(), s.store, s.quarantine)
	km, ok := d.Keymaker.(keymaker.UnixDialer)
	if !ok {
		t.Fatalf("unexpected Keymaker dialer %T", d.Keymaker)
	}
	if km.SocketUID != uint32(os.Getuid()) || km.PeerUID != uint32(os.Getuid()) || km.SocketGID != uint32(os.Getgid()) || km.SocketMode != 0600 || km.Path != filepath.Join(f.root, "km/request.sock") {
		t.Fatal("Keymaker pins drifted")
	}
	src := d.Source
	if src == nil {
		t.Fatal("missing Source adapter")
	}
	if _, ok := src.Dialer.(source.UnixDialer); !ok {
		t.Fatal("Source lost its own Unix dialer")
	}
	if _, ok := src.Wire.(source.Pinned); !ok {
		t.Fatal("Source adapter opened")
	}
	if _, ok := d.KeymakerWire.(keymaker.Pinned); !ok {
		t.Fatal("Keymaker wire opened")
	}
	uid, err := d.Placement.ChildUID(uint32(os.Getuid()))
	must(t, err)
	if uid != 30002 {
		t.Fatalf("placement UID %d", uid)
	}
}

// Claim: the shared quarantine owns launcher.lock before store creation, constructors/attach run in order, a second scope refuses, and close drains admission then closes records before releasing quarantine ownership.
func TestBootAndCloseOrder(t *testing.T) {
	f := newFixture(t)
	var trace []string
	f.deps.trace = func(stage string) {
		trace = append(trace, stage)
		if stage == "records" {
			q := &sweep.Quarantine{}
			err := q.Start(f.state, sweep.Established)
			_ = q.Close()
			if !errors.Is(err, syscall.EWOULDBLOCK) {
				t.Error("record store existed without prior shared lock ownership")
			}
		}
	}
	s := f.open()
	if !reflect.DeepEqual(trace, []string{"quarantine", "records", "launcher", "hooks", "attach"}) {
		t.Fatalf("boot order %v", trace)
	}
	f.deps.trace = nil
	if other, err := f.attach(); other != nil || err == nil {
		t.Fatal("second process scope took ownership")
	}
	must(t, s.Close())
	if !reflect.DeepEqual(trace, []string{"quarantine", "records", "launcher", "hooks", "attach", "stop-admitting", "close-records", "close-quarantine"}) {
		t.Fatalf("close order %v", trace)
	}
	if body, err := s.Serve(30001, "closed", f.frame); body != "" || err == nil {
		t.Fatal("closed session admitted request")
	}
	q := &sweep.Quarantine{}
	must(t, q.Start(f.state, sweep.Established))
	must(t, q.Close())
}

// Claim: a latched hook must-exit becomes transport.ErrFatal with no success envelope, remains latched and serves no later request; an ordinary pre-bound refusal beside it remains definite and replayable.
func TestHookExitObserver(t *testing.T) {
	f := newFixture(t)
	f.events.err = sweep.ErrNotDurable
	s := f.open()
	body, err := s.Serve(30001, dispatchID, f.frame)
	if body != "" || !errors.Is(err, transport.ErrFatal) || !errors.Is(err, sweep.ErrNotDurable) || s.ExitCause() == nil {
		t.Fatalf("exit latch not fatal: %q %v", body, err)
	}
	count := f.events.calls
	f.events.err = nil
	body, err = s.Serve(30001, dispatchID, f.frame)
	if body != "" || !errors.Is(err, transport.ErrFatal) || f.events.calls != count {
		t.Fatal("later request reached the hook after fatal")
	}
	ordinary := newFixture(t)
	ordinary.events.err = errFixture
	control := ordinary.open()
	body, err = control.Serve(30001, dispatchID, ordinary.frame)
	wireRefusal(t, body, err, "m0_start_invalidated")
	body, err = control.Serve(30001, dispatchID, ordinary.frame)
	wireRefusal(t, body, err, "m0_start_invalidated")
	if control.ExitCause() != nil {
		t.Fatal("ordinary refusal stopped serving")
	}
}

// Claim: the extension's optional observer checks startup and post-Serve exit duties, permanently returns the fatal class without success, and leaves ordinary launcher errors unknown without an observer.
func TestExtensionExitObserver(t *testing.T) {
	f := newFixture(t)
	var cause error
	f.inner.startErr = errFixture
	f.inner.atStart = func() { cause = syscall.EIO }
	e, err := m0.Attach("/configuration/m0.json", f.seams, f.read, func() error { return cause })
	must(t, err)
	defer e.Close()
	body, err := e.Serve(30001, dispatchID, f.frame)
	if body != "" || !errors.Is(err, transport.ErrFatal) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("post-Serve fatal: %q %v", body, err)
	}
	starts := f.inner.starts
	cause = nil
	_, err = e.Serve(30001, dispatchID, f.frame)
	if !errors.Is(err, transport.ErrFatal) || f.inner.starts != starts {
		t.Fatal("fatal extension served later request")
	}
	g := newFixture(t)
	g.inner.startErr = errFixture
	ordinary, err := m0.Attach("/configuration/m0.json", g.seams, g.read)
	must(t, err)
	defer ordinary.Close()
	body, err = ordinary.Serve(30001, dispatchID, g.frame)
	wireRefusal(t, body, err, "observation_unknown")
	body, err = ordinary.Serve(30001, dispatchID, g.frame)
	wireRefusal(t, body, err, "observation_unknown")
	if g.inner.starts != 1 {
		t.Fatal("ordinary launcher refusal repeated")
	}
	h := newFixture(t)
	failed, err := m0.Attach("/configuration/m0.json", h.seams, h.read, func() error { return syscall.EIO })
	if failed != nil || !errors.Is(err, transport.ErrFatal) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("startup latch admitted: %v", err)
	}
}

var _ launcher.RecordStore = (*recordStore)(nil)
var _ m0hook.StopRequests = (*fixtureStops)(nil)
