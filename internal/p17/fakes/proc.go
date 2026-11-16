package fakes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
)

// ProcTree is a fake procfs root for enumeration and incarnation tests.
type ProcTree struct {
	Root string
}

// NewProcTree creates the tree with a boot ID.
func NewProcTree(root, bootID string) (*ProcTree, error) {
	if err := os.MkdirAll(filepath.Join(root, "sys", "kernel", "random"), 0o700); err != nil {
		return nil, err
	}
	p := &ProcTree{Root: root}
	return p, p.SetBootID(bootID)
}

// SetBootID rewrites the boot ID.
func (p *ProcTree) SetBootID(id string) error {
	return os.WriteFile(filepath.Join(p.Root, "sys", "kernel", "random", "boot_id"), []byte(id+"\n"), 0o600)
}

// Add writes <pid>/status with the four Uid fields and <pid>/stat with the
// given start time at field 22. comm contains spaces and a parenthesis so the
// stat parser is exercised.
func (p *ProcTree) Add(pid int, uids [4]uint32, start uint64) error {
	dir := filepath.Join(p.Root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	status := fmt.Sprintf("Name:\tfake (proc)\nUmask:\t0022\nState:\tS (sleeping)\nPid:\t%d\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\n", pid, uids[0], uids[1], uids[2], uids[3], uids[0], uids[1], uids[2], uids[3])
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
		return err
	}
	stat := fmt.Sprintf("%d (fake (proc) name) S 1 %d %d 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 %d 0 0 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n", pid, pid, pid, start)
	return os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600)
}

// Remove deletes a process.
func (p *ProcTree) Remove(pid int) error {
	return os.RemoveAll(filepath.Join(p.Root, strconv.Itoa(pid)))
}

// MakeUnreadable strips read permission from <pid>/status. For a non-root
// test process this makes the pid unreadable.
func (p *ProcTree) MakeUnreadable(pid int) error {
	return os.Chmod(filepath.Join(p.Root, strconv.Itoa(pid), "status"), 0)
}

// KillUID removes every process whose real, effective, saved or filesystem
// uid equals uid, as the real helper's kill(-1) would. It never looks at a
// pid list supplied by a caller.
func (p *ProcTree) KillUID(uid uint32) (int, error) {
	entries, err := os.ReadDir(p.Root)
	if err != nil {
		return 0, err
	}
	killed := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		uids, err := incarnation.ReadUIDs(p.Root, pid)
		if err != nil {
			continue
		}
		if uids[0] == uid || uids[1] == uid || uids[2] == uid || uids[3] == uid {
			if err := p.Remove(pid); err != nil {
				return killed, err
			}
			killed++
		}
	}
	return killed, nil
}

// Killer is a sweep.Killer that kills in a ProcTree and records every uid it
// was asked for. Fail makes it refuse. Immortal leaves processes in place.
type Killer struct {
	Tree     *ProcTree
	Fail     error
	Immortal bool

	mu   sync.Mutex
	uids []uint32
}

// KillAll implements sweep.Killer.
func (k *Killer) KillAll(_ context.Context, uid uint32) error {
	k.mu.Lock()
	k.uids = append(k.uids, uid)
	k.mu.Unlock()
	if k.Fail != nil {
		return k.Fail
	}
	if k.Immortal || k.Tree == nil {
		return nil
	}
	_, err := k.Tree.KillUID(uid)
	return err
}

// UIDs returns the uids KillAll was called with.
func (k *Killer) UIDs() []uint32 {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]uint32(nil), k.uids...)
}

// Incarnations is an incarnation.Reader over a ProcTree.
func (p *ProcTree) Incarnations() incarnation.Reader { return incarnation.Proc{Root: p.Root} }
