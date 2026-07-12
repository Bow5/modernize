package main

import (
	"go/ast"
	"go/token"
	"go/types"
)

func modernizeForIn(f *ast.File, info *types.Info) int {
	count := 0
	ast.Inspect(f, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok || rs.Tok != token.DEFINE {
			return true
		}
		if rs.InPos.IsValid() {
			// Fix mistaken `for v, _ in seq` where v is a value, not an index.
			if rs.Value != nil && isBlankIdent(rs.Value) && rs.Key != nil && !isBlankIdent(rs.Key) {
				if singleRangeVarIsValue(info, rs.X) {
					v := rs.Key
					rs.Key = &ast.Ident{NamePos: v.Pos(), Name: "_"}
					rs.Value = v
					count++
				}
			}
			return true
		}
		if rs.Value == nil {
			if rs.Key == nil || isBlankIdent(rs.Key) {
				return true
			}
			if singleRangeVarIsValue(info, rs.X) {
				v := rs.Key
				rs.Key = &ast.Ident{NamePos: v.Pos(), Name: "_"}
				rs.Value = v
				rs.InPos = v.End()
				rs.Range = token.NoPos
				count++
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

// singleRangeVarIsValue reports whether `for v := range x` binds v to the
// iteration value (channel receive, iter.Seq) rather than an index/key.
func singleRangeVarIsValue(info *types.Info, x ast.Expr) bool {
	if v, ok := singleRangeVarIsValueType(info, x); ok {
		return v
	}
	return splitSeqRangeExpr(x)
}

func singleRangeVarIsValueType(info *types.Info, x ast.Expr) (bool, bool) {
	if info == nil || x == nil {
		return false, false
	}
	tv, ok := info.Types[x]
	if !ok || tv.Type == nil {
		return false, false
	}
	switch u := tv.Type.Underlying().(type) {
	case *types.Chan:
		return true, true
	case *types.Signature:
		if u.Params().Len() != 1 {
			return false, true
		}
		yield, ok := u.Params().At(0).Type().Underlying().(*types.Signature)
		if !ok {
			return false, true
		}
		return yield.Params().Len() == 1, true
	default:
		return false, true
	}
}

func splitSeqRangeExpr(x ast.Expr) bool {
	call, ok := ast.Unparen(x).(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := ast.Unparen(sel.X).(*ast.Ident)
	return ok && id.Name == "strings" && sel.Sel.Name == "SplitSeq"
}

func isBlankIdent(e ast.Expr) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	return ok && id.Name == "_"
}
