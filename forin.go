package main

import (
	"go/ast"
	"go/token"
)

func modernizeForIn(f *ast.File) int {
	count := 0
	ast.Inspect(f, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok || rs.InPos.IsValid() || rs.Tok != token.DEFINE {
			return true
		}
		if rs.Value == nil {
			if rs.Key == nil || isBlankIdent(rs.Key) {
				return true
			}
			// for k := range x -> for k, _ in x
			rs.Value = &ast.Ident{NamePos: rs.Key.End(), Name: "_"}
			rs.InPos = rs.Range
			rs.Range = token.NoPos
			count++
			return true
		}
		if !isBlankIdent(rs.Key) {
			if rs.Key == nil {
				return true
			}
			// for i, v := range x -> for i, v in x
			rs.InPos = rs.Value.End()
			rs.Range = token.NoPos
			count++
			return true
		}
		// for _, v := range x -> for v in x
		rs.Key = rs.Value
		rs.Value = nil
		rs.InPos = rs.Key.End()
		rs.Range = token.NoPos
		count++
		return true
	})
	return count
}

func isBlankIdent(e ast.Expr) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	return ok && id.Name == "_"
}
