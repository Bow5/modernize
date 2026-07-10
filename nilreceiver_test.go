package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestRemoveNilReceiverGuard(t *testing.T) {
	const src = `package p
type Connection struct{}
type Subroute struct{ Connection *Connection }

func (c *Connection) Subroute(s string) *Subroute {
	if c == nil {
		return nil
	}
	return &Subroute{Connection: c}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := removeNilReceiverGuards(f)
	if n != 1 {
		t.Fatalf("removed %d guards, want 1", n)
	}
	fn := f.Decls[2].(*ast.FuncDecl)
	if len(fn.Body.List) != 1 {
		t.Fatalf("body len %d, want 1 after guard removal", len(fn.Body.List))
	}
}

func TestRemoveNilReceiverGuardSkipsLoggingBody(t *testing.T) {
	const src = `package p
type T struct{}
func (t *T) M() {
	if t == nil {
		log.Println("bad")
		return
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if removeNilReceiverGuards(f) != 0 {
		t.Fatal("expected guard with extra logging to be kept")
	}
}

func TestOptionalMethodChainsWithNilGuard(t *testing.T) {
	const src = `package p
type M struct{}
func Connection(host string) *M { return nil }
func (m *M) Subroute(path string) *M {
	if m == nil {
		return nil
	}
	return m
}

func f(host, path string) *M {
	return Connection(host).Subroute(path)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*ast.File{f}
	guardIdx := buildNilReceiverGuardIndex(files)
	if n := optionalMethodChains(f, files, guardIdx); n != 1 {
		t.Fatalf("rewrote %d chains, want 1", n)
	}
	ret := f.Decls[3].(*ast.FuncDecl).Body.List[0].(*ast.ReturnStmt)
	call := ret.Results[0].(*ast.CallExpr)
	sel := call.Fun.(*ast.SelectorExpr)
	if _, ok := sel.X.(*ast.NullCondExpr); !ok {
		t.Fatalf("expected NullCondExpr on selector, got %T", sel.X)
	}
}

func TestOptionalMethodChainsSkipsWithoutNilGuard(t *testing.T) {
	const src = `package p
type M struct{}
func Connection(host string) *M { return nil }
func (m *M) Subroute(path string) *M {
	return m
}

func f(host, path string) *M {
	return Connection(host).Subroute(path)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*ast.File{f}
	guardIdx := buildNilReceiverGuardIndex(files)
	if n := optionalMethodChains(f, files, guardIdx); n != 0 {
		t.Fatalf("rewrote %d chains, want 0 without nil receiver guard", n)
	}
}

func TestOptionalMethodChainsDoubleChain(t *testing.T) {
	const src = `package p
type M struct{}
func Connection() *M { return nil }
func (m *M) MethodA() *M {
	if m == nil {
		return nil
	}
	return m
}
func (m *M) MethodB() *M {
	if m == nil {
		return nil
	}
	return m
}

func f() *M {
	return Connection().MethodA().MethodB()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*ast.File{f}
	guardIdx := buildNilReceiverGuardIndex(files)
	if n := optionalMethodChains(f, files, guardIdx); n != 2 {
		t.Fatalf("rewrote %d chains, want 2", n)
	}
	ret := f.Decls[4].(*ast.FuncDecl).Body.List[0].(*ast.ReturnStmt)
	outer := ret.Results[0].(*ast.CallExpr).Fun.(*ast.SelectorExpr)
	if _, ok := outer.X.(*ast.NullCondExpr); !ok {
		t.Fatalf("outer expected NullCondExpr, got %T", outer.X)
	}
	innerCall := outer.X.(*ast.NullCondExpr).X.(*ast.CallExpr)
	inner := innerCall.Fun.(*ast.SelectorExpr)
	if _, ok := inner.X.(*ast.NullCondExpr); !ok {
		t.Fatalf("inner expected NullCondExpr, got %T", inner.X)
	}
}

func TestOptionalMethodChainsOnlyInnerGuard(t *testing.T) {
	const src = `package p
type M struct{}
func Connection() *M { return nil }
func (m *M) MethodA() *M {
	return m
}
func (m *M) MethodB() *M {
	if m == nil {
		return nil
	}
	return m
}

func f() *M {
	return Connection().MethodA().MethodB()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*ast.File{f}
	guardIdx := buildNilReceiverGuardIndex(files)
	if n := optionalMethodChains(f, files, guardIdx); n != 1 {
		t.Fatalf("rewrote %d chains, want 1 (MethodB only)", n)
	}
}
