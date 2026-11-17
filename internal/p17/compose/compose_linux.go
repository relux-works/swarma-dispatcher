//go:build linux

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/m0hook"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

// StopIdentity proves readiness without consuming or inventing a stop request.
// Its producer must authorize safe repeated consumption of emergency identities
// outside M0's normal journaled stop pass, as required by the hook contract.
type StopIdentity interface {
	m0hook.StopRequests
	VerifyPinned() error
}

type UnpinnedStopIdentity struct{ m0hook.UnpinnedStopRequests }

func (UnpinnedStopIdentity) VerifyPinned() error {
	return m0.Unpinned("stop identity: user-manager stop source is unpinned")
}

// Deps contains service producers, never authority derived from configuration,
// entry or receipt bytes. No Keymaker dialer can be injected here.
type Deps struct {
	Static            StaticGate
	Stops             StopIdentity
	Events            m0hook.RunEvents
	ChildInterface    m0hook.ChildInterface
	ChildIncarnations m0hook.ChildIncarnations
	Generations       source.Generations
	Incarnations      incarnation.Reader
	Killer            sweep.Killer
	Export            sweep.WorkProductExport
	Release           sweep.OperatorRelease
	// Private fixture seams select fake wires/procfs, never a different Keymaker dialer.
	sourceWire          source.Wire
	sourceDialer        source.Dialer
	keymakerWire        keymaker.Wire
	procRoot, mountInfo string
	syncFile            func(*os.File) error
	step                func(string) error
	trace               func(string)
}

func DefaultDeps() Deps {
	return Deps{Static: UnpinnedStaticGate{}, Stops: UnpinnedStopIdentity{},
		Events: m0hook.UnpinnedRunEvents{}, ChildInterface: m0hook.UnpinnedChildInterface{}, ChildIncarnations: m0hook.UnpinnedChildIncarnations{},
		Generations: source.UnpinnedGenerations{}, Incarnations: incarnation.Proc{}, Killer: sweep.UnpinnedKiller{},
		Export: sweep.UnpinnedWorkProductExport{}, Release: sweep.UnpinnedOperatorRelease{}}
}

type exitLatch struct {
	mu    sync.Mutex
	cause error
}

func (l *exitLatch) get() error { l.mu.Lock(); defer l.mu.Unlock(); return l.cause }
func (l *exitLatch) fail(err error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cause == nil {
		l.cause = errors.Join(sweep.ErrNotDurable, err)
	}
	return l.cause
}

type recordStore struct {
	*runrecord.Store
	install installation
	latch   *exitLatch
	mu      sync.Mutex
}

func (s *recordStore) Create(r *runrecord.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.latch.get(); err != nil {
		return err
	}
	first := s.Pending()
	if err := s.Store.Create(r); err != nil {
		return err
	}
	if first {
		if err := s.install.write(recordMarker, "established\n"); err != nil {
			return s.latch.fail(err)
		}
	}
	return nil
}
func (s *recordStore) Update(key string, mutate func(*runrecord.Record) error) (*runrecord.Record, error) {
	if err := s.latch.get(); err != nil {
		return nil, err
	}
	return s.Store.Update(key, mutate)
}

// Session owns exactly one shared P17 lifetime and the attached M0 extension.
// Serve/Close exclusion stops and drains admission before closing the stores.
type Session struct {
	mu         sync.RWMutex
	closed     bool
	ext        *m0.Extension
	store      *recordStore
	quarantine *sweep.Quarantine
	bootstrap  *os.File
	hook       *m0hook.Hooked
	latch      exitLatch
	gate       StaticGate
	trace      func(string)
}

