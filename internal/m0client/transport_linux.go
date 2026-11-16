//go:build linux

package m0client

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	oPath           = 0x200000 // Linux O_PATH; the frozen syscall package omits it on some architectures.
	maxFramePayload = 131072
	socketBuffer    = 65536
	maxSocketBuffer = 131072
	ancillaryBytes  = 4096

	stagePins    = "endpoint_pin_refused"
	stageConnect = "connect_failed"
	stagePeer    = "service_uid_refused"
	stageHello   = "hello_failed"
	stageRequest = "request_write_failed"
	stageReply   = "reply_missing"
)

var (
	errIdentity  = errors.New("endpoint identity differs from the pins")
	errPeer      = errors.New("kernel peer is not the configured service UID")
	errAncillary = errors.New("ancillary data refused")
	errFrame     = errors.New("frame length outside 1..131072")
	errHello     = errors.New("hello result is not the negotiated template")
	errBuffer    = errors.New("socket buffer outside the transport limit")
)

// endpointSpec is one client's transport pins. owner is the UID that must own
// the endpoint directory and socket; peer is the UID SO_PEERCRED must report.
// The tool sets both to the configured service_uid; they are separate members
// only so each check can be exercised on its own. connected is nil outside
// tests; when set it runs once between connect and the re-stat.
type endpointSpec struct {
	dir, name      string
	owner, peer    uint32
	gid            uint32
	stage, session time.Duration
	connected      func()
	dev, ino       uint64
	serviceGID     uint32
	caller         Principal
	host           host
}

type ident struct {
	dev, ino       uint64
	uid, gid, mode uint32
	links          uint64
}

func identity(st syscall.Stat_t) ident {
	return ident{uint64(st.Dev), st.Ino, st.Uid, st.Gid, st.Mode, uint64(st.Nlink)}
}

func (h host) identOf(fd int) (ident, error) {
	st, err := h.fstat(fd)
	return identity(st), err
}

// pinned is the endpoint directory held open through every ancestor.
type pinned struct {
	fds  []int
	ids  []ident
	host host
}

func (p *pinned) close() {
	for _, fd := range p.fds {
		_ = syscall.Close(fd)
	}
	p.fds = nil
}

func (p *pinned) last() int { return p.fds[len(p.fds)-1] }

