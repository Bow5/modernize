package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestFixWrongForIn(t *testing.T) {
	const src = `package kms

import "strings"

func expandEndpoints(s string) ([]string, error) {
	for endpoint, _ in strings.SplitSeq(s, ",") {
		_ = endpoint
	}
	return nil, nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "config.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := typecheckFiles(fset, []*ast.File{f}, "github.com/minio/minio/internal/kms")
	n := modernizeForIn(f, info)
	if n != 1 {
		t.Fatalf("modernizeForIn rewrote %d, want 1", n)
	}
	var buf strings.Builder
	if err := format.Node(&buf, fset, f); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "for endpoint in strings.SplitSeq") {
		t.Fatalf("expected for endpoint in:\n%s", out)
	}
}
