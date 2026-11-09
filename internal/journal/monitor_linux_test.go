//go:build linux && (amd64 || arm64)

package journal

import (
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fixtureEngine keeps monitors outside replaceable fault seams. Sessions use
// engine.calls; tests replace calls below it, so even a nondelegating fault is
// observed. Acquisition is not a published Session and needs no session mutex.
type fixtureEngine struct {
	*engine
	calls   calls
	monitor *fixtureMonitor
}
type seamEvent struct {
	kind  string
	fd    int
	name  string
	flags int
}
type fixtureMonitor struct {
	mu                        sync.Mutex
	published                 map[int]*Session
	locks                     map[int]bool
	callbacks                 map[int]int
	report                    func(string, ...any)
	hook                      func(seamEvent) error
	ownershipHits, serialHits int
}

var fixtureFlockFailed atomic.Bool

var fixtureLocks = struct {
	sync.Mutex
	fds map[int]bool
}{fds: make(map[int]bool)}

func (m *fixtureMonitor) before(v seamEvent) error {
	m.mu.Lock()
	s := m.published[v.fd]
	active := m.callbacks[v.fd] > 0 && m.locks[v.fd]
	hook := m.hook
	m.mu.Unlock()
	if active && (v.kind == "close" || (v.kind == "flock" && v.flags&syscall.LOCK_UN != 0)) {
		m.mu.Lock()
		m.ownershipHits++
		m.mu.Unlock()
		m.report("OWNERSHIP-MONITOR: %s releases lock inside callback", v.kind)
	}
	if s != nil && s.mu.TryLock() {
		s.mu.Unlock()
		m.mu.Lock()
		m.serialHits++
		m.mu.Unlock()
		m.report("SERIAL-MONITOR: %s without published session mutex", v.kind)
	}
	if hook != nil {
		return hook(v)
	}
	return nil
}
func newFixtureEngine(t *testing.T, c calls) *fixtureEngine {
	t.Helper()
	if fixtureFlockFailed.Load() {
		t.Fatal("FLOCK-BOUND: earlier adapter timeout")
	}
	m := &fixtureMonitor{published: make(map[int]*Session), locks: make(map[int]bool), callbacks: make(map[int]int), report: t.Errorf}
	e := &fixtureEngine{engine: &engine{}, calls: c, monitor: m}
	e.engine.calls = calls{
		open: func(d int, n string, f int, mode uint32) (int, error) {
			if err := m.before(seamEvent{"open", d, n, f}); err != nil {
				return -1, err
			}
			fd, err := e.calls.open(d, n, f, mode)
			if err == nil {
				m.mu.Lock()
				if s := m.published[d]; s != nil {
					m.published[fd] = s
				}
				if n == "lock" {
					m.locks[fd] = true
				}
				m.mu.Unlock()
				if n == "lock" {
					fixtureLocks.Lock()
					fixtureLocks.fds[fd] = true
					fixtureLocks.Unlock()
				}
			}
			return fd, err
		},
		stat: func(fd int, s *syscall.Stat_t) error {
			if err := m.before(seamEvent{kind: "stat", fd: fd}); err != nil {
				return err
			}
			return e.calls.stat(fd, s)
		},
		statfs: func(fd int, s *syscall.Statfs_t) error {
			if err := m.before(seamEvent{kind: "statfs", fd: fd}); err != nil {
				return err
			}
			return e.calls.statfs(fd, s)
		},
		flock: func(fd, how int) error {
			if err := m.before(seamEvent{kind: "flock", fd: fd, flags: how}); err != nil {
				return err
			}
			return boundedFlock(t, func() error { return e.calls.flock(fd, how) })
		},
		read: func(fd int, p []byte, o int64) (int, error) {
			if err := m.before(seamEvent{kind: "read", fd: fd}); err != nil {
				return 0, err
			}
			return e.calls.read(fd, p, o)
		},
		write: func(fd int, p []byte, o int64) (int, error) {
			if err := m.before(seamEvent{kind: "write", fd: fd}); err != nil {
				return 0, err
			}
			return e.calls.write(fd, p, o)
		},
		sync: func(fd int) error {
			if err := m.before(seamEvent{kind: "sync", fd: fd}); err != nil {
				return err
			}
			return e.calls.sync(fd)
		},
		rename: func(a int, b string, c int, d string) error {
			if err := m.before(seamEvent{kind: "rename", fd: a, name: b}); err != nil {
				return err
			}
			return e.calls.rename(a, b, c, d)
		},
		close: func(fd int) error {
			injected := m.before(seamEvent{kind: "close", fd: fd})
			err := e.calls.close(fd) // close faults still close the real fixture descriptor
			m.mu.Lock()
			delete(m.published, fd)
			delete(m.locks, fd)
			m.mu.Unlock()
			fixtureLocks.Lock()
			delete(fixtureLocks.fds, fd)
			fixtureLocks.Unlock()
			if injected != nil {
				return injected
			}
			return err
		},
		random: func(b []byte) (int, error) { return e.calls.random(b) },
	}
	return e
}

// The wrapper marks both callback phases, including callbacks that ignore errors.
func (e *fixtureEngine) enter(c Config, p []byte, consumer Consumer, init bool) (*Session, error) {
	if consumer != nil {
		consumer = monitoredConsumer{consumer, e.monitor}
	}
	s, err := e.engine.enter(c, p, consumer, init)
	if s != nil {
		e.monitor.mu.Lock()
		for _, fd := range s.owned {
			e.monitor.published[fd] = s
		}
		e.monitor.published[s.lock] = s
		e.monitor.mu.Unlock()
	}
	return s, err
}

type monitoredConsumer struct {
	Consumer
	m *fixtureMonitor
}

func (c monitoredConsumer) during(r *Recovery, fn func() error) error {
	c.m.mu.Lock()
	fd := r.s.lock
	c.m.callbacks[fd]++
	c.m.mu.Unlock()
	defer func() { c.m.mu.Lock(); c.m.callbacks[fd]--; c.m.mu.Unlock() }()
	return fn()
}
func (c monitoredConsumer) Consume(n uint64, p []byte, r *Recovery) error {
	return c.during(r, func() error { return c.Consumer.Consume(n, p, r) })
}
func (c monitoredConsumer) Finish(r *Recovery) error {
	return c.during(r, func() error { return c.Consumer.Finish(r) })
}

// Bound all fixture flock calls, not only a cached adapter preflight. On failure
// release only descriptors registered by these tests, then join the contender.
func boundedFlock(t *testing.T, call func() error) error {
	t.Helper()
	if fixtureFlockFailed.Load() {
		return syscall.ETIMEDOUT
	}
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
	}
	fixtureFlockFailed.Store(true)
	t.Errorf("FLOCK-BOUND: acquisition exceeded two seconds")
	fixtureLocks.Lock()
	for fd := range fixtureLocks.fds {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
	}
	fixtureLocks.Unlock()
	select {
	case <-done:
		return syscall.ETIMEDOUT
	case <-time.After(2 * time.Second):
		t.Fatalf("FLOCK-BOUND: acquisition did not quiesce")
		return syscall.ETIMEDOUT
	}
}

