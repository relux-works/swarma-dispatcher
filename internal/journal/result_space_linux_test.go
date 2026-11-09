//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// Source-derived selector coverage keeps the errno sweep coupled to branching
// sites, not a second handwritten branch list. A new unknown errno fails closed
// until its numeric binding is supplied here. Controls add ordinary I/O failures.
//
//go:embed session_linux.go sys_linux.go
var recoverySource embed.FS

func recoveryBranchErrnos(t *testing.T) []syscall.Errno {
	t.Helper()
	bindings := map[string]syscall.Errno{
		"EIO": syscall.EIO, "EINVAL": syscall.EINVAL, "ENOLCK": syscall.ENOLCK,
		"ENOSYS": syscall.ENOSYS, "EOPNOTSUPP": syscall.EOPNOTSUPP, "ENOENT": syscall.ENOENT,
		"EEXIST": syscall.EEXIST, "ELOOP": syscall.ELOOP, "ENOTDIR": syscall.ENOTDIR,
		"EACCES": syscall.EACCES, "EISDIR": syscall.EISDIR, "ENXIO": syscall.ENXIO,
		"ENOSPC": syscall.ENOSPC, "EROFS": syscall.EROFS, "EINTR": syscall.EINTR,
	}
	seen := map[syscall.Errno]bool{syscall.EIO: true, syscall.ENOSPC: true, syscall.EROFS: true}
	for _, file := range []string{"session_linux.go", "sys_linux.go"} {
		b, err := recoverySource.ReadFile(file)
		must(t, err)
		tree, err := parser.ParseFile(token.NewFileSet(), file, b, 0)
		must(t, err)
		for _, decl := range tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			switch fn.Name.Name {
			case "open", "openError", "unsupported", "fullWrite", "replace", "readInputBudget", "check", "syncFD", "closeFD":
			default:
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "syscall" || !strings.HasPrefix(sel.Sel.Name, "E") {
					return true
				}
				value, ok := bindings[sel.Sel.Name]
				if !ok {
					t.Fatalf("RESULT-SPACE: unbound production errno %s", sel.Sel.Name)
				}
				seen[value] = true
				return true
			})
		}
	}
	result := make([]syscall.Errno, 0, len(seen))
	for e := range seen {
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// Discover checked stat fields from the production predicates. Unknown fields
// fail closed rather than silently disappearing from the perturbation sweep.
func recoveryStatVariants(t *testing.T) []string {
	t.Helper()
	b, err := recoverySource.ReadFile("session_linux.go")
	must(t, err)
	tree, err := parser.ParseFile(token.NewFileSet(), "session_linux.go", b, 0)
	must(t, err)
	bindings := map[string][]string{"Mode": {"type", "mode"}, "Uid": {"uid"}, "Nlink": {"nlink"}, "Size": {"size", "negative-size", "size-change"}}
	seen := map[string]bool{}
	for _, decl := range tree.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "check" && fn.Name.Name != "readInputBudget") {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			obj, ok := sel.X.(*ast.Ident)
			if !ok || (obj.Name != "st" && obj.Name != "after") {
				return true
			}
			variants, ok := bindings[sel.Sel.Name]
			if !ok {
				t.Fatalf("RESULT-SPACE: unbound stat predicate %s", sel.Sel.Name)
			}
			for _, v := range variants {
				seen[v] = true
			}
			return true
		})
	}
	if len(seen) == 0 {
		t.Fatal("RESULT-SPACE: empty stat predicate discovery")
	}
	result := make([]string, 0, len(seen))
	for v := range seen {
		result = append(result, v)
	}
	sort.Strings(result)
	return result
}

func resultContender(t *testing.T, c Config) Class { cl, _ := guardContenderResult(t, c); return cl }

