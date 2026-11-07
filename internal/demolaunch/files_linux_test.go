//go:build linux

package demolaunch

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Metadata controls exercise each protected snapshot predicate with root-owned
// synthetic attributes. Actual non-root files refuse without repair; this is
// explicitly not a positive root deployment or distinct-role qualification.
func TestSnapshotProtection(t *testing.T) {
	for _, mode := range []uint32{0440, 0444, 0644} {
		id := fileID{uid: 0, gid: 123, mode: syscall.S_IFREG | mode, links: 1}
		if !snapshotMetadata(id, mode, 123) {
			t.Fatal("valid metadata refused")
		}
		for _, change := range []func(*fileID){func(s *fileID) { s.uid = 1000 }, func(s *fileID) { s.gid = 124 }, func(s *fileID) { s.mode |= 0020 }, func(s *fileID) { s.mode = syscall.S_IFIFO | mode }, func(s *fileID) { s.links = 2 }} {
			bad := id
			change(&bad)
			if snapshotMetadata(bad, mode, 123) {
				t.Fatal("bad snapshot metadata admitted", bad)
			}
		}
	}
	root := fixtureDir(t)
	name := filepath.Join(root, "config.json")
	content := marshal(cfgFixture())
	if err := os.WriteFile(name, content, 0440); err != nil {
		t.Fatal(err)
	}
	if b, err := sourceSnapshot(name, true); b != nil || err == nil {
		t.Fatal("unprotected config accepted")
	}
	after, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(after, content) {
		t.Fatal("config modified", err)
	}
}

