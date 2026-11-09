//go:build linux && (amd64 || arm64)

package journal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
)

// The source-shape control counts direct s.mu.Unlock calls only in Append,
// append, drift, fullWrite and syncFD: one deferred call in Append, none in the
// other four. This deliberately requires review if that structure changes.
// The runtime spinner below and TestSessionQueuedAheadOfClose provide
// scheduling-dependent coverage of indirect unlock forms.
func TestAppendCriticalSection(t *testing.T) {
	b, err := recoverySource.ReadFile("session_linux.go")
	must(t, err)
	tree, err := parser.ParseFile(token.NewFileSet(), "session_linux.go", b, 0)
	must(t, err)
	checked := 0
	for _, d := range tree.Decls {
		f, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch f.Name.Name {
		case "Append", "append", "drift", "fullWrite", "syncFD":
		default:
			continue
		}
		checked++
		unlocks, deferred := 0, 0
		isUnlock := func(c *ast.CallExpr) bool {
			sel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Unlock" {
				return false
			}
			mu, ok := sel.X.(*ast.SelectorExpr)
			if !ok || mu.Sel.Name != "mu" {
				return false
			}
			s, ok := mu.X.(*ast.Ident)
			return ok && s.Name == "s"
		}
		ast.Inspect(f.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && isUnlock(c) {
				unlocks++
			}
			if d, ok := n.(*ast.DeferStmt); ok && isUnlock(d.Call) {
				deferred++
			}
			return true
		})
		want := 0
		if f.Name.Name == "Append" {
			want = 1
		}
		if unlocks != want || deferred != want {
			t.Fatalf("APPEND-CRITICAL: %s releases mutex inside transition (%d unlocks, %d deferred)", f.Name.Name, unlocks, deferred)
		}
	}
	if checked != 5 {
		t.Fatal("APPEND-CRITICAL: helper chain control missing")
	}
	_, e, s := sweepSetup(t)
	var pending atomic.Bool
	journal := s.journal
	e.monitor.hook = func(v seamEvent) error {
		if v.kind == "stat" && v.fd == journal {
			pending.Store(true)
		}
		return nil
	}
	sy := e.calls.sync
	e.calls.sync = func(fd int) error {
		err := sy(fd)
		if fd == journal {
			pending.Store(false)
		}
		return err
	}
	stop, done := make(chan struct{}), make(chan struct{})
	var hits, acquired atomic.Int64
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if s.mu.TryLock() {
				acquired.Add(1)
				var st syscall.Stat_t
				if pending.Load() || syscall.Fstat(journal, &st) != nil || st.Size != s.state.EOF {
					hits.Add(1)
				}
				s.mu.Unlock()
			}
			runtime.Gosched()
		}
	}()
	// A short bounded positive control; source-shape assertions, not scheduler
	// luck, kill the named seam-free unlock plants.
	for i := 0; i < 32; i++ {
		_, err = s.Append([]byte("x"))
		if err != nil {
			break
		}
		runtime.Gosched()
	}
	close(stop)
	guardReceive(t, done, "APPEND-CRITICAL spinner")
	e.monitor.hook = nil
	e.calls.sync = sy
	if err != nil || hits.Load() != 0 {
		t.Fatalf("APPEND-CRITICAL: inconsistent transition hits=%d err=%v", hits.Load(), err)
	}
	// Always exercise the stable-state positive assertion even on one scheduler P.
	s.mu.Lock()
	var st syscall.Stat_t
	must(t, syscall.Fstat(journal, &st))
	same := st.Size == s.state.EOF
	s.mu.Unlock()
	if !same {
		t.Fatal("APPEND-CRITICAL: final state differs from journal")
	}
	t.Logf("runtime mutex acquisitions: %d", acquired.Load())
}