// Every counted seam is exercised with every production-classified errno;
// stat predicates and short reads add result dimensions. Class stability here
// is a relational assertion; independent class oracles remain in guard tests.
func TestRecoveryResultSpace(t *testing.T) {
	errnos := recoveryBranchErrnos(t)
	statVariants := recoveryStatVariants(t)
	for _, phase := range []string{"Consume", "Finish"} {
		for _, op := range []string{"ReplaceRecord", "ReadInput"} {
			run := func(t *testing.T, k int, errno syscall.Errno, perturb string) ([]seamEvent, string) {
				c, e, s := sweepSetup(t)
				must(t, s.Close())
				history := disk(t, c)
				active := false
				trace := []seamEvent{}
				var hits []string
				var mu sync.Mutex
				e.monitor.report = func(f string, a ...any) {
					mu.Lock()
					hits = append(hits, fmt.Sprintf(f, a...))
					mu.Unlock()
				}
				e.monitor.hook = func(v seamEvent) error {
					if active {
						trace = append(trace, v)
						if len(trace) == k {
							if errno != 0 {
								return errno
							}
						}
					}
					return nil
				}
				stat, read := e.calls.stat, e.calls.read
				e.calls.stat = func(fd int, st *syscall.Stat_t) error {
					err := stat(fd, st)
					if err == nil && active && len(trace) == k {
						switch perturb {
						case "type":
							st.Mode = st.Mode&^syscall.S_IFMT | syscall.S_IFDIR
						case "mode":
							st.Mode ^= 0040
						case "uid":
							st.Uid++
						case "nlink":
							st.Nlink = 2
						case "size":
							st.Size = MaxPayload + 1
						case "negative-size":
							st.Size = -1
						case "size-change":
							st.Size++
						}
					}
					return err
				}
				e.calls.read = func(fd int, p []byte, off int64) (int, error) {
					if active && len(trace) == k && perturb == "short" {
						return 0, nil
					}
					return read(fd, p, off)
				}
				inside := Class("?")
				var opErr error
				callback := func(r *Recovery) error {
					active = true
					if op == "ReplaceRecord" {
						opErr = r.ReplaceRecord("record", []byte("new"))
					} else {
						b, err := r.ReadInput("arg", sha256.Sum256([]byte("abc")))
						if err == nil {
							must(t, r.Budget.Release(uint64(len(b))))
						}
						opErr = err
					}
					if opErr != nil {
						_ = r.s.fail(opErr)
					}
					active = false
					inside = resultContender(t, c)
					return nil
				}
				cons := consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error { return callback(r) }}
				if phase == "Finish" {
					cons = consumerFuncs{finish: callback}
				}
				got, err := e.enter(c, nil, cons, false)
				e.monitor.hook = nil
				if got != nil {
					_ = got.Close()
				}
				e.monitor.report = t.Errorf
				fresh := resultContender(t, c)
				t.Logf("trace %s/%s k=%d errno=%d op=%v entry=%v contender=%s fresh=%s monitor=%d", phase, op, k, int(errno), opErr, err, inside, fresh, len(hits))
				if inside != OwnershipContended {
					t.Errorf("RESULT-SPACE: %s/%s k=%d errno=%v: ownership released inside the consumer callback: contender class=%q op-class=%s monitor-reports=%d %q",
						phase, op, k, errno, inside, ClassOf(opErr), len(hits), hits)
				} else if len(hits) != 0 {
					t.Errorf("RESULT-SPACE: %s/%s k=%d errno=%v: fixture monitor reported %q", phase, op, k, errno, hits)
				}
				if ClassOf(err) != ClassOf(opErr) || (got != nil) != (opErr == nil) {
					t.Errorf("RESULT-SPACE: ignored operation result changed: op=%v entry=%v", opErr, err)
				}
				var operationError, entryError *Error
				opPoison := errors.As(opErr, &operationError) && operationError.Poisoned
				entryPoison := errors.As(err, &entryError) && entryError.Poisoned
				if entryPoison != (opPoison || (op == "ReplaceRecord" && opErr != nil)) {
					t.Errorf("RESULT-SPACE: poison changed: op=%v entry=%v", opErr, err)
				}
				if !bytes.Equal(history, disk(t, c)) {
					t.Fatal("RESULT-SPACE: journal changed")
				}
				if fresh != "" {
					t.Errorf("RESULT-SPACE: %s/%s k=%d errno=%v: fresh owner after the entry returned %s", phase, op, k, errno, fresh)
				}
				desc := ""
				if k > 0 && len(trace) >= k {
					desc = trace[k-1].kind
				}
				return trace, desc
			}
			var baseline []seamEvent
			t.Run(phase+"/"+op+"/control", func(t *testing.T) {
				baseline, _ = run(t, 0, 0, "")
				if len(baseline) == 0 {
					t.Fatal("RESULT-SPACE: empty success trace")
				}
			})
			for i, v := range baseline {
				var variants []string
				if v.kind == "stat" {
					variants = statVariants
				}
				if v.kind == "read" {
					variants = []string{"short"}
				}
				for _, variant := range variants {
					t.Run(fmt.Sprintf("%s/%s/%d-%s/%s", phase, op, i+1, v.kind, variant), func(t *testing.T) {
						tr, kind := run(t, i+1, 0, variant)
						if len(tr) < i+1 || kind != v.kind {
							t.Fatal("RESULT-SPACE: perturbation not reached")
						}
					})
				}
				for _, errno := range errnos {
					t.Run(fmt.Sprintf("%s/%s/%d-%s/%d", phase, op, i+1, v.kind, int(errno)), func(t *testing.T) {
						tr, kind := run(t, i+1, errno, "")
						if len(tr) < i+1 || kind != v.kind {
							t.Fatalf("RESULT-SPACE: injection not reached k=%d trace=%d", i+1, len(tr))
						}
					})
				}
			}
		}
	}
}

