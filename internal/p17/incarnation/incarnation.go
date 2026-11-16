// Package incarnation records and compares kernel process incarnations (D2):
// pid, real uid, process start time and the boot ID. On Linux the facts come
// from /proc. A saved pid is never liveness evidence on its own; only an
// incarnation that still matches is.
package incarnation

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Incarnation is one kernel process incarnation.
type Incarnation struct {
	PID        int    `json:"pid"`
	UID        uint32 `json:"uid"`
	StartTicks uint64 `json:"start_ticks"`
	BootID     string `json:"boot_id"`
}

// Valid reports whether every field is set.
func (i Incarnation) Valid() bool {
	return i.PID > 0 && i.StartTicks > 0 && i.BootID != ""
}

// ErrGone means the process no longer exists.
var ErrGone = errors.New("incarnation: no such process")

// ErrUnreadable means the process exists but its facts could not be read.
var ErrUnreadable = errors.New("incarnation: process facts unreadable")

// Reader reads incarnations. Self reads the calling process.
type Reader interface {
	Read(pid int) (Incarnation, error)
	Self() (Incarnation, error)
}

// Proc reads from a procfs root; the empty root means "/proc".
type Proc struct {
	Root string
}

func (p Proc) root() string {
	if p.Root == "" {
		return "/proc"
	}
	return p.Root
}

// ReadUIDs returns the four fields of the Uid: line (real, effective, saved,
// filesystem) of /proc/<pid>/status under root. ErrGone when the process
// directory is absent; ErrUnreadable for any other failure.
func ReadUIDs(root string, pid int) ([4]uint32, error) {
	var out [4]uint32
	if pid <= 0 {
		return out, ErrUnreadable
	}
	dir := filepath.Join(root, strconv.Itoa(pid))
	b, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		if _, serr := os.Lstat(dir); serr != nil && errors.Is(serr, os.ErrNotExist) {
			return out, ErrGone
		}
		return out, errors.Join(ErrUnreadable, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line[len("Uid:"):])
		if len(fields) != 4 {
			return out, errors.Join(ErrUnreadable, errors.New("incarnation: Uid line shape"))
		}
		for i, f := range fields {
			v, err := strconv.ParseUint(f, 10, 32)
			if err != nil {
				return out, errors.Join(ErrUnreadable, err)
			}
			out[i] = uint32(v)
		}
		return out, nil
	}
	return out, errors.Join(ErrUnreadable, errors.New("incarnation: no Uid line"))
}

// startTicks parses field 22 (starttime) of /proc/<pid>/stat. The comm field
// may contain spaces and parentheses, so fields are split after the last ')'.
func startTicks(stat []byte) (uint64, error) {
	s := string(stat)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, errors.New("incarnation: stat shape")
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is field 3 (state); field 22 is index 19.
	if len(fields) < 20 {
		return 0, errors.New("incarnation: stat field count")
	}
	v, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, err
	}
	if v == 0 {
		return 0, errors.New("incarnation: zero start time")
	}
	return v, nil
}

// BootID reads the boot ID under root.
func (p Proc) BootID() (string, error) {
	b, err := os.ReadFile(filepath.Join(p.root(), "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return "", errors.Join(ErrUnreadable, err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.Join(ErrUnreadable, errors.New("incarnation: empty boot_id"))
	}
	return id, nil
}

// Read reads one incarnation. The real uid is recorded.
func (p Proc) Read(pid int) (Incarnation, error) {
	uids, err := ReadUIDs(p.root(), pid)
	if err != nil {
		return Incarnation{}, err
	}
	dir := filepath.Join(p.root(), strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		if _, serr := os.Lstat(dir); serr != nil && errors.Is(serr, os.ErrNotExist) {
			return Incarnation{}, ErrGone
		}
		return Incarnation{}, errors.Join(ErrUnreadable, err)
	}
	ticks, err := startTicks(stat)
	if err != nil {
		return Incarnation{}, errors.Join(ErrUnreadable, err)
	}
	boot, err := p.BootID()
	if err != nil {
		return Incarnation{}, err
	}
	return Incarnation{PID: pid, UID: uids[0], StartTicks: ticks, BootID: boot}, nil
}

// Self reads the calling process.
func (p Proc) Self() (Incarnation, error) { return p.Read(os.Getpid()) }

// Alive reports whether the recorded incarnation is still the live process
// with that pid. A missing process is dead with no error. An unreadable
// process is an error: liveness is then not determined, and callers must not
// treat that as dead.
func Alive(r Reader, rec Incarnation) (bool, error) {
	if r == nil {
		return false, ErrUnreadable
	}
	if !rec.Valid() {
		return false, errors.Join(ErrUnreadable, errors.New("incarnation: recorded incarnation incomplete"))
	}
	cur, err := r.Read(rec.PID)
	if err != nil {
		if errors.Is(err, ErrGone) {
			return false, nil
		}
		return false, err
	}
	return cur == rec, nil
}