func (s *Session) mark(step string) {
	if s.trace != nil {
		s.trace(step)
	}
}
func (s *Session) ExitCause() error {
	err := s.latch.get()
	if s.hook != nil {
		err = errors.Join(err, s.hook.ExitCause())
	}
	return err
}
func (s *Session) ConfigBytes() []byte {
	if s == nil {
		return nil
	}
	return s.ext.ConfigBytes()
}
func (s *Session) ManagedCallers() []transport.Principal { return s.ext.ManagedCallers() }
func (s *Session) Serve(peer uint32, id string, body []byte) (response string, resultErr error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return "", transport.ErrInvalid
	}
	if err := s.ExitCause(); err != nil {
		return "", errors.Join(transport.ErrFatal, err)
	}
	defer func() {
		if err := s.ExitCause(); err != nil {
			response, resultErr = "", errors.Join(resultErr, transport.ErrFatal, err)
		}
	}()
	var header struct {
		Kind string `json:"kind"`
		Op   string `json:"op"`
	}
	if json.Unmarshal(body, &header) == nil && header.Kind == m0.RequestKind {
		class := ""
		if header.Op == "dispatch" {
			class = ClassStart
		}
		if header.Op == "cancel" {
			class = ClassEnd
		}
		if class != "" {
			if err := s.gate.Verify(class); err != nil {
				if m0.MustExit(err) || launcher.MustExit(err) {
					s.latch.fail(err)
				}
				return "", err
			}
		}
	}
	return s.ext.Serve(peer, id, body)
}
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.mark("stop-admitting")
	var err error
	if s.ext != nil {
		err = s.ext.Close()
	}
	if s.store != nil {
		err = errors.Join(err, s.store.Close())
		s.mark("close-records")
	}
	if s.quarantine != nil {
		err = errors.Join(err, s.quarantine.Close())
		s.mark("close-quarantine")
	}
	if s.bootstrap != nil {
		err = errors.Join(err, s.bootstrap.Close())
	}
	return err
}
func closed(detail string, cause error) error {
	if m0.MustExit(cause) || launcher.MustExit(cause) {
		return errors.Join(transport.ErrFatal, cause)
	}
	return &m0.Refusal{Code: m0.CodeClosed, Detail: detail, Cause: cause}
}

