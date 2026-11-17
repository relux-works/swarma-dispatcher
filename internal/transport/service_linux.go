//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Shutdown begins at the first signal/fatal request, never at completion of accept.
const shutdownBudget = time.Second

type shutdown struct {
	mu     sync.Mutex
	start  time.Time
	cause  error
	cancel context.CancelFunc
}

func (s *shutdown) request(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.start.IsZero() {
		s.start = time.Now()
		s.cause = cause
		s.cancel()
	} else if cause != nil {
		s.cause = errors.Join(s.cause, cause)
	}
}
func (s *shutdown) snapshot() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.start, s.cause
}

type slots struct {
	mu      sync.Mutex
	total   int
	byUID   map[uint32]int
	active  map[*os.File]struct{}
	stopped bool
}

func newSlots() *slots {
	return &slots{byUID: make(map[uint32]int), active: make(map[*os.File]struct{})}
}
func (s *slots) global() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.total >= 64 {
		return false
	}
	s.total++
	return true
}
func (s *slots) uid(uid uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.byUID[uid] >= 8 {
		return false
	}
	s.byUID[uid]++
	return true
}
func (s *slots) release(uid *uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total--
	if uid != nil {
		s.byUID[*uid]--
		if s.byUID[*uid] == 0 {
			delete(s.byUID, *uid)
		}
	}
}
func (s *slots) register(f *os.File) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	s.active[f] = struct{}{}
	return true
}
func (s *slots) remove(f *os.File) { s.mu.Lock(); defer s.mu.Unlock(); delete(s.active, f) }
func (s *slots) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	for f := range s.active {
		wakeClose(f)
	}
}

func serveOperation(f *os.File, start time.Time, recheck func() error) error {
	return serveOperationExt(f, start, recheck, 0, nil)
}