// Controls exercise real callback wiring and locked/unlocked calls of every
// descriptor seam, including inheritance; predicate controls also pin close.
func TestFixtureMonitorControls(t *testing.T) {
	t.Run("wiring", testFixtureMonitorWiring)
	for _, kind := range []string{"close", "unlock", "sync"} {
		t.Run(kind, func(t *testing.T) {
			c, e := fixture(t, true)
			s := start(t, c, e)
			var messages []string
			e.monitor.report = func(f string, a ...any) { messages = append(messages, fmt.Sprintf(f, a...)) }
			if kind == "sync" {
				original := e.calls.sync
				e.calls.sync = func(int) error { return nil }
				s.mu.Lock()
				must(t, s.e.calls.sync(s.journal))
				s.mu.Unlock()
				if len(messages) != 0 {
					t.Fatal("SERIAL-MONITOR-CONTROL: locked positive refused")
				}
				must(t, s.e.calls.sync(s.journal))
				e.calls.sync = original
				if len(messages) != 1 || e.monitor.serialHits != 1 {
					t.Fatal("SERIAL-MONITOR-CONTROL: unlocked violation missed")
				}
			} else {
				// Exercise the monitor without actually releasing this owner's lock.
				cl, fl := e.calls.close, e.calls.flock
				e.calls.close = func(int) error { return nil }
				e.calls.flock = func(int, int) error { return nil }
				invoke := func() {
					if kind == "close" {
						must(t, s.e.calls.close(s.lock))
					} else {
						must(t, s.e.calls.flock(s.lock, syscall.LOCK_UN))
					}
				}
				s.mu.Lock()
				invoke()
				s.mu.Unlock()
				if len(messages) != 0 {
					t.Fatal("OWNERSHIP-MONITOR-CONTROL: inactive callback refused")
				}
				e.monitor.mu.Lock()
				e.monitor.locks[s.lock] = true
				e.monitor.published[s.lock] = s
				e.monitor.callbacks[s.lock] = 1
				e.monitor.mu.Unlock()
				s.mu.Lock()
				invoke()
				s.mu.Unlock()
				e.monitor.mu.Lock()
				e.monitor.callbacks[s.lock] = 0
				e.monitor.mu.Unlock()
				e.calls.close, e.calls.flock = cl, fl
				e.monitor.mu.Lock()
				e.monitor.locks[s.lock] = true
				e.monitor.published[s.lock] = s
				e.monitor.mu.Unlock()
				fixtureLocks.Lock()
				fixtureLocks.fds[s.lock] = true
				fixtureLocks.Unlock()
				if len(messages) != 1 || e.monitor.ownershipHits != 1 {
					t.Fatal("OWNERSHIP-MONITOR-CONTROL: callback release violation missed")
				}
			}
			e.monitor.report = t.Errorf
		})
	}
}