// Attach uses one protected file read, builds the shared scope, runs H6 once,
// then hands the decorated original launcher to M0. No generation is converted.
func Attach(ctx context.Context, path string, seams m0.Seams, d Deps, read func(string) ([]byte, error)) (_ *Session, resultErr error) {
	b, err := read(path)
	if err != nil {
		return nil, &m0.Refusal{Code: m0.CodeRequestInvalid, Detail: "m0 configuration unreadable", Cause: err}
	}
	b = append([]byte(nil), b...)
	cfg, err := m0.ParseConfig(b)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, closed("M0 not selected by configuration", nil)
	}
	if d.Static == nil {
		d.Static = UnpinnedStaticGate{}
	}
	if err := d.Static.Verify(ClassStart); err != nil {
		return nil, closed("static gate refused composition", err)
	}
	if d.Stops == nil {
		d.Stops = UnpinnedStopIdentity{}
	}
	if err := d.Stops.VerifyPinned(); err != nil {
		return nil, closed("stop identity refused composition", err)
	}
	if seams.Policy == nil {
		seams.Policy = m0.UnpinnedPolicySource{}
	}
	installed, err := seams.Policy.Current()
	if err != nil {
		return nil, closed("policy source refused composition", err)
	}
	if installed == nil || installed.Policy == nil || installed.Removed {
		return nil, closed("installed policy required", nil)
	}
	worker := installed.Policy.Worker.UID
	if worker < 30000 || worker > 59999 || worker == uint32(os.Getuid()) {
		return nil, closed("installed worker UID refused", nil)
	}
	if seams.Launcher == nil {
		seams.Launcher = m0.UnpinnedLauncher{}
	}
	s := &Session{gate: d.Static, trace: d.trace}
	defer func() {
		if resultErr != nil {
			if launcher.MustExit(resultErr) {
				resultErr = errors.Join(transport.ErrFatal, s.latch.fail(resultErr))
			}
			resultErr = errors.Join(resultErr, s.Close())
		}
	}()
	// The transport's socket-safe umask applies before composition creates files.
	// All state directories are preinstalled, never created or repaired here.
	syscall.Umask(0177)
	s.bootstrap, err = bootstrapLock(cfg.P17.StateDir)
	if err != nil {
		return nil, err
	}
	install := installation{dir: cfg.P17.StateDir, syncFile: d.syncFile, step: d.step}
	facts, err := install.prepare()
	if err != nil {
		return nil, err
	}
	s.quarantine = &sweep.Quarantine{}
	if err := s.quarantine.Start(cfg.P17.StateDir, facts.ledger); err != nil {
		return nil, err
	}
	s.mark("quarantine")
	if err := install.checkpoint("quarantine-started"); err != nil {
		return nil, err
	}
	if facts.firstStart {
		if err := install.write(ledgerMarker, "established\n"); err != nil {
			return nil, s.latch.fail(err)
		}
	}
	umask := uint32(0177)
	jc := journal.Config{Directory: cfg.P17.StateDir, Records: "records", Inputs: "inputs", DeclaredUmask: &umask, SupportedLocalStorage: cfg.SupportedLocalStorage, RecoveryPrerequisiteEstablished: cfg.RecoveryAttested, ConsumerBudget: 4 << 20}
	var store *runrecord.Store
	if facts.journalEstablished {
		store, err = runrecord.Open(jc, s.quarantine)
	} else {
		store, err = runrecord.Initialize(jc)
	}
	if err != nil {
		return nil, err
	}
	s.store = &recordStore{Store: store, install: install, latch: &s.latch}
	s.mark("records")
	deps := launcherDeps(cfg.P17, worker, d, s.store, s.quarantine)
	l, err := launcher.New(launcher.Config{StateDirectory: jc.Directory, Installation: facts.ledger, LauncherUID: uint32(os.Getuid()), Station: cfg.P17.Station, RequestTimeout: time.Duration(cfg.P17.BindTimeoutMS) * time.Millisecond, ProcRoot: d.procRoot, ProcMountInfo: d.mountInfo}, deps)
	if err != nil {
		return nil, err
	}
	s.mark("launcher")
	s.hook, err = m0hook.New(ctx, m0hook.Deps{L: l, Inner: seams.Launcher, Installation: facts.ledger, Hygiene: d.ChildInterface, Incarnations: d.ChildIncarnations, Events: d.Events, Stops: d.Stops})
	if err != nil {
		return nil, err
	}
	s.mark("hooks")
	seams.Launcher = s.hook
	s.ext, err = m0.Attach(path, seams, func(string) ([]byte, error) { return append([]byte(nil), b...), nil }, s.ExitCause)
	if err != nil {
		return nil, err
	}
	s.mark("attach")
	if err := s.ExitCause(); err != nil {
		return nil, errors.Join(transport.ErrFatal, err)
	}
	return s, nil
}

func launcherDeps(p m0.P17Config, worker uint32, d Deps, store launcher.RecordStore, q *sweep.Quarantine) launcher.Deps {
	if d.Generations == nil {
		d.Generations = source.UnpinnedGenerations{}
	}
	if d.Incarnations == nil {
		d.Incarnations = incarnation.Proc{}
	}
	wire := d.sourceWire
	if wire == nil {
		wire = source.Pinned{}
	}
	kmWire := d.keymakerWire
	if kmWire == nil {
		kmWire = keymaker.Pinned{}
	}
	dialer := d.sourceDialer
	if dialer == nil {
		dialer = source.UnixDialer{}
	}
	client := &source.Client{Pins: source.Pins{Root: "/", ControlDir: p.SourceControlDir, SourceUID: p.SourceUID, SourceGeneration: p.SourceGeneration}, Dialer: dialer, Wire: wire, Generations: d.Generations}
	return launcher.Deps{Placement: isolation.Fixed(worker), Source: client, Generations: d.Generations,
		Keymaker:     keymaker.UnixDialer{Root: "/", Path: filepath.Join(p.KeymakerSocketDir, p.KeymakerSocketName), SocketUID: p.KeymakerUID, SocketGID: p.KeymakerGID, SocketMode: os.FileMode(p.KeymakerMode), PeerUID: p.KeymakerUID, Timeout: time.Duration(p.BindTimeoutMS) * time.Millisecond},
		KeymakerWire: kmWire, Records: store, Quarantine: q, Incarnations: d.Incarnations, Killer: d.Killer, WorkProductExport: d.Export, OperatorRelease: d.Release}
}
