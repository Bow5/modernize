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
