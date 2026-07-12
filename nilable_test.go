package main

import (
	"go/ast"
	"strings"
	"testing"
)

func TestUpgradeNilablePointersDirective(t *testing.T) {
	const input = `module example.com/app

go 1.27

nilable_pointers enable

require example.com/lib v0.0.0
`
	got := strings.Replace(input, "nilable_pointers enable", "nilable_pointers warnings", 1)
	if !strings.Contains(got, "nilable_pointers warnings") {
		t.Fatalf("missing warnings directive:\n%s", got)
	}
	if strings.Contains(got, "nilable_pointers enable") {
		t.Fatalf("still has enable:\n%s", got)
	}
}

func TestInsertNilablePointersDirective(t *testing.T) {
	const input = `module example.com/app

go 1.27

require example.com/lib v0.0.0
`
	got := insertNilablePointersDirective(input)
	if !strings.Contains(got, "nilable_pointers warnings") {
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

func TestSetRefNilable(t *testing.T) {
	slice := &ast.ArrayType{Elt: &ast.Ident{Name: "string"}}
	got := setRefNilable(slice, true)
	n, ok := got.(*ast.NilableTypeExpr)
	if !ok {
		t.Fatalf("got %T", got)
	}
	if _, ok := n.X.(*ast.ArrayType); !ok {
		t.Fatalf("expected slice inside nilable, got %T", n.X)
	}
	back := setRefNilable(got, false)
	if _, ok := back.(*ast.ArrayType); !ok {
		t.Fatalf("expected slice back, got %T", back)
	}
}

func TestSetRefNilableChan(t *testing.T) {
	ch := &ast.ChanType{Value: &ast.Ident{Name: "int"}}
	got := setRefNilable(ch, true)
	n, ok := got.(*ast.NilableTypeExpr)
	if !ok {
		t.Fatalf("got %T", got)
	}
	if _, ok := n.X.(*ast.ChanType); !ok {
		t.Fatalf("expected chan inside nilable, got %T", n.X)
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
