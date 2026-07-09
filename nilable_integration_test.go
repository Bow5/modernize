package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestPtrAnnotatorNilEvidence(t *testing.T) {
	const src = `package p

type Node struct {
	Next *Node
}

func Find(id int) *Node {
	return nil
}

func Must(x *Node) *Node {
	if x == nil {
		panic("nil")
	}
	return x
}

func Use(n *Node) {
	var p *Node
	p = nil
	_ = n
	_ = p
}

func Call() {
	Use(nil)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()

	strict := func(kind, owner, name string, index int) {
		key := ptrSiteKey{kind: kind, owner: owner, name: name, index: index}
		if ann.nilable[key] {
			t.Errorf("%v should be strict", key)
		}
	}
	markNilable := func(kind, owner, name string, index int) {
		key := ptrSiteKey{kind: kind, owner: owner, name: name, index: index}
		if !ann.nilable[key] {
			t.Errorf("%v should be nilable", key)
		}
	}

	strict("field", "Node", "Next", -1)
	markNilable("result", "Find", "", 0)
	strict("result", "Must", "", 0)
	strict("param", "Must", "x", 0)
	markNilable("param", "Use", "n", 0)
	markNilable("var", "Use", "p", -1)

	rewriteFilePointerTypes(fset, f, ann)
	var buf strings.Builder
	if err := format.Node(&buf, fset, f); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Find(id int) *Node?") {
		t.Fatalf("expected nilable result:\n%s", out)
	}
	if strings.Contains(out, "Next *Node?") {
		t.Fatalf("Next should stay strict:\n%s", out)
	}
	if !strings.Contains(out, "Must(x *Node) *Node") {
		t.Fatalf("Must should keep strict pointers:\n%s", out)
	}
}
