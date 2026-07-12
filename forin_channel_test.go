package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestFixChannelForInBlankValue(t *testing.T) {
	const src = `package kafka

func f(logCh chan interface{}) {
	for entry, _ in logCh {
		_ = entry
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "kafka.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, ok := typecheckFiles(fset, []*ast.File{f}, "example.com/kafka")
	if !ok {
		t.Fatal("typecheck failed")
	}
	var rs *ast.RangeStmt
	ast.Inspect(f, func(n ast.Node) bool {
		if r, ok := n.(*ast.RangeStmt); ok {
			rs = r
			return false
		}
		return true
	})
	if !singleRangeVarIsValue(f, info, rs.X) {
		t.Fatal("singleRangeVarIsValue false")
	}
	n := modernizeForIn(f, info)
	if n != 1 {
		t.Fatalf("modernizeForIn rewrote %d, want 1", n)
	}
	var buf strings.Builder
	if err := format.Node(&buf, fset, f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "for entry in logCh") {
		t.Fatalf("expected for entry in logCh:\n%s", buf.String())
	}
}
