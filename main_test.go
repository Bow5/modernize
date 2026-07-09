package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestErrUsedInStmtsDetectsLaterAssign(t *testing.T) {
	const src = `package p
func f() {
	f, err := open()
	if err != nil {
		return nil, err
	}
	if err = sync(); err != nil {
		return nil, err
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := f.Decls[0].(*ast.FuncDecl).Body
	if !errUsedInStmts(body.List, 2, "err") {
		t.Fatal("expected err reuse detected at index 2")
	}
}
