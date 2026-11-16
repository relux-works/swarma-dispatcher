package incarnation_test

import (
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
)

func tree(t *testing.T) *fakes.ProcTree {
	t.Helper()
	p, err := fakes.NewProcTree(t.TempDir(), "boot-1")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Claim (D2): Read returns pid, REAL uid, the stat start time (field 22, with
// a comm containing spaces and a parenthesis) and the boot ID.
func TestReadFakeProcess(t *testing.T) {
	p := tree(t)
	if err := p.Add(4242, [4]uint32{2001, 0, 2001, 0}, 777); err != nil {
		t.Fatal(err)
	}
	got, err := p.Incarnations().Read(4242)
	want := incarnation.Incarnation{PID: 4242, UID: 2001, StartTicks: 777, BootID: "boot-1"}
	if err != nil || got != want || !got.Valid() {
		t.Fatalf("got %+v err %v", got, err)
	}
}

// Claim (D2 liveness): a recorded incarnation is alive only while pid, uid,
// start time and boot ID all still match; a reused pid with another start
// time, another uid or after a reboot is dead, and an absent pid is dead with
// no error.
func TestAlive(t *testing.T) {
	p := tree(t)
	if err := p.Add(100, [4]uint32{2001, 2001, 2001, 2001}, 500); err != nil {
		t.Fatal(err)
	}
	rec := incarnation.Incarnation{PID: 100, UID: 2001, StartTicks: 500, BootID: "boot-1"}
	if alive, err := incarnation.Alive(p.Incarnations(), rec); err != nil || !alive {
		t.Fatalf("positive control: alive=%v err=%v", alive, err)
	}
	// Recycled pid: same pid, later start.
	if err := p.Add(100, [4]uint32{2001, 2001, 2001, 2001}, 501); err != nil {
		t.Fatal(err)
	}
	if alive, err := incarnation.Alive(p.Incarnations(), rec); err != nil || alive {
		t.Fatalf("recycled pid counted alive: %v %v", alive, err)
	}
	// Recycled pid under another uid.
	if err := p.Add(100, [4]uint32{3001, 3001, 3001, 3001}, 500); err != nil {
		t.Fatal(err)
	}
	if alive, err := incarnation.Alive(p.Incarnations(), rec); err != nil || alive {
		t.Fatalf("other uid counted alive: %v %v", alive, err)
	}
	// Same everything, different boot.
	if err := p.Add(100, [4]uint32{2001, 2001, 2001, 2001}, 500); err != nil {
		t.Fatal(err)
	}
	if err := p.SetBootID("boot-2"); err != nil {
		t.Fatal(err)
	}
	if alive, err := incarnation.Alive(p.Incarnations(), rec); err != nil || alive {
		t.Fatalf("other boot counted alive: %v %v", alive, err)
	}
	if err := p.Remove(100); err != nil {
		t.Fatal(err)
	}
	if alive, err := incarnation.Alive(p.Incarnations(), rec); err != nil || alive {
		t.Fatalf("absent pid: alive=%v err=%v", alive, err)
	}
}

// Claim (fail closed): an unreadable live pid is an error, not "dead"; an
// incomplete recorded incarnation is an error too.
func TestAliveUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file; unreadable plant needs a non-root test process")
	}
	p := tree(t)
	if err := p.Add(100, [4]uint32{2001, 2001, 2001, 2001}, 500); err != nil {
		t.Fatal(err)
	}
	if err := p.MakeUnreadable(100); err != nil {
		t.Fatal(err)
	}
	rec := incarnation.Incarnation{PID: 100, UID: 2001, StartTicks: 500, BootID: "boot-1"}
	if alive, err := incarnation.Alive(p.Incarnations(), rec); err == nil || alive || !errors.Is(err, incarnation.ErrUnreadable) {
		t.Fatalf("unreadable: alive=%v err=%v", alive, err)
	}
	if _, err := incarnation.Alive(p.Incarnations(), incarnation.Incarnation{PID: 100}); err == nil {
		t.Fatal("incomplete record accepted")
	}
}

// Claim (D5a Uid line): ReadUIDs returns all four Uid fields; a missing
// process is ErrGone and a malformed Uid line is ErrUnreadable.
func TestReadUIDs(t *testing.T) {
	p := tree(t)
	if err := p.Add(7, [4]uint32{1, 2, 3, 4}, 9); err != nil {
		t.Fatal(err)
	}
	uids, err := incarnation.ReadUIDs(p.Root, 7)
	if err != nil || uids != [4]uint32{1, 2, 3, 4} {
		t.Fatalf("%v %v", uids, err)
	}
	if _, err := incarnation.ReadUIDs(p.Root, 8); !errors.Is(err, incarnation.ErrGone) {
		t.Fatalf("absent pid: %v", err)
	}
	if err := os.WriteFile(p.Root+"/7/status", []byte("Uid:\t1\t2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := incarnation.ReadUIDs(p.Root, 7); !errors.Is(err, incarnation.ErrUnreadable) {
		t.Fatalf("malformed Uid line: %v", err)
	}
}

// Claim (positive control on the real procfs): the test process reads its own
// incarnation with its real uid, a nonzero start time and a boot ID, and is
// alive by its own record.
func TestSelfOnProc(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs is Linux only")
	}
	self, err := incarnation.Proc{}.Self()
	if err != nil || self.PID != os.Getpid() || self.UID != uint32(os.Getuid()) || !self.Valid() {
		t.Fatalf("%+v %v", self, err)
	}
	if alive, err := incarnation.Alive(incarnation.Proc{}, self); err != nil || !alive {
		t.Fatalf("self not alive: %v %v", alive, err)
	}
}
