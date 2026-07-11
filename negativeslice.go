package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/token"
)

func modernizeNegativeSlice(fset *token.FileSet, f *ast.File) (edits []sourceEdit, count int) {
	ast.Inspect(f, func(n ast.Node) bool {
		se, ok := n.(*ast.SliceExpr)
		if !ok {
			return true
		}
		var local []sourceEdit
		if low, ok := modernizeLenMinusBound(fset, se.X, se.Low); ok {
			local = append(local, sourceEdit{
				start: fset.Position(se.Low.Pos()).Offset,
				end:   fset.Position(se.Low.End()).Offset,
				text:  []byte(low),
			})
		}
		if high, omit, ok := modernizeLenHighBound(fset, se.X, se.High); ok {
			if omit {
				local = append(local, sourceEdit{
					start: fset.Position(se.High.Pos()).Offset,
					end:   fset.Position(se.High.End()).Offset,
				})
			} else {
				local = append(local, sourceEdit{
					start: fset.Position(se.High.Pos()).Offset,
					end:   fset.Position(se.High.End()).Offset,
					text:  []byte(high),
				})
			}
		}
		if len(local) > 0 {
			edits = append(edits, local...)
			count++
		}
		return true
	})
	return edits, count
}

func modernizeLenMinusBound(fset *token.FileSet, slice ast.Expr, bound ast.Expr) (string, bool) {
	if bound == nil {
		return "", false
	}
	be, ok := ast.Unparen(bound).(*ast.BinaryExpr)
	if !ok || be.Op != token.SUB {
		return "", false
	}
	if !modernizeIsLenOf(fset, be.X, slice) {
		return "", false
	}
	return modernizeNegativeLiteral(be.Y)
}

func modernizeLenHighBound(fset *token.FileSet, slice ast.Expr, bound ast.Expr) (string, bool, bool) {
	if bound == nil {
		return "", false, false
	}
	if modernizeIsLenOf(fset, bound, slice) {
		return "", true, true
	}
	be, ok := ast.Unparen(bound).(*ast.BinaryExpr)
	if !ok || be.Op != token.SUB {
		return "", false, false
	}
	if !modernizeIsLenOf(fset, be.X, slice) {
		return "", false, false
	}
	neg, ok := modernizeNegativeLiteral(be.Y)
	return neg, false, ok
}

func modernizeIsLenOf(fset *token.FileSet, call ast.Expr, slice ast.Expr) bool {
	ce, ok := ast.Unparen(call).(*ast.CallExpr)
	if !ok {
		return false
	}
	fn, ok := ce.Fun.(*ast.Ident)
	if !ok || fn.Name != "len" || len(ce.Args) != 1 {
		return false
	}
	return modernizeExprEqual(fset, ce.Args[0], slice)
}

func modernizeExprEqual(fset *token.FileSet, a, b ast.Expr) bool {
	var ba, bb bytes.Buffer
	if err := format.Node(&ba, fset, a); err != nil {
		return false
	}
	if err := format.Node(&bb, fset, b); err != nil {
		return false
	}
	return ba.String() == bb.String()
}

func modernizeNegativeLiteral(e ast.Expr) (string, bool) {
	bl, ok := ast.Unparen(e).(*ast.BasicLit)
	if !ok || bl.Kind != token.INT {
		return "", false
	}
	if len(bl.Value) > 0 && bl.Value[0] == '-' {
		return "", false
	}
	return "-" + bl.Value, true
}
