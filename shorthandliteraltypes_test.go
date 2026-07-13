package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestShorthandSliceRewriteOK(t *testing.T) {
	tests := []struct {
		src  string
		want bool
	}{
		{`package p; func f() { _ = []int{1, 2, 3} }`, true},
		{`package p; func f() { _ = []int64{1, 2, 3} }`, false},
		{`package p; func f() { _ = []int64{int64(1)} }`, true},
		{`package p; type U struct{Name string}; func f() { _ = []U{U{"bob"}} }`, true},
		{`package p; type U struct{Name string}; type I interface{}; func f() { _ = []I{U{"bob"}} }`, false},
		{`package p; func f() { _ = []int{} }`, false},
	}
	for _, tc := range tests {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "p.go", tc.src, 0)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		lit := compositeLitFromFile(f)
		if got := shorthandSliceRewriteOK(lit); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.src, got, tc.want)
		}
	}
}

func TestShorthandMapRewriteOK(t *testing.T) {
	tests := []struct {
		src  string
		want bool
	}{
		{`package p; func f() { _ = map[string]int{"a": 1} }`, true},
		{`package p; func f() { _ = map[string]int64{"a": 1} }`, false},
		{`package p; func f() { _ = map[string]int{} }`, false},
	}
	for _, tc := range tests {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "p.go", tc.src, 0)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		lit := compositeLitFromFile(f)
		if got := shorthandMapRewriteOK(lit); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.src, got, tc.want)
		}
	}
}

func compositeLitFromFile(f *ast.File) *ast.CompositeLit {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for _, stmt := range fn.Body.List {
			assign, ok := stmt.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) == 0 {
				continue
			}
			if lit, ok := assign.Rhs[0].(*ast.CompositeLit); ok {
				return lit
			}
		}
	}
	panic("composite lit not found")
}
