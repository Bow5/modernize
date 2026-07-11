package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestModernizeForIn(t *testing.T) {
	const src = `package p

func f(items []string) {
	for _, item := range items {
		_ = item
	}
	for i, item := range items {
		_ = i
	}
	for k := range items {
		_ = k
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := modernizeForIn(f)
	if n != 3 {
		t.Fatalf("modernizeForIn rewrote %d, want 3", n)
	}
	var buf strings.Builder
	if err := format.Node(&buf, fset, f); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "for item in items") {
		t.Fatalf("expected for item in items:\n%s", out)
	}
	if !strings.Contains(out, "for i, item in items") {
		t.Fatalf("expected for i, item in items:\n%s", out)
	}
	if !strings.Contains(out, "for k, _ in items") {
		t.Fatalf("expected for k, _ in items:\n%s", out)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		if rs.InPos.IsValid() && rs.Range.IsValid() {
			t.Fatalf("range stmt has both InPos and Range: %#v", rs)
		}
		return true
	})
}
