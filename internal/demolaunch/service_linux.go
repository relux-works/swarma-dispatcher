//go:build linux

package demolaunch

import (
	"bytes"
	"context"
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
	mu           sync.Mutex
	total        int
	byUID        map[uint32]int
	active       map[*os.File]struct{}
	stopped      bool
	readObserved func() // Optional non-reading test observer; nil in production.
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

func serveOperation(f *os.File, start time.Time, recheck func() error, engine *engine) error {
	return serveOperationObserved(f, start, recheck, engine, nil)
}
func serveOperationObserved(f *os.File, start time.Time, recheck func() error, engine *engine, observe func()) (result error) {
	wire, err := newSocketIO(f, start)
	if err != nil {
		return err
	}
	read := func() ([]byte, error) {
		if observe != nil {
			observe()
		}
		return wire.readFrame()
	}
	b, err := read()
	if err != nil {
		return err
	}
	if !bytes.Equal(b, []byte(`{"protocol":"swarma-demo-dispatcher/1"}`)) {
		return ErrInvalid
	}
	if err = recheck(); err != nil {
		return err
	}
	if err = wire.writeFrame(`{"protocol":"swarma-demo-dispatcher/1"}`); err != nil {
		return err
	}
	b, err = read()
	if err != nil {
		return err
	}
	if err = recheck(); err != nil {
		return err
	}
	req, _ := parseRequest(b)
	reply := engine.handle(b)
	if req.Op == "demo.launch" && (reply.State == "receipt-seen" || reply.State == "unknown") {
		defer func() {
			if result != nil {
				engine.unknown("launch_reply_lost")
			}
		}()
	}
	if err = recheck(); err != nil {
		return err
	}
	return wire.writeFrame(string(marshal(reply)))

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
func serve(ctx context.Context, e *endpoint, c serverConfig, stop *shutdown, engine *engine) error {
	return serveWith(ctx, e, c, stop, engine, peerUID, newSlots())
}

// Tests inject a peer result without weakening enrollment or the real entrypoint.
func serveWith(ctx context.Context, e *endpoint, c serverConfig, stop *shutdown, engine *engine, peer func(*os.File) (uint32, error), resources *slots) error {
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
		uid, err := peer(f)
		if err != nil {
			wakeClose(f)
			resources.release(nil)
			continue
		}
		var caller *principal
		if c.Caller.UID == uid {
			caller = &c.Caller
		}

		if caller == nil {
			wakeClose(f)
			resources.release(nil)
			continue
		}
		pin, err := principalPin(*caller)
		if err != nil {
			wakeClose(f)
			resources.release(nil)
			continue
		}
		if err = pin.recheck(); err != nil {
			pin.close()
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
			_ = serveOperationObserved(f, start, pin.recheck, engine, resources.readObserved)
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

// Daemon runs the separate demo, not production, launch protocol.
// It must run as a standalone process on a qualified local filesystem deployment.
func Daemon(configPath string) (result error) {
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
	c, roster, err := loadServerInputs(configPath, protectedSnapshot)
	if err != nil {
		stop.request(err)
		return err
	}
	if err = runtimeConfig(c.commonConfig, c.ServiceUID, true); err != nil {
		stop.request(err)
		return err
	}
	pin, x := principalPin(c.Caller)
	if x != nil {
		return x
	}
	pin.close()
	if ctx.Err() != nil {
		return nil
	}
	dir, err := endpointPin(c.commonConfig)
	if err != nil {
		stop.request(err)
		return err
	}
	e, err := createEndpointCalls(dir, c.commonConfig, publicationSyscalls(), stop)
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
	// Acquire endpoint singleton custody before inspecting or latching state.

	store, err := openState(c.StateDir, c.ServiceUID)
	if err != nil {
		return err
	}
	defer store.dir.Close()
	engine, err := newEngine(ctx, store, roster, c)
	if err != nil {
		return err
	}

	return serve(ctx, e, c, stop, engine)
}

// Client performs one demo exchange without automatic retry.
func Client(configPath, operation string, input []byte) (string, error) {
	r, err := parseRequest(input)
	if err != nil || r.Op != "demo."+operation {
		return "", ErrInvalid
	}
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
	return clientExchange(f, start, input)
}
func clientExchange(f *os.File, start time.Time, input []byte) (string, error) {
	wire, err := newSocketIO(f, start)
	if err != nil {
		return "", err
	}
	const hello = `{"protocol":"swarma-demo-dispatcher/1"}`
	if err = wire.writeFrame(hello); err != nil {
		return "", err
	}
	b, err := wire.readFrame()
	if err != nil {
		return "", err
	}
	if string(b) != hello {
		return "", ErrInvalid
	}
	if err = wire.writeFrame(string(input)); err != nil {
		return "", err
	}
	// The reply may wait for a 45-second stop, but intake stages remain two seconds.
	wire.responseWait = true
	b, err = wire.readFrame()
	if err != nil {
		return "", err
	}
	m, err := object(b, 16384, "state closed", "code timing receipt attempt stopped output_loss")
	if err != nil {
		return "", ErrInvalid
	}
	var r response
	if typed(b, &r) != nil {
		return "", ErrInvalid
	}
	switch r.State {
	case "refused", "unknown", "unresolved", "absent", "facts", "receipt-seen":
	default:
		return "", ErrInvalid
	}
	if r.State == "receipt-seen" {
		if r.Receipt == nil {
			return "", ErrInvalid
		}
		if _, err = parseReceipt(m["receipt"], r.Receipt.ExecutionID); err != nil {
			return "", err
		}
	}
	return string(b), nil
}

// authenticate performs no stream I/O, including on a failed pin.
func authenticate(f *os.File, uid uint32, pin func() error) error {
	if err := verifyPeer(f, uid); err != nil {
		return err
	}
	return pin()
}

// Both protected inputs are read before endpoint publication or state access.
// Tests substitute expected ownership, never the compiled roster path or modes.
func loadServerInputs(configPath string, read func(string, uint32, uint32, int) ([]byte, error)) (serverConfig, []account, error) {
	b, err := read(configPath, 0440, uint32(os.Getegid()), serverConfigLimit)
	if err != nil {
		return serverConfig{}, nil, err
	}
	c, err := parseServer(b)
	if err != nil {
		return serverConfig{}, nil, err
	}
	b, err = read(rosterPath, 0644, 0, 16384)
	if err != nil {
		return serverConfig{}, nil, err
	}
	roster, err := parseRoster(b, c.ServiceAccount)
	return c, roster, err
}