// State ownership/mode, exclusive create, file+directory sync order and immutable
// names are checked using genuine local files; failed modes are never repaired.
func TestStateProtectionAndSyncOrder(t *testing.T) {
	root := fixtureDir(t)
	s, err := openState(root, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.dir.Close()
	var kinds []bool
	real := s.sync
	s.sync = func(f *os.File) error {
		info, e := f.Stat()
		if e != nil {
			return e
		}
		kinds = append(kinds, info.IsDir())
		return real(f)
	}
	if err = s.create("probe", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kinds[0] || !kinds[1] {
		t.Fatal("not file then directory", kinds)
	}
	if err = s.create("probe", []byte("second")); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	b, err := s.read("probe")
	if err != nil || string(b) != "first" {
		t.Fatal(string(b), err)
	}
	if err = os.Chmod(root, 0750); err != nil {
		t.Fatal(err)
	}
	if other, err := openState(root, uint32(os.Geteuid())); other != nil || !errors.Is(err, ErrOwnership) {
		if other != nil {
			other.dir.Close()
		}
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != 0750 {
		t.Fatal("state repaired")
	}
	if other, err := openState(root, uint32(os.Geteuid())+1); other != nil || err == nil {
		if other != nil {
			other.dir.Close()
		}
		t.Fatal("wrong owner")
	}
}

// A valid receipt's literal identities pass; extra/missing/duplicate/trailing,
// oversized and wrong-incarnation records cannot become receipt-seen.
func TestReceiptContract(t *testing.T) {
	execution := "EX-0123456789abcdef0123456789abcdef"
	good := `{"execution_id":"` + execution + `","epoch":1,"pid":12,"start_time":33,"boot_id":"` + boot + `","session_id":12}`
	if r, err := parseReceipt([]byte(good), execution); err != nil || r.PID != 12 {
		t.Fatal(r, err)
	}
	for _, bad := range []string{good + `{}`, strings.Replace(good, `"pid":12`, `"pid":12,"pid":12`, 1), strings.Replace(good, `"epoch":1`, `"epoch":2`, 1), strings.Replace(good, `"pid":12`, `"extra":12`, 1), strings.Replace(good, `"pid":12`, `"pid":0`, 1), strings.Repeat(" ", 4096) + good} {
		if _, err := parseReceipt([]byte(bad), execution); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
	if _, err := parseReceipt([]byte(good), "EX-ffffffffffffffffffffffffffffffff"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	h := fixture(t, "good")
	h.e.receiptRoot = h.root
	h.e.receiptOwner = uint32(os.Geteuid())
	dir := filepath.Join(h.root, gen)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, execution+".json")
	// The root-owned production receipt predicate rejects current-UID artifacts
	// with the wrong group/mode; a symlink cannot turn its target into evidence.
	target := filepath.Join(h.root, "target")
	if err := os.WriteFile(target, []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, file); err != nil {
		t.Fatal(err)
	}
	if b, err := h.e.receiptBytes(h.e.accounts["demo"], execution); err == nil || b != nil {
		t.Fatal("followed receipt link")
	}
}

var forbiddenCalls = map[string]bool{
	"Remove": true, "RemoveAll": true, "Rename": true, "Renameat": true, "Renameat2": true,
	"Rmdir": true, "Unlink": true, "Unlinkat": true, "Truncate": true, "Ftruncate": true,
	"WriteFile": true, "Create": true, "Creat": true, "Chmod": true, "Fchmod": true, "Link": true, "Symlink": true, "Mknod": true, "CommandContext": true, "Pdeathsig": true, "Syscall": true,
	"O_TRUNC": true, "Kill": true, "Signal": true, "Tkill": true, "Tgkill": true, "PidfdSendSignal": true,
}

func destructiveCalls(name string, src []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, decl := range f.Decls {
		fn, _ := decl.(*ast.FuncDecl)
		ast.Inspect(decl, func(n ast.Node) bool {
			if c, ok := n.(*ast.ChanType); ok {
				if s, ok := c.Value.(*ast.SelectorExpr); ok && s.Sel.Name == "Signal" {
					if p, ok := s.X.(*ast.Ident); ok && p.Name == "os" {
						return false
					}
				}
			}
			id, ok := n.(*ast.Ident)
			if !ok || !forbiddenCalls[id.Name] {
				return true
			}
			// The sole exception is the named socket cleanup function, not its file.
			if name == "endpoint_linux.go" && fn != nil && fn.Name.Name == "removeOwned" && id.Name == "Unlinkat" {
				return true
			}
			found = append(found, id.Name)
			return true
		})
	}
	return found, nil
}

// Scan every non-test demo source, including new files. Individual plants
// defend every forbidden spelling and the exact socket-cleanup exception.
// This lexical AST guard is not an interprocedural or raw-syscall proof.
func TestStateMutationChecker(t *testing.T) {
	names, err := filepath.Glob("*.go")
	must(t, err)
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		must(t, err)
		found, err := destructiveCalls(name, b)
		if err != nil || len(found) != 0 {
			t.Fatal(name, found, err)
		}
	}
	for name := range forbiddenCalls {
		for _, file := range []string{"engine_linux.go", "endpoint_linux.go", "future.go"} {
			found, err := destructiveCalls(file, []byte("package p;func plant(){_ = os."+name+"}"))
			if err != nil || len(found) != 1 || found[0] != name {
				t.Fatal("missed plant", file, name, found, err)
			}
		}
	}
	for _, tc := range []struct {
		file, fn string
		want     int
	}{{"endpoint_linux.go", "removeOwned", 0}, {"endpoint_linux.go", "other", 1}, {"state_linux.go", "removeOwned", 1}} {
		found, err := destructiveCalls(tc.file, []byte("package p;func "+tc.fn+"(){syscall.Unlinkat(1,endpointName)}"))
		if err != nil || len(found) != tc.want {
			t.Fatal(tc, found, err)
		}
	}
	found, err := destructiveCalls("valid.go", []byte(`package p;func f(){os.Open("x")}`))
	if err != nil || len(found) != 0 {
		t.Fatal("positive control", found, err)
	}
}

// Fixed command construction has exactly sudo -n and one compiled path, an
// empty environment, no shell or credential inheritance; this never executes sudo.
func TestFixedRunnerCommand(t *testing.T) {
	for _, path := range []string{launcherPath, managerPath} {
		c := fixedRunner(path)
		if c.Path != "/usr/bin/sudo" || len(c.Args) != 3 || c.Args[0] != "/usr/bin/sudo" || c.Args[1] != "-n" || c.Args[2] != path || c.Env == nil || len(c.Env) != 0 || c.Dir != "/" || (c.SysProcAttr != nil && c.SysProcAttr.Pdeathsig != 0) {
			t.Fatal("command changed", c.Path, c.Args, c.Env, c.Dir)
		}
	}
}

// An expired original budget permits safe narrowing but never timely. A successful
// first stop and reaped launch permit the next roster generation sequentially.
func TestExpiredStopAndSequentialLaunch(t *testing.T) {
	h := fixture(t, "good")
	assertState(t, h.e.handle(launchBytes("demo")), "receipt-seen", "")
	assertState(t, h.e.handle(stopBytes(10)), "absent", "")
	waitChild(t, h.e.launchChildren["demo"], time.Now().Add(30*time.Second))
	assertState(t, h.e.handle(launchBytes("second")), "receipt-seen", "")
	other := fixture(t, "good")
	assertState(t, other.e.handle(launchBytes("demo")), "receipt-seen", "")
	other.mode = "remaining"
	assertState(t, other.e.handle(stopBytes(10)), "unresolved", "processes_remain")
	other.e.stops["demo"].deadline = time.Now().Add(-time.Second)
	other.mode = "good"
	calls := other.calls.Load()
	r := other.e.handle(stopBytes(10))
	assertState(t, r, "absent", "")
	if r.Timing != "missed" || other.calls.Load() != calls+1 {
		t.Fatal("deadline refilled", r)
	}
}

func boundaryReferences(src []byte, function string, required []string) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "boundary.go", src, 0)
	if err != nil {
		return false, err
	}
	found := map[string]bool{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == function {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					found[id.Name] = true
				}
				return true
			})
		}
	}
	for _, name := range required {
		if !found[name] {
			return false, nil
		}
	}
	return true, nil
}

// Non-root composed tests cannot execute distinct-UID deployment. This narrow
// source guard additionally detects deletion of entrypoint boundary wiring; each
// required reference has a known omission control. It does not prove execution
// order or replace root-owned-config, sudo and distinct-user qualification.
func TestEntrypointBoundaryWiring(t *testing.T) {
	b, err := os.ReadFile("service_linux.go")
	must(t, err)
	for fn, required := range map[string][]string{
		"Daemon":          {"loadServerInputs", "protectedSnapshot", "runtimeConfig", "principalPin", "endpointPin", "createEndpointCalls", "openState", "newEngine", "serve"},
		"Client":          {"clientWith", "loadClientInput", "runtimeConfig", "endpointPin"},
		"loadClientInput": {"sourceSnapshot", "parseClient"},
		"clientWith":      {"parseRequest", "principalPin", "socketIdentity", "verifyPeer", "clientExchange"},
	} {
		ok, err := boundaryReferences(b, fn, required)
		if err != nil || !ok {
			t.Fatal(fn, err)
		}
		for _, name := range required {
			plant := bytes.ReplaceAll(b, []byte(name), []byte("omittedBoundary"))
			ok, err := boundaryReferences(plant, fn, required)
			if err != nil || ok {
				t.Fatal("wiring omission plant missed", fn, name, err)
			}
		}
	}
}