// pinDirectory opens every component without symlinks and pins its identity.
// Ancestors are root/pin-owned and not group/other-writable. Callers check
// each leaf's exact role-specific owner, group, mode and configured identity.
func (h host) pinDirectory(name string, owner uint32) (_ *pinned, err error) {
	if name != "/" && !validAbsolute(name) {
		return nil, errIdentity
	}
	p := &pinned{host: h}
	defer func() {
		if err != nil {
			p.close()
		}
	}()
	fd, err := syscall.Open("/", oPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	p.fds = append(p.fds, fd)
	var parts []string
	if name != "/" {
		parts = strings.Split(strings.TrimPrefix(name, "/"), "/")
	}
	for i := 0; ; i++ {
		id, err := h.identOf(p.last())
		if err != nil {
			return nil, err
		}
		if id.mode&syscall.S_IFMT != syscall.S_IFDIR || (id.uid != 0 && id.uid != owner) || id.mode&0022 != 0 {
			return nil, errIdentity
		}
		p.ids = append(p.ids, id)
		if i == len(parts) {
			break
		}
		fd, err := syscall.Openat(p.last(), parts[i], oPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		p.fds = append(p.fds, fd)
	}
	return p, nil
}

// recheck requires that the path still names the same pinned identities.
func (p *pinned) recheck(name string, owner uint32) error {
	now, err := p.host.pinDirectory(name, owner)
	if err != nil {
		return err
	}
	defer now.close()
	if len(now.ids) != len(p.ids) {
		return errIdentity
	}
	for i, id := range p.ids {
		if id != now.ids[i] {
			return errIdentity
		}
	}
	return nil
}

func (p *pinned) stat(name string) (ident, error) {
	fd, err := syscall.Openat(p.last(), name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return ident{}, err
	}
	defer syscall.Close(fd)
	return p.host.identOf(fd)
}

func (c *Config) callerPin() (*pinned, error) {
	return pinCaller(c.host, c.Caller)
}

func pinCaller(h host, caller Principal) (*pinned, error) {
	p, err := h.pinDirectory(caller.Dir, caller.UID)
	if err != nil {
		return nil, err
	}
	leaf := p.ids[len(p.ids)-1]
	if leaf.dev != caller.Dev || leaf.ino != caller.Ino || leaf.uid != caller.UID || leaf.mode&permMask != 0700 {
		p.close()
		return nil, errIdentity
	}
	return p, nil
}

func groupMember(gid uint32) bool {
	if uint32(os.Getegid()) == gid {
		return true
	}
	groups, err := os.Getgroups()
	if err != nil {
		return false
	}
	for _, g := range groups {
		if uint32(g) == gid {
			return true
		}
	}
	return false
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

func wakeClose(f *os.File) {
	_ = f.SetDeadline(time.Now())
	_ = control(f, func(fd int) error { return syscall.Shutdown(fd, syscall.SHUT_RDWR) })
	_ = f.Close()
}

// closeRights closes every complete SCM_RIGHTS descriptor in one returned
// control buffer, including those before a malformed or truncated tail.
func closeRights(b []byte) {
	header := syscall.CmsgLen(0)
	wide := unsafe.Sizeof(syscall.Cmsghdr{}.Len) == 8
	word := 4
	if wide {
		word = 8
	}
	for len(b) >= header {
		var length uint64
		if wide {
			length = binary.NativeEndian.Uint64(b)
		} else {
			length = uint64(binary.NativeEndian.Uint32(b))
		}
		level := int32(binary.NativeEndian.Uint32(b[word:]))
		kind := int32(binary.NativeEndian.Uint32(b[word+4:]))
		if length < uint64(header) {
			return
		}
		end := uint64(len(b))
		if length < end {
			end = length
		}
		if level == syscall.SOL_SOCKET && kind == syscall.SCM_RIGHTS {
			for data := b[header:int(end)]; len(data) >= 4; data = data[4:] {
				if fd := int(int32(binary.NativeEndian.Uint32(data))); fd >= 0 {
					_ = syscall.Close(fd)
				}
			}
		}
		if length > uint64(len(b)) {
			return
		}
		step := syscall.CmsgSpace(int(length) - header)
		if step <= 0 || step > len(b) {
			return
		}
		b = b[step:]
	}
}

type wire struct {
	file   *os.File
	raw    syscall.RawConn
	start  time.Time
	spec   endpointSpec
	sent   int
	prefix [4]byte
	oob    [ancillaryBytes]byte
}

// deadline is min(stage start + stage budget, session start + session budget).
func (w *wire) deadline() time.Time {
	d := time.Now().Add(w.spec.stage)
	if end := w.start.Add(w.spec.session); end.Before(d) {
		return end
	}
	return d
}

// receive is one nonblocking recvmsg. Returned controls and flags are
// inspected and disposed before the data, end of file or an error is
// considered; any control data or flag other than MSG_CMSG_CLOEXEC refuses.
func (w *wire) receive(p []byte) (int, error) {
	var n int
	var call error
	err := w.raw.Read(func(fd uintptr) bool {
		clear(w.oob[:])
		var count, flags int
		n, count, flags, _, call = syscall.Recvmsg(int(fd), p, w.oob[:], syscall.MSG_CMSG_CLOEXEC|syscall.MSG_DONTWAIT)
		if count > 0 {
			closeRights(w.oob[:count])
		}
		if count > 0 || flags&^syscall.MSG_CMSG_CLOEXEC != 0 {
			n, call = 0, errAncillary
		}
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

func (w *wire) readFull(p []byte, inside bool) error {
	done := 0
	for done < len(p) {
		n, err := w.receive(p[done:])
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

// readFrame reads one frame within one stage deadline. The length is checked
// before the payload buffer exists; end of file anywhere is a failure because
// a reply is expected.
func (w *wire) readFrame() ([]byte, error) {
	if err := w.file.SetReadDeadline(w.deadline()); err != nil {
		return nil, err
	}
	if err := w.readFull(w.prefix[:], false); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(w.prefix[:])
	if size == 0 || size > maxFramePayload {
		return nil, errFrame
	}
	body := make([]byte, size)
	if err := w.readFull(body, true); err != nil {
		return nil, err
	}
	return body, nil
}

// writeFrame writes one whole frame within one stage deadline and counts
// every byte the kernel accepted.
func (w *wire) writeFrame(payload []byte) error {
	if len(payload) == 0 || len(payload) > maxFramePayload {
		return errFrame
	}
	if err := w.file.SetWriteDeadline(w.deadline()); err != nil {
		return err
	}
	p := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(p, uint32(len(payload)))
	copy(p[4:], payload)
	for len(p) > 0 {
		var n int
		var call error
		err := w.raw.Write(func(fd uintptr) bool {
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
		w.sent += n
		p = p[n:]
	}
	return nil
}

// connect creates the client socket and connects through the pinned directory
// descriptor, so the path cannot be redirected after the pin.
func connect(dir *pinned, spec endpointSpec, start time.Time) (*os.File, error) {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "m0 caller connection")
	fail := func(e error) (*os.File, error) { wakeClose(f); return nil, e }
	for _, option := range []int{syscall.SO_RCVBUF, syscall.SO_SNDBUF} {
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, option, socketBuffer); err != nil {
			return fail(err)
		}
		n, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, option)
		if err != nil {
			return fail(err)
		}
		if n <= 0 || n > maxSocketBuffer {
			return fail(errBuffer)
		}
	}
	deadline := start.Add(spec.stage)
	if end := start.Add(spec.session); end.Before(deadline) {
		deadline = end
	}
	if err := f.SetWriteDeadline(deadline); err != nil {
		return fail(err)
	}
	target := "/proc/self/fd/" + strconv.Itoa(dir.last()) + "/" + spec.name
	err = control(f, func(fd int) error { return syscall.Connect(fd, &syscall.SockaddrUnix{Name: target}) })
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
		if _, e = syscall.Getpeername(int(fd)); e == syscall.ENOTCONN {
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

// exchange is the whole client state machine of the transport contract for one
// request: pin, connect, re-stat, verify the kernel peer, hello, one request
// frame whose correlation ID is id, one reply, close. There is no retry. sent
// counts the bytes written to the socket; stage names the step that failed.
func exchange(spec endpointSpec, id string, request []byte) (reply []byte, sent int, stage string, err error) {
	if !groupMember(spec.gid) || groupMember(spec.serviceGID) || spec.host.realUID() != spec.caller.UID || spec.host.effectiveUID() != spec.caller.UID {
		return nil, 0, stagePins, errIdentity
	}
	caller, err := pinCaller(spec.host, spec.caller)
	if err != nil {
		return nil, 0, stagePins, err
	}
	defer caller.close()
	dir, err := spec.host.pinDirectory(spec.dir, spec.owner)
	if err != nil {
		return nil, 0, stagePins, err
	}
	defer dir.close()
	leaf := dir.ids[len(dir.ids)-1]
	if leaf.dev != spec.dev || leaf.ino != spec.ino || leaf.uid != spec.owner || leaf.gid != spec.serviceGID || leaf.mode&permMask != 0711 {
		return nil, 0, stagePins, errIdentity
	}
	before, err := dir.stat(spec.name)
	if err != nil {
		return nil, 0, stagePins, err
	}
	if before.mode&syscall.S_IFMT != syscall.S_IFSOCK || before.uid != spec.owner || before.gid != spec.gid || before.mode&permMask != 0660 || before.links != 1 {
		return nil, 0, stagePins, errIdentity
	}
	start := time.Now()
	f, err := connect(dir, spec, start)
	if err != nil {
		return nil, 0, stageConnect, err
	}
	defer wakeClose(f)
	if spec.connected != nil {
		spec.connected()
	}
	if err = caller.recheck(spec.caller.Dir, spec.caller.UID); err != nil {
		return nil, 0, stagePins, err
	}
	if err = dir.recheck(spec.dir, spec.owner); err != nil {
		return nil, 0, stagePins, err
	}
	after, err := dir.stat(spec.name)
	if err != nil {
		return nil, 0, stagePins, err
	}
	if after != before {
		return nil, 0, stagePins, errIdentity
	}
	uid, err := peerUID(f)
	if err != nil {
		return nil, 0, stagePeer, err
	}
	if uid != spec.peer {
		return nil, 0, stagePeer, errPeer
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return nil, 0, stageConnect, err
	}
	w := &wire{file: f, raw: raw, start: start, spec: spec}
	if err = w.writeFrame([]byte(helloFrame(id))); err != nil {
		return nil, w.sent, stageHello, err
	}
	hello, err := w.readFrame()
	if err != nil {
		return nil, w.sent, stageHello, err
	}
	if string(hello) != helloResult(id) {
		return nil, w.sent, stageHello, errHello
	}
	if err = w.writeFrame(request); err != nil {
		return nil, w.sent, stageRequest, err
	}
	reply, err = w.readFrame()
	if err != nil {
		return nil, w.sent, stageReply, err
	}
	return reply, w.sent, "", nil
}
