package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/token"
	"sort"
)

type sourceEdit struct {
	start int
	end   int
	text  []byte
}

func shorthandCompositeEdit(fset *token.FileSet, lit *ast.CompositeLit) ([]sourceEdit, bool) {
	if lit.Type == nil || !lit.Lbrace.IsValid() {
		return nil, false
	}
	start := fset.Position(lit.Type.Pos()).Offset
	openEnd := fset.Position(lit.Lbrace + 1).Offset
	switch t := lit.Type.(type) {
	case *ast.ArrayType:
		if t.Len != nil || !lit.Rbrace.IsValid() || !shorthandSliceRewriteOK(lit) {
			return nil, false
		}
		closeStart := fset.Position(lit.Rbrace).Offset
		closeEnd := fset.Position(lit.Rbrace + 1).Offset
		return []sourceEdit{
			{start: start, end: openEnd, text: []byte("[")},
			{start: closeStart, end: closeEnd, text: []byte("]")},
		}, true
	case *ast.MapType:
		if !shorthandMapRewriteOK(lit) {
			return nil, false
		}
		return []sourceEdit{{start: start, end: openEnd, text: []byte("{")}}, true
	default:
		return nil, false
	}
}

func modernizeShorthandLiterals(fset *token.FileSet, f *ast.File, src []byte) (edits []sourceEdit, count int) {
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if e, ok := shorthandCompositeEdit(fset, n); ok {
				edits = append(edits, e...)
				count++
			}
		case *ast.CallExpr:
			if e, ok := shorthandSetOfEdit(fset, n); ok {
				edits = append(edits, e)
				count++
			}
		}
		return true
	})
	return edits, count
}

func isSetIdent(x ast.Expr) bool {
	id, ok := ast.Unparen(x).(*ast.Ident)
	return ok && id.Name == "set"
}

func shorthandSetOfEdit(fset *token.FileSet, call *ast.CallExpr) (sourceEdit, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !isSetIdent(sel.X) || sel.Sel.Name != "Of" {
		return sourceEdit{}, false
	}
	if !shorthandSetOfRewriteOK(call) {
		return sourceEdit{}, false
	}
	start := fset.Position(call.Pos()).Offset
	end := fset.Position(call.End()).Offset
	if len(call.Args) == 0 {
		return sourceEdit{start: start, end: end, text: []byte("{}")}, true
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, arg := range call.Args {
		if i > 0 {
			buf.WriteString(", ")
		}
		if err := format.Node(&buf, fset, arg); err != nil {
			return sourceEdit{}, false
		}
	}
	buf.WriteByte('}')
	return sourceEdit{start: start, end: end, text: buf.Bytes()}, true
}

func modernizeSpreadCalls(fset *token.FileSet, f *ast.File) (edits []sourceEdit, count int) {
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !call.Ellipsis.IsValid() || len(call.Args) == 0 {
			return true
		}
		last := call.Args[len(call.Args)-1]
		start := fset.Position(last.Pos()).Offset
		end := fset.Position(call.Ellipsis + token.Pos(len("..."))).Offset
		var buf bytes.Buffer
		buf.WriteString("...")
		if err := format.Node(&buf, fset, last); err != nil {
			return true
		}
		edits = append(edits, sourceEdit{start: start, end: end, text: buf.Bytes()})
		count++
		return true
	})
	return edits, count
}

func applySourceEdits(src []byte, edits []sourceEdit) []byte {
	if len(edits) == 0 {
		return src
	}
	sort.Slice(edits, func(i, j int) bool {
		return edits[i].start > edits[j].start
	})
	for _, e := range edits {
		if e.start < 0 || e.end > len(src) || e.start > e.end {
			continue
		}
		var out bytes.Buffer
		out.Write(src[:e.start])
		out.Write(e.text)
		out.Write(src[e.end:])
		src = out.Bytes()
	}
	return src
}
