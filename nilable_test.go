package main

import (
	"go/ast"
	"strings"
	"testing"
)

func TestInsertNilablePointersDirective(t *testing.T) {
	const input = `module example.com/app

go 1.27

require example.com/lib v0.0.0
`
	got := insertNilablePointersDirective(input)
	if !strings.Contains(got, "nilable_pointers enable") {
		t.Fatalf("missing directive:\n%s", got)
	}
	if strings.Count(got, "nilable_pointers") != 1 {
		t.Fatalf("expected one directive:\n%s", got)
	}
}

func TestHasNilablePointersDirective(t *testing.T) {
	if !hasNilablePointersDirective("nilable_pointers warnings\n") {
		t.Fatal("expected true")
	}
	if hasNilablePointersDirective("go 1.27\n") {
		t.Fatal("expected false")
	}
}

func TestSetPointerNilable(t *testing.T) {
	star := &ast.StarExpr{X: &ast.Ident{Name: "string"}}
	got := setPointerNilable(star, true)
	n, ok := got.(*ast.NilableTypeExpr)
	if !ok {
		t.Fatalf("got %T", got)
	}
	if _, ok := n.X.(*ast.StarExpr); !ok {
		t.Fatalf("expected star inside nilable, got %T", n.X)
	}
	back := setPointerNilable(got, false)
	if _, ok := back.(*ast.StarExpr); !ok {
		t.Fatalf("expected star back, got %T", back)
	}
}