// Real nonregular entries, hard links, mode-zero files and a real temporary
// collision refuse invalid-state in both phases. Regular entries are positive
// controls. No case releases ownership inside the callback; entry retains the
// class and a fresh owner can recover after it ends.
func TestRecoveryRealEntryConditions(t *testing.T) {
	for _, phase := range []string{"Consume", "Finish"} {
		for _, where := range []string{"inputs", "records"} {
			for _, kind := range []string{"regular", "socket", "symlink", "fifo", "directory", "hardlink", "mode-zero", "collision"} {
				t.Run(phase+"/"+where+"/"+kind, func(t *testing.T) {
					c, e, s := sweepSetup(t)
					must(t, s.Close())
					name := "x"
					dir := filepath.Join(c.Directory, where)
					if kind == "collision" && where == "inputs" {
						return
					}
					switch kind {
					case "regular":
						name = "arg"
						if where == "records" {
							name = "record"
						}
					case "socket":
						wd, err := os.Getwd()
						must(t, err)
						must(t, os.Chdir(dir))
						l, err := net.Listen("unix", name)
						if err == nil {
							l.(*net.UnixListener).SetUnlinkOnClose(false)
						}
						must(t, os.Chdir(wd))
						must(t, err)
						defer l.Close()
					case "symlink":
						target := "arg"
						if where == "records" {
							target = "record"
						}
						must(t, os.Symlink(target, filepath.Join(dir, name)))
					case "fifo":
						must(t, syscall.Mkfifo(filepath.Join(dir, name), 0600))
					case "hardlink":
						target := "arg"
						if where == "records" {
							target = "record"
						}
						must(t, os.Link(filepath.Join(dir, target), filepath.Join(dir, name)))
					case "mode-zero":
						put(t, filepath.Join(dir, name), []byte("abc"), 0600)
						must(t, os.Chmod(filepath.Join(dir, name), 0))
					case "collision":
						op := e.calls.open
						e.calls.open = func(d int, n string, f int, m uint32) (int, error) {
							if strings.HasPrefix(n, ".record-tmp-") {
								put(t, filepath.Join(dir, n), []byte("stale"), 0600)
							}
							return op(d, n, f, m)
						}
					case "directory":
						must(t, os.Mkdir(filepath.Join(dir, name), 0700))
					}
					var hits []string
					e.monitor.report = func(f string, a ...any) { hits = append(hits, fmt.Sprintf(f, a...)) }
					inside := Class("?")
					var opErr error
					callback := func(r *Recovery) error {
						if where == "records" {
							opErr = r.ReplaceRecord(name, []byte("new"))
						} else {
							b, err := r.ReadInput(name, sha256.Sum256([]byte("abc")))
							if err == nil {
								must(t, r.Budget.Release(uint64(len(b))))
							}
							opErr = err
						}
						inside = resultContender(t, c)
						return nil
					}
					cons := consumerFuncs{consume: func(_ uint64, _ []byte, r *Recovery) error { return callback(r) }}
					if phase == "Finish" {
						cons = consumerFuncs{finish: callback}
					}
					got, err := e.enter(c, nil, cons, false)
					if got != nil {
						_ = got.Close()
					}
					e.monitor.report = t.Errorf
					t.Logf("trace %s/%s/%s: op=%v entry=%v contender=%s monitor=%d", phase, where, kind, opErr, err, inside, len(hits))
					if inside != OwnershipContended {
						t.Errorf("RESULT-SPACE: %s/%s/%s: ownership released inside the consumer callback: contender class=%q op=%v monitor-reports=%d", phase, where, kind, inside, opErr, len(hits))
					}
					if len(hits) != 0 {
						t.Errorf("RESULT-SPACE: real-entry monitor: %v", hits)
					}
					sweepContender(t, c, "")
					if kind == "regular" {
						if opErr != nil || err != nil || got == nil {
							t.Fatalf("RESULT-SPACE: control: op=%v entry=%v", opErr, err)
						}
					} else if ClassOf(opErr) != InvalidState || ClassOf(err) != InvalidState || got != nil {
						t.Fatalf("RESULT-SPACE: setup %s: op=%v entry=%v", kind, opErr, err)
					}
				})
			}
		}
	}
}
