package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestModernizeShorthandTypes(t *testing.T) {
	const src = `package p

// Person doc
type Person struct {
	Name string
}

type Stringer interface {
	String() string
}

type Alias = int

func f() {
	type local struct {
		ID int
	}
	_ = local{}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	n := modernizeShorthandTypes(f)
	if n != 3 {
		t.Fatalf("converted %d decls, want 3", n)
	}
	if _, ok := f.Decls[0].(*ast.StructDecl); !ok {
		t.Fatalf("Decls[0] = %T, want *ast.StructDecl", f.Decls[0])
	}
	if _, ok := f.Decls[1].(*ast.InterfaceDecl); !ok {
		t.Fatalf("Decls[1] = %T, want *ast.InterfaceDecl", f.Decls[1])
	}
	if _, ok := f.Decls[2].(*ast.GenDecl); !ok {
		t.Fatalf("Decls[2] = %T, want *ast.GenDecl (alias)", f.Decls[2])
	}
	fn := f.Decls[3].(*ast.FuncDecl)
	ds := fn.Body.List[0].(*ast.DeclStmt)
	if _, ok := ds.Decl.(*ast.StructDecl); !ok {
		t.Fatalf("local decl = %T, want *ast.StructDecl", ds.Decl)
	}

	var out strings.Builder
	if err := format.Node(&out, fset, f); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"struct Person {",
		"interface Stringer {",
		"type Alias = int",
		"struct local {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "type Person struct") || strings.Contains(got, "type Stringer interface") {
		t.Fatalf("old type syntax remains:\n%s", got)
	}
}
