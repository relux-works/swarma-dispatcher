//go:build linux

package demolaunch

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/frame"
)

const stageBudget = 2 * time.Second
const sessionBudget = 55 * time.Second

func stageDeadline(start, now time.Time) time.Time {
	d := now.Add(stageBudget)
	end := start.Add(sessionBudget)
	if end.Before(d) {
		return end
	}
	return d
}
func control(f *os.File, fn func(int) error) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var call error
	if err = raw.Control(func(fd uintptr) { call = fn(int(fd)) }); err != nil {
		return err
	}
	return call
}
func tuneSocket(f *os.File) error {
	return control(f, func(fd int) error {
		for _, option := range []int{syscall.SO_RCVBUF, syscall.SO_SNDBUF} {
			if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, option, 65536); err != nil {
				return err
			}
			n, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, option)
			if err != nil {
				return err
			}
			if n <= 0 || n > 131072 {
				return ErrLimit
			}
		}
		return nil
	})
}
func peerUID(f *os.File) (uint32, error) {
	var uid uint32
	err := control(f, func(fd int) error {
		cred, err := syscall.GetsockoptUcred(fd, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err == nil {
			uid = cred.Uid
		}
		return err
	})
	return uid, err
}
func wakeClose(f *os.File) {
	_ = f.SetDeadline(time.Now())
	_ = control(f, func(fd int) error { return syscall.Shutdown(fd, syscall.SHUT_RDWR) })
	_ = f.Close()
}

// disposeControls never trusts a right as authority. It also closes complete
// returned rights preceding malformed/truncated control tails; kernel disposal
// of undelivered descriptors remains an isolated-Linux qualification obligation.
func disposeControls(b []byte, flags int) error {
	refused := len(b) > 0 || flags & ^syscall.MSG_CMSG_CLOEXEC != 0
	header := syscall.CmsgLen(0)
	word := strconv.IntSize / 8
	for len(b) >= header {
		var length uint64
		if word == 8 {
			length = binary.NativeEndian.Uint64(b)
		} else {
			length = uint64(binary.NativeEndian.Uint32(b))
		}
		level := int32(binary.NativeEndian.Uint32(b[word:]))
		kind := int32(binary.NativeEndian.Uint32(b[word+4:]))
		if length < uint64(header) {
			break
		}
		available := uint64(len(b))
		if length < available {
			available = length
		}
		if level == syscall.SOL_SOCKET && kind == syscall.SCM_RIGHTS {
			for data := b[header:int(available)]; len(data) >= 4; data = data[4:] {
				fd := int(int32(binary.NativeEndian.Uint32(data)))
				if fd >= 0 {
					_ = syscall.Close(fd)
				}
			}
		}
		if length > uint64(len(b)) {
			break
		}
		step := syscall.CmsgSpace(int(length) - header)
		if step > len(b) || step <= 0 {
			break
		}
		b = b[step:]
	}
	if refused {
		return ErrAncillary
	}
	return nil
}

type socketIO struct {
	responseWait bool
	file         *os.File
	raw          syscall.RawConn
	start        time.Time
	prefix       [4]byte
	oob          [4096]byte
	out          [131076]byte
	body         []byte
}

func newSocketIO(f *os.File, start time.Time) (*socketIO, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	return &socketIO{file: f, raw: raw, start: start}, nil
}
func (s *socketIO) receive(p []byte) (int, error) {
	var n int
	var call error
	err := s.raw.Read(func(fd uintptr) bool {
		clear(s.oob[:])
		var count, flags int
		n, count, flags, _, call = syscall.Recvmsg(int(fd), p, s.oob[:], syscall.MSG_CMSG_CLOEXEC|syscall.MSG_DONTWAIT)
		// This ordering is load-bearing: controls are disposed even with EOF/error.
		n, call = received(n, s.oob[:count], flags, call)
		return call != syscall.EAGAIN && call != syscall.EWOULDBLOCK
	})
	if err != nil {
		return 0, err
	}
	if call != nil {
		return 0, call
	}
	return n, nil
}
func (s *socketIO) readPart(p []byte, inside bool) error {
	done := 0
	for done < len(p) {
		n, err := s.receive(p[done:])
		if err != nil {
			return err
		}
		if n == 0 {
			if done > 0 || inside {
				return io.ErrUnexpectedEOF
			}
			return io.EOF
		}
		done += n
	}
	return nil
}
func (s *socketIO) readFrame() ([]byte, error) {
	deadline := stageDeadline(s.start, time.Now())
	if s.responseWait {
		deadline = s.start.Add(sessionBudget)
	}
	if err := s.file.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	if err := s.readPart(s.prefix[:], false); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(s.prefix[:])
	if size == 0 {
		return nil, frame.ErrEmptyFrame
	}
	if size > frame.MaxPayloadSize {
		return nil, frame.ErrFrameTooLarge
	}
	if s.body == nil {
		// One bounded backing buffer, allocated only after the first valid prefix.
		// Growing from hello to an operation must not retain a second payload buffer.
		s.body = make([]byte, frame.MaxPayloadSize)
	}
	s.body = s.body[:int(size)]
	if err := s.readPart(s.body, true); err != nil {
		return nil, err
	}
	return s.body, nil
}
func (s *socketIO) writeFrame(payload string) error {
	if len(payload) == 0 {
		return frame.ErrEmptyFrame
	}
	if len(payload)+4 > len(s.out) {
		return ErrLimit
	}
	if err := s.file.SetWriteDeadline(stageDeadline(s.start, time.Now())); err != nil {
		return err
	}
	binary.BigEndian.PutUint32(s.out[:4], uint32(len(payload)))
	copy(s.out[4:], payload)
	p := s.out[:4+len(payload)]
	for len(p) > 0 {
		var n int
		var call error
		err := s.raw.Write(func(fd uintptr) bool {
			n, call = syscall.SendmsgN(int(fd), p, nil, nil, syscall.MSG_NOSIGNAL|syscall.MSG_DONTWAIT)
			return call != syscall.EAGAIN && call != syscall.EWOULDBLOCK
		})
		if err != nil {
			return err
		}
		if call != nil {
			return call
		}
		if n <= 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func connectSocket(d *directory, start time.Time) (*os.File, error) {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "capacity connection")
	fail := func(e error) (*os.File, error) { wakeClose(f); return nil, e }
	if err = tuneSocket(f); err != nil {
		return fail(err)
	}
	if err = f.SetWriteDeadline(stageDeadline(start, start)); err != nil {
		return fail(err)
	}
	err = control(f, func(fd int) error {
		return syscall.Connect(fd, &syscall.SockaddrUnix{Name: descriptorPath(d, endpointName)})
	})
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, syscall.EINPROGRESS) {
		return fail(err)
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return fail(err)
	}
	var status error
	err = raw.Write(func(fd uintptr) bool {
		n, e := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_ERROR)
		if e != nil {
			status = e
			return true
		}
		if n != 0 {
			status = syscall.Errno(n)
			return true
		}
		// Check completion rather than treating an initial SO_ERROR=0 as connected.
		_, e = syscall.Getpeername(int(fd))
		if e == syscall.ENOTCONN {
			return false
		}
		status = e
		return true
	})
	if err != nil {
		return fail(err)
	}
	if status != nil {
		return fail(status)
	}
	return f, nil
}

// received centralizes ordering for the actual recvmsg result; deterministic
// error+control tests do not assert that Linux produces that combination.
func received(n int, controls []byte, flags int, call error) (int, error) {
	if err := disposeControls(controls, flags); err != nil {
		return 0, err
	}
	if call != nil {
		return 0, call
	}
	return n, nil
}
func verifyPeer(f *os.File, want uint32) error {
	uid, err := peerUID(f)
	if err != nil {
		return err
	}
	if uid != want {
		return ErrIdentity
	}
	return nil
}
