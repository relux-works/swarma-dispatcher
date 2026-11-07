//go:build linux

package demolaunch

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func daemonSealsFirst(src []byte) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "service_linux.go", src, 0)
	if err != nil {
		return false
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Daemon" {
			continue
		}
		if fn.Body == nil || len(fn.Body.List) == 0 {
			return false
		}
		gate, ok := fn.Body.List[0].(*ast.IfStmt)
		if !ok || gate.Else != nil {
			return false
		}
		assign, ok := gate.Init.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return false
		}
		id, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || id.Name != "err" {
			return false
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) != 0 {
			return false
		}
		name, ok := call.Fun.(*ast.Ident)
		if !ok || name.Name != "sealInheritedDescriptors" {
			return false
		}
		cond, ok := gate.Cond.(*ast.BinaryExpr)
		if !ok || cond.Op != token.NEQ {
			return false
		}
		left, lok := cond.X.(*ast.Ident)
		right, rok := cond.Y.(*ast.Ident)
		if !lok || !rok || left.Name != "err" || right.Name != "nil" || len(gate.Body.List) != 1 {
			return false
		}
		ret, ok := gate.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return false
		}
		value, ok := ret.Results[0].(*ast.Ident)
		return ok && value.Name == "err"
	}
	return false
}

// A narrow AST witness requires the seal and error return as Daemon's first
// statement, with omission/order/error-swallowing controls. It does not execute
// the positive root-owned daemon path or qualify supervisor deployment.
func TestDaemonDescriptorSealWiring(t *testing.T) {
	b, err := os.ReadFile("service_linux.go")
	must(t, err)
	gate := []byte("if err := sealInheritedDescriptors(); err != nil {\n\t\treturn err\n\t}")
	if bytes.Count(b, gate) != 1 || !daemonSealsFirst(b) {
		t.Fatal("daemon must seal descriptors first and refuse errors")
	}
	for _, replacement := range []string{"", "syscall.Umask(0177)\n" + string(gate), "if err := sealInheritedDescriptors(); err != nil { return nil }"} {
		if daemonSealsFirst(bytes.Replace(b, gate, []byte(replacement), 1)) {
			t.Fatal("source control missed", replacement)
		}
	}
	has, err := boundaryReferences(b, "Client", []string{"sealInheritedDescriptors"})
	must(t, err)
	if has {
		t.Fatal("Client must not seal process descriptors")
	}
}