// serveOperationExt is serveOperation with an optional post-hello extension.
// Unmanaged peers keep the reference grammar, with other frames offered once
// to ext when attached. Managed peers can reach only the M0 discriminator and
// require ext. Results share the pin recheck, and ErrFatal propagates.
func serveOperationExt(f *os.File, start time.Time, recheck func() error, peer uint32, ext Extension) error {
	wire, err := newSocketIO(f, start)
	if err != nil {
		return err
	}
	b, err := wire.readFrame()
	if err != nil {
		return err
	}
	id, err := parseHello(b)
	if err != nil {
		return err
	}
	clear(b)
	b = nil // Only the bounded copied correlation ID survives hello recognition.
	// The accept check precedes reads; this check also precedes the hello result.
	if err = recheck(); err != nil {
		return err
	}
	if err = wire.writeFrame(helloResult(id)); err != nil {
		return err
	}
	b, err = wire.readFrame()
	if err != nil {
		return err
	}
	allowed, err := parseOperation(b, id)
	var response string
	switch {
	case managed(peer):
		// Managed admission never reaches the reference-subset recognizers.
		if ext == nil || !m0Request(b) {
			return ErrInvalid
		}
		response, err = ext.Serve(peer, id, b)
		clear(b)
		if err != nil {
			return err
		}
	case err == nil:
		response = invalid(id)
		if allowed {
			response = hold(id)
		}
	case ext != nil && errors.Is(err, ErrInvalid):
		response, err = ext.Serve(peer, id, b)
		clear(b)
		if err != nil {
			return err
		}
	default:
		return err
	}
	if err = recheck(); err != nil {
		return err
	}
	return wire.writeFrame(response)
}
func acceptOne(listener *os.File) (*os.File, time.Time, error) {
	raw, err := listener.SyscallConn()
	if err != nil {
		return nil, time.Time{}, err
	}
	var fd int
	var call error
	var start time.Time
	err = raw.Read(func(p uintptr) bool {
		fd, _, call = syscall.Accept4(int(p), syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC)
		if call == nil {
			start = time.Now()
		}
		return call != syscall.EAGAIN && call != syscall.EWOULDBLOCK
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	if call != nil {
		return nil, time.Time{}, call
	}
	return os.NewFile(uintptr(fd), "dispatcher connection"), start, nil
}

// Only these connection-local interruptions are retried, eight consecutively.
// EAGAIN belongs to runtime readiness waiting, not this accept retry policy.
func transientAccept(err error) bool {
	return errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPROTO)
}
func serve(ctx context.Context, e *endpoint, c serverConfig, stop *shutdown) error {
	return serveExt(ctx, e, c, stop, nil)
}

// serveExt is serve with an optional extension. An extension ErrFatal from any
// connection requests daemon shutdown with that cause.
func serveExt(ctx context.Context, e *endpoint, c serverConfig, stop *shutdown, ext Extension) error {
	return serveObserved(ctx, e, c, stop, ext, admissionCalls{peerUID, principalPin})
}

// admissionCalls is an internal observation seam, never configurable by a caller.
// Production always supplies the kernel credential lookup and principalPin.
type admissionCalls struct {
	peer func(*os.File) (uint32, error)
	pin  func(principal) (*directory, error)
}

func managedCallers(c serverConfig, ext Extension, pin func(principal) (*directory, error)) ([]principal, error) {
	if ext == nil {
		return nil, nil
	}
	callers := append([]principal(nil), ext.ManagedCallers()...)
	if len(callers) > 4 {
		return nil, ErrConfig
	}
	for i, p := range callers {
		if !p.ValidManaged() || p.UID == c.ServiceUID || i > 0 && callers[i-1].UID >= p.UID {
			return nil, ErrConfig
		}
		// The ranges are disjoint, but assert that the two roles never overlap.
		for _, other := range c.Callers {
			if other.UID == p.UID {
				return nil, ErrConfig
			}
		}
		d, err := pin(p)
		if err != nil {
			return nil, err
		}
		d.close()
	}
	return callers, nil
}

func serveObserved(ctx context.Context, e *endpoint, c serverConfig, stop *shutdown, ext Extension, calls admissionCalls) error {
	callers, err := managedCallers(c, ext, calls.pin)
	if err != nil {
		return err
	}
	return serveAdmitted(ctx, e, c, stop, ext, callers, calls)
}

func serveAdmitted(ctx context.Context, e *endpoint, c serverConfig, stop *shutdown, ext Extension, managedPins []principal, calls admissionCalls) error {
	resources := newSlots()
	var workers sync.WaitGroup
	watcherDone := make(chan struct{})
	listener := e.listener
	go func() { defer close(watcherDone); <-ctx.Done(); resources.stop(); wakeClose(listener) }()
	consecutive := 0
	for ctx.Err() == nil {
		f, start, err := acceptOne(e.listener)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if transientAccept(err) && consecutive < 8 {
				consecutive++
				continue
			}
			stop.request(err)
			break
		}
		consecutive = 0
		// One transient accepted descriptor; no per-connection worker/body before both slots.
		if !resources.global() {
			wakeClose(f)
			continue
		}
		if err = tuneSocket(f); err != nil {
			wakeClose(f)
			resources.release(nil)
			continue
		}
		uid, err := calls.peer(f)
		if err != nil {
			wakeClose(f)
			resources.release(nil)
			continue
		}
		var caller *principal
		candidates := c.Callers
		if managed(uid) {
			candidates = nil
			if ext != nil {
				candidates = managedPins
			}
		}
		for i := range candidates {
			if candidates[i].UID == uid {
				caller = &candidates[i]
				break
			}
		}
		if caller == nil {
			wakeClose(f)
			resources.release(nil)
			continue
		}
		pin, err := calls.pin(*caller)
		if err != nil {
			wakeClose(f)
			resources.release(nil)
			continue
		}
		if !resources.uid(uid) {
			pin.close()
			wakeClose(f)
			resources.release(nil)
			continue
		}
		if !resources.register(f) {
			pin.close()
			wakeClose(f)
			resources.release(&uid)
			continue
		}
		workers.Add(1)
		go func(f *os.File, uid uint32, pin *directory, start time.Time) {
			defer workers.Done()
			defer resources.release(&uid)
			defer resources.remove(f)
			defer pin.close()
			defer wakeClose(f)
			// Protocol errors close only this connection, without retaining payload/IDs.
			if err := serveOperationExt(f, start, pin.recheck, uid, ext); errors.Is(err, ErrFatal) {
				stop.request(err)
			}
		}(f, uid, pin, start)
	}
	stop.request(nil)
	begin, cause := stop.snapshot()
	remaining := time.Until(begin.Add(shutdownBudget))
	if remaining <= 0 {
		return errors.Join(cause, ErrShutdown)
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-watcherDone:
	case <-timer.C:
		return errors.Join(cause, ErrShutdown)
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
		return cause
	case <-timer.C:
		return errors.Join(cause, ErrShutdown)
	}
}

// Daemon loads one protected server snapshot and serves only capacity:hold.
// It must run as a standalone process on a qualified local filesystem deployment.
func Daemon(configPath string) error { return DaemonWith(configPath, nil) }

