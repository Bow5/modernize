package main

import (
	"go/ast"
	"go/token"
)

func isChanTypeExpr(t ast.Expr) bool {
	switch t := ast.Unparen(t).(type) {
	case *ast.ChanType:
		return true
	case *ast.Ident:
		return t.Name == "chan"
	case *ast.StarExpr:
		return isChanTypeExpr(t.X)
	case *ast.ParenExpr:
		return isChanTypeExpr(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name == "Chan" || t.Sel.Name == "chan"
	default:
		return false
	}
}

func fieldListHasChanNamed(list *ast.FieldList, name string) bool {
	if list == nil {
		return false
	}
	for _, field := range list.List {
		if !isChanTypeExpr(field.Type) {
			continue
		}
		for _, id := range field.Names {
			if id.Name == name {
				return true
			}
		}
	}
	return false
}

func fileDeclIsChanType(f *ast.File, name string) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GenDecl:
			if n.Tok != token.TYPE {
				return true
			}
			for _, spec := range n.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				if fieldListHasChanNamed(st.Fields, name) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func fileFuncParamIsChanType(f *ast.File, name string) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		if fieldListHasChanNamed(fd.Type.Params, name) {
			found = true
			return false
		}
		return true
	})
	return found
}

func fileStructFieldIsChan(f *ast.File, sel *ast.SelectorExpr) bool {
	field := sel.Sel.Name
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Type == nil {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		if fieldListHasChanNamed(st.Fields, field) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isMakeChanCall(x ast.Expr) bool {
	call, ok := ast.Unparen(x).(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return fun.Name == "make" && len(call.Args) > 0 && isChanTypeExpr(call.Args[0])
	case *ast.SelectorExpr:
		return fun.Sel.Name == "make" && len(call.Args) > 0 && isChanTypeExpr(call.Args[0])
	default:
		return false
	}
}

func assignDefinesChanIdent(lhs ast.Expr, rhs ast.Expr, name string) bool {
	id, ok := ast.Unparen(lhs).(*ast.Ident)
	if !ok || id.Name != name {
		return false
	}
	return isMakeChanCall(rhs)
}

func identAssignedFromMakeChan(f *ast.File, name string, usePos token.Pos) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		if !n.Pos().IsValid() || n.Pos() >= usePos {
			return true
		}
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			if stmt.Tok != token.DEFINE && stmt.Tok != token.ASSIGN {
				return true
			}
			for i, lhs := range stmt.Lhs {
				if i >= len(stmt.Rhs) {
					break
				}
				if assignDefinesChanIdent(lhs, stmt.Rhs[i], name) {
					found = true
					return false
				}
			}
		case *ast.RangeStmt:
			// Stop at inner ranges; only inspect stmts at same block level via parent walk.
			return true
		}
		return true
	})
	return found
}

func callExprLooksLikeChannelStream(x ast.Expr) bool {
	call, ok := ast.Unparen(x).(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Stream"
}
