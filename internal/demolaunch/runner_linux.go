//go:build linux

package demolaunch

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const sudoPath = "/usr/bin/sudo"
const launcherPath = "/usr/local/libexec/swarma/swarma-user-launcher"
const managerPath = "/usr/local/libexec/swarma/swarma-user-manager"
const logCap = 1 << 20

// The only injection seam is unexported. No flag, environment or config selects it.
type runner func(string) *exec.Cmd

func fixedRunner(path string) *exec.Cmd {
	c := exec.Command(sudoPath, "-n", path)
	c.Env = []string{}
	c.Dir = "/"
	return c
}

type child struct {
	done       chan struct{}
	err        error // published by closing done
	outputDone chan struct{}
	stdout     *capture
	stderr     *capture
}
type capture struct {
	mu                sync.Mutex
	data              []byte
	limit             int
	overflow          bool
	err               error
	file              *os.File
	onTruncate        func() error
	onLoss            func() error
	lossRecorded      bool
	lossPersistErr    error
	onLossUnpersisted func()
	writeFile         func([]byte) (int, error)
	// Nil in production; tests pin the actual stdin deadline at its call site.
	stdinNow      func() time.Time
	stdinDeadline func(time.Time)
}

func (c *capture) write(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(b)
	remaining := c.limit - len(c.data)
	// File captures count bytes without retaining log contents.
	if c.file != nil {
		remaining = c.limit
	}
	if n > remaining {
		n = remaining
	}
	if c.file != nil {
		if c.err == nil && n > 0 {
			write := c.writeFile
			if write == nil {
				write = c.file.Write
			}
			written, e := write(b[:n])
			c.limit -= written
			if e != nil {
				c.err = e
			}
			if written != n && e == nil {
				c.err = io.ErrShortWrite
			}
		}
	} else {
		c.data = append(c.data, b[:n]...)
	}
	if n < len(b) && !c.overflow {
		c.overflow = true
		if c.onTruncate != nil {
			c.err = errors.Join(c.err, c.onTruncate())
		}
	}
	c.recordLoss()
}
func (c *capture) snapshot() ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.data...), c.overflow, c.err
}
func drain(ctx context.Context, f *os.File, c *capture) {
	defer f.Close()
	if c.file != nil {
		defer func() {
			c.mu.Lock()
			c.err = errors.Join(c.err, c.file.Sync(), c.file.Close())
			c.recordLoss()
			c.mu.Unlock()
		}()
	}
	var b [32768]byte
	for ctx.Err() == nil {
		if err := f.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
			c.mu.Lock()
			c.err = err
			c.recordLoss()
			c.mu.Unlock()
			return
		}
		n, err := f.Read(b[:])
		if n > 0 {
			c.write(b[:n])
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		if err != nil {
			if err != io.EOF {
				c.mu.Lock()
				c.err = err
				c.recordLoss()
				c.mu.Unlock()
			}
			return
		}
	}
}

// Child exit is polled without blocking wait(2). There is one bounded-lifetime
// poller and drainer pair per occupied slot, never a goroutine per retry.
// Shutdown closes pipes and releases handles, never signals the child/account.
func startChild(ctx context.Context, run runner, path string, payload []byte, deadline time.Time, out, stderr *capture) (*child, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	c := run(path)
	c.Stdin = inR
	c.Stdout = outW
	c.Stderr = errW
	err = c.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, err
	}
	ch := &child{done: make(chan struct{}), outputDone: make(chan struct{}), stdout: out, stderr: stderr}
	var remaining atomic.Int32
	remaining.Store(2)
	finished := func() {
		if remaining.Add(-1) == 0 {
			close(ch.outputDone)
		}
	}
	go func() { defer finished(); drain(ctx, outR, out) }()
	go func() { defer finished(); drain(ctx, errR, stderr) }()
	go func() {
		defer close(ch.done)
		defer c.Process.Release()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var status syscall.WaitStatus
			pid, e := syscall.Wait4(c.Process.Pid, &status, syscall.WNOHANG, nil)
			if e != nil && e != syscall.EINTR {
				ch.err = e
				return
			}
			if pid == c.Process.Pid {
				if !status.Exited() || status.ExitStatus() != 0 {
					ch.err = errors.New("child_exit_nonzero")
				}
				return
			}
			select {
			case <-ctx.Done():
				ch.err = ctx.Err()
				return
			case <-ticker.C:
			}
		}
	}()
	now := time.Now
	if out.stdinNow != nil {
		now = out.stdinNow
	}
	intake := childIntakeDeadline(now(), deadline)
	if out.stdinDeadline != nil {
		out.stdinDeadline(intake)
	}
	err = inW.SetWriteDeadline(intake)
	if err == nil {
		var n int
		n, err = inW.Write(payload)
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
	}
	err = errors.Join(err, inW.Close())
	return ch, err
}

func childIntakeDeadline(now, deadline time.Time) time.Time {
	intake := now.Add(5 * time.Second)
	if deadline.Before(intake) {
		intake = deadline
	}
	return intake
}
func (e *engine) logCapture(generation, stream string) (*capture, error) {
	name := "output-" + generation + "-" + stream
	f, err := openAt(e.store.dir, name+".log", syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	id, x := identity(f)
	if x != nil || id.uid != e.store.uid || id.mode&07777 != 0600 || id.links != 1 {
		f.Close()
		return nil, ErrOwnership
	}
	if err = e.store.sync(f); err == nil {
		err = e.store.sync(e.store.dir)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &capture{file: f, limit: logCap, onTruncate: func() error { return e.store.create(name+".truncated", []byte("true")) }, onLoss: func() error { return e.store.create(name+".lost", []byte(`"output_lost"`)) }, onLossUnpersisted: func() { e.mu.Lock(); defer e.mu.Unlock(); _ = e.closeAdmission("loss_unpersisted") }}, nil
}

// capture.mu is held; persist at most once, continue draining on every failure.
func (c *capture) recordLoss() {
	if c.err != nil && !c.lossRecorded && c.onLoss != nil {
		c.lossRecorded = true
		c.lossPersistErr = c.onLoss()
		c.err = errors.Join(c.err, c.lossPersistErr)
		if c.lossPersistErr != nil && c.onLossUnpersisted != nil {
			// Never acquire engine.mu while holding capture.mu: status takes
			// those locks in the opposite order. Publish capture loss first.
			c.mu.Unlock()
			c.onLossUnpersisted()
			c.mu.Lock()
		}
	}
}
