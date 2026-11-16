package sweep_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// This file is self-contained on purpose: it builds its proc tree and
// mountinfo by hand and calls Enumerate through reflection, so the same file
// is the control that fails on the revision before the view qualification
// (where Enumerate took only a procfs root) and passes with it.

func controlTree(t *testing.T, superOptions string) (root, mountinfo string) {
	t.Helper()
	root = t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// What a non-root launcher sees: pid 1 and itself. With hidepid=2 the
	// processes of R_i are absent from this listing, not unreadable.
	write("1/status", "Name:\tinit\nPid:\t1\nUid:\t0\t0\t0\t0\n")
	write("4242/status", "Name:\tlauncher\nPid:\t4242\nUid:\t1000\t1000\t1000\t1000\n")
	write("self/status", "Name:\tlauncher\nPid:\t4242\nNSpid:\t4242\n")
	write("self/mountinfo", fmt.Sprintf("21 1 8:1 / / rw - ext4 /dev/root rw\n22 21 0:21 / %s rw,nosuid,nodev,noexec - proc proc %s\n", root, superOptions))
	return root, filepath.Join(root, "self", "mountinfo")
}

// enumerate calls sweep.Enumerate whichever first parameter this revision
// has: a procfs root string, or a view struct with ProcRoot and MountInfo.
func enumerate(t *testing.T, root, mountinfo string, uid uint32) sweep.Enumeration {
	t.Helper()
	fn := reflect.ValueOf(sweep.Enumerate)
	first := fn.Type().In(0)
	var arg reflect.Value
	if first.Kind() == reflect.String {
		arg = reflect.ValueOf(root)
	} else {
		arg = reflect.New(first).Elem()
		arg.FieldByName("ProcRoot").SetString(root)
		arg.FieldByName("MountInfo").SetString(mountinfo)
	}
	return fn.Call([]reflect.Value{arg, reflect.ValueOf(uid)})[0].Interface().(sweep.Enumeration)
}

// Claim (procfs view, control across revisions): a listing that shows no
// process under R_i is NOT a zero-process proof when the procfs is mounted
// with hidepid=2 (R_i's pids are hidden from the non-root launcher) or with
// subset=pid (refused conservatively, though kernel documentation says pid
// directories stay visible); the same listing with hidepid=0 is proved.
func TestHiddenPidsAreNotAProof(t *testing.T) {
	for _, opts := range []string{"rw,hidepid=2", "rw,hidepid=invisible", "rw,subset=pid"} {
		root, mi := controlTree(t, opts)
		if e := enumerate(t, root, mi, ri); e.Proved() {
			t.Errorf("%s: an unqualified or conservatively refused view was reported as proved", opts)
		}
	}
	root, mi := controlTree(t, "rw,hidepid=0")
	if e := enumerate(t, root, mi, ri); !e.Proved() {
		t.Errorf("qualifying neighbour not proved: %+v", e)
	}
}
