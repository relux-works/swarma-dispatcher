//go:build linux

package main

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"syscall"
	"testing"
)

// Inspect every non-test Go file in this package, including inactive platform files.
// Raw fcntl is permitted only for constant F_DUPFD_CLOEXEC; ioctl only for the
// read-only TIOCINQ query. Numeric expressions are evaluated, not token-matched.
// This source policy covers direct Go syscall calls and named flag APIs, not
// assembly, cgo, generated-at-runtime code, function aliases, or calls inside
// other packages (including net and internal/demolaunch). Selector constants
// are resolved by name, not type identity; misleading selectors are a blind spot.
// Runtime success/refusal/signal tests complement this deliberately narrow policy.
func TestIntakeNeverSetsFlags(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range files {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if reason := flagSyscallViolation(f); reason != "" {
			t.Errorf("intake mutates shared descriptor flags: %s: %s", entry.Name(), reason)
		}
	}
}

func flagSyscallViolation(f *ast.File) string {
	known := map[string]int64{
		"SYS_FCNTL": int64(syscall.SYS_FCNTL), "SYS_IOCTL": int64(syscall.SYS_IOCTL),
		"SYS_GETPID": int64(syscall.SYS_GETPID),
		"SYS_PPOLL":  int64(syscall.SYS_PPOLL), "F_DUPFD_CLOEXEC": int64(syscall.F_DUPFD_CLOEXEC),
		"F_SETFL": int64(syscall.F_SETFL), "F_SETFD": int64(syscall.F_SETFD),
		"TIOCINQ": int64(syscall.TIOCINQ), "FIONBIO": 0x5421,
	}
	var value func(ast.Expr) constant.Value
	value = func(e ast.Expr) constant.Value {
		switch e := e.(type) {
		case *ast.BasicLit:
			return constant.MakeFromLiteral(e.Value, e.Kind, 0)
		case *ast.ParenExpr:
			return value(e.X)
		case *ast.SelectorExpr:
			if v, ok := known[e.Sel.Name]; ok {
				return constant.MakeInt64(v)
			}
		case *ast.BinaryExpr:
			a, b := value(e.X), value(e.Y)
			if a.Kind() == constant.Int && b.Kind() == constant.Int {
				switch e.Op {
				case token.ADD, token.SUB, token.OR, token.AND, token.XOR:
					return constant.BinaryOp(a, e.Op, b)
				}
			}
		}
		return constant.MakeUnknown()
	}
	integer := func(e ast.Expr) (int64, bool) {
		v := value(e)
		if v.Kind() != constant.Int {
			return 0, false
		}
		return constant.Int64Val(v)
	}
	reason := ""
	ast.Inspect(f, func(n ast.Node) bool {
		if reason != "" {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && (id.Name == "SetNonblock" || id.Name == "F_SETFL" || id.Name == "F_SETFD" || id.Name == "FIONBIO") {
			reason = "flag-setting API or command " + id.Name
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		case *ast.Ident:
			name = fun.Name
		}
		// SyscallConn obtains an os.File raw connection; it is not a trap
		// invocation. Exempt only the zero-argument selector spelling, while
		// continuing to inspect its receiver and any surrounding callbacks.
		if _, selector := call.Fun.(*ast.SelectorExpr); selector && name == "SyscallConn" && len(call.Args) == 0 {
			return true
		}
		if !strings.Contains(name, "Syscall") {
			return true
		}
		if len(call.Args) < 3 {
			reason = "unrecognized raw syscall"
			return false
		}
		trap, ok := integer(call.Args[0])
		if !ok {
			reason = "non-constant or unknown syscall number"
			return false
		}
		if trap == int64(syscall.SYS_FCNTL) || trap == int64(syscall.SYS_IOCTL) {
			cmd, constantCommand := integer(call.Args[2])
			allowed := int64(syscall.F_DUPFD_CLOEXEC)
			if trap == int64(syscall.SYS_IOCTL) {
				allowed = int64(syscall.TIOCINQ)
			}
			if !constantCommand || cmd != allowed {
				reason = "raw descriptor command is not an allowed constant"
			}
		} else if trap != int64(syscall.SYS_PPOLL) {
			reason = "raw syscall is outside the intake allowlist"
		}
		return true
	})
	return reason
}

// Literal fixtures prove the scanner rejects numeric/variable commands as well
// as named ones; direct duplication is the positive control. These are syntax
// inputs only, never executed or type-checked as product code.
func TestFlagSyscallPolicy(t *testing.T) {
	// Pin the unchanged production spelling that previously matched the raw
	// syscall recognizer. The paired refusal ensures this exemption does not
	// hide a forbidden call inside the receiver expression.
	for _, tc := range []struct {
		name, source, want string
	}{
		{"production-connection", "raw, err := input.SyscallConn()", ""},
		{"connection-receiver", "raw, err := syscall.AllThreadsSyscall(syscall.SYS_FCNTL, fd, 4, 0).SyscallConn()", "raw descriptor command is not an allowed constant"},
		{"connection-arguments", "input.SyscallConn(trap, fd, command)", "non-constant or unknown syscall number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package main; func f() { "+tc.source+" }", 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := flagSyscallViolation(f); got != tc.want {
				t.Fatalf("flag syscall connection policy mismatch: %s: got %q want %q", tc.name, got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		command string
		allowed bool
	}{
		{"syscall.F_DUPFD_CLOEXEC", true}, {"1030", true}, {"4", false},
		{"2", false}, {"syscall.F_SETFL", false}, {"syscall.F_SETFD", false}, {"command", false},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package main; func f() { syscall.Syscall(syscall.SYS_FCNTL, fd, "+tc.command+", 0) }", 0)
		if err != nil {
			t.Fatal(err)
		}
		if (flagSyscallViolation(f) == "") != tc.allowed {
			t.Fatalf("flag syscall policy mismatch: %s", tc.command)
		}
	}
}

// Each refusal branch has its own assertion and an adjacent allowed control.
// These parsed fixtures are never executed: unknown entry points fail closed.
func TestFlagSyscallPolicyBranches(t *testing.T) {
	for _, tc := range []struct{ name, good, bad, reason, assertion string }{
		{"ioctl", "syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCINQ, 0)", "syscall.Syscall(syscall.SYS_IOCTL, fd, 0x5421, 0)", "raw descriptor command is not an allowed constant", "flag policy ioctl refusal changed"},
		{"ioctl-expression", "syscall.RawSyscall(syscall.SYS_IOCTL, fd, syscall.TIOCINQ, 0)", "syscall.RawSyscall(syscall.SYS_IOCTL, fd, syscall.TIOCINQ+6, 0)", "raw descriptor command is not an allowed constant", "flag policy ioctl expression refusal changed"},
		{"trap", "syscall.Syscall6(syscall.SYS_PPOLL, 0, 0, 0, 0, 0, 0)", "syscall.Syscall6(syscall.SYS_GETPID, 0, 0, 0, 0, 0, 0)", "raw syscall is outside the intake allowlist", "flag policy trap allowlist changed"},
		{"unknown-trap", "syscall.RawSyscall6(syscall.SYS_PPOLL, 0, 0, 0, 0, 0, 0)", "syscall.RawSyscall6(trap, 0, 0, 0, 0, 0, 0)", "non-constant or unknown syscall number", "flag policy unknown trap accepted"},
		{"allthreads", "syscall.AllThreadsSyscall(syscall.SYS_FCNTL, fd, 1030, 0)", "syscall.AllThreadsSyscall(syscall.SYS_FCNTL, fd, 4, 0)", "raw descriptor command is not an allowed constant", "flag policy allthreads mutation accepted"},
		{"allthreads6", "syscall.AllThreadsSyscall6(syscall.SYS_FCNTL, fd, 1030, 0, 0, 0, 0)", "syscall.AllThreadsSyscall6(syscall.SYS_FCNTL, fd, 4, 0, 0, 0, 0)", "raw descriptor command is not an allowed constant", "flag policy allthreads6 mutation accepted"},
		{"unknown-entry", "syscall.FutureSyscall(syscall.SYS_PPOLL, 0, 0)", "syscall.FutureSyscall(trap, 0, 0)", "non-constant or unknown syscall number", "flag policy unknown entry accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parse := func(call string) *ast.File {
				f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package main; func f() { "+call+" }", 0)
				if err != nil {
					t.Fatal(err)
				}
				return f
			}
			if got := flagSyscallViolation(parse(tc.good)); got != "" {
				t.Fatalf("flag policy allowed control refused: %s", got)
			}
			if got := flagSyscallViolation(parse(tc.bad)); got != tc.reason {
				t.Fatalf("%s: got %q want %q", tc.assertion, got, tc.reason)
			}
		})
	}
}
