package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestLabelInterfaceNilComparisons(t *testing.T) {
	const src = `package p

type I interface { M() }

func f(err error, i I, s *int) {
	if err != nil {}
	if i == nil {}
	if s == nil {}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	editsByFile := labelInterfaceNilComparisons(fset, []*ast.File{f}, "example.com/p")
	edits := editsByFile[0]
	if len(edits) != 1 {
		t.Fatalf("got %d labels, want 1 (error interface skipped)", len(edits))
	}
	for _, e := range edits {
		if string(e.text) != interfaceNilEqFixme + "\n" {
			t.Fatalf("unexpected edit text %q", e.text)
		}
	}
}