// DaemonWith is Daemon with an optional post-hello extension. A nil extension
// is the reference subset; the caller owns the extension's lifetime.
func DaemonWith(configPath string, ext Extension) error {
	s, err := LoadServer(configPath)
	if err != nil {
		return err
	}
	return DaemonFromSnapshot(s, ext)
}

// DaemonFromSnapshot consumes a protected snapshot without reopening its path.
func DaemonFromSnapshot(s *ServerSnapshot, ext Extension) error {
	return daemonFromSnapshot(s, ext, daemonCalls{endpointPin, createEndpointCalls, admissionCalls{peerUID, principalPin}})
}

type daemonCalls struct {
	pin       func(commonConfig) (*directory, error)
	publish   func(*directory, commonConfig, publicationCalls, *shutdown) (*endpoint, error)
	admission admissionCalls
}

// The shared lifecycle installs umask and signal handling before any file
// creation. Both public entry points use exactly the same serve/cleanup path.
func daemonFromSnapshot(snapshot *ServerSnapshot, ext Extension, calls daemonCalls) (result error) {
	// Set once before product concurrency/file creation. New socket inode starts 0600.
	syscall.Umask(0177)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := &shutdown{cancel: cancel}
	defer func() {
		stop.request(result)
		begin, _ := stop.snapshot()
		if time.Since(begin) > shutdownBudget {
			result = errors.Join(result, ErrShutdown)
		}
	}()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	signalDone := make(chan struct{})
	defer close(signalDone)
	go func() {
		for {
			select {
			case <-signals:
				stop.request(nil)
			case <-signalDone:
				return
			}
		}
	}()
	c, err := snapshot.take()
	if err != nil {
		stop.request(err)
		return err
	}
	managedPins, err := managedCallers(c, ext, principalPin)
	if err != nil {
		stop.request(err)
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	dir, err := calls.pin(c.commonConfig)
	if err != nil {
		stop.request(err)
		return err
	}
	e, err := calls.publish(dir, c.commonConfig, publicationSyscalls(), stop)
	if err != nil {
		stop.request(err)
		return err
	}
	defer func() {
		stop.request(nil)
		begin, _ := stop.snapshot()
		result = errors.Join(result, e.closeBefore(begin.Add(shutdownBudget)))
		if time.Since(begin) > shutdownBudget {
			result = errors.Join(result, ErrShutdown)
		}
	}()
	return serveAdmitted(ctx, e, c, stop, ext, managedPins, calls.admission)
}

// Capacity performs exactly one authenticated exchange, returning the closed
// hold JSON. A returned response is always a refusal, never available capacity.
func Capacity(configPath string) (string, error) {
	b, err := sourceSnapshot(configPath, false)
	if err != nil {
		return "", err
	}
	c, err := parseClient(b)
	if err != nil {
		return "", err
	}
	if err = runtimeConfig(c.commonConfig, c.Caller.UID, false); err != nil {
		return "", err
	}
	pin, err := principalPin(c.Caller)
	if err != nil {
		return "", err
	}
	defer pin.close()
	d, err := endpointPin(c.commonConfig)
	if err != nil {
		return "", err
	}
	defer d.close()
	before, err := atIdentity(d, endpointName)
	if err != nil {
		return "", err
	}
	if !socketIdentity(before, c.commonConfig) {
		return "", ErrIdentity
	}
	if err = pin.recheck(); err != nil {
		return "", err
	}
	start := time.Now()
	f, err := connectSocket(d, start)
	if err != nil {
		return "", err
	}
	defer wakeClose(f)
	if err = d.recheck(); err != nil {
		return "", err
	}
	after, err := atIdentity(d, endpointName)
	if err != nil {
		return "", err
	}
	if !before.attrs(after) {
		return "", ErrIdentity
	}
	if err = verifyPeer(f, c.ServiceUID); err != nil {
		return "", err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random[:])
	return capacityExchange(f, start, id)
}
func capacityExchange(f *os.File, start time.Time, id string) (string, error) {
	if !validID(id) {
		return "", ErrInvalid
	}
	wire, err := newSocketIO(f, start)
	if err != nil {
		return "", err
	}
	if err = wire.writeFrame(hello(id)); err != nil {
		return "", err
	}
	b, err := wire.readFrame()
	if err != nil {
		return "", err
	}
	if !bytes.Equal(b, []byte(helloResult(id))) {
		return "", ErrInvalid
	}
	if err = wire.writeFrame(capacity(id)); err != nil {
		return "", err
	}
	b, err = wire.readFrame()
	if err != nil {
		return "", err
	}
	if !bytes.Equal(b, []byte(hold(id))) {
		return "", ErrInvalid
	}
	return hold(id), nil
}
