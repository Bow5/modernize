package main

import (
	"go/ast"
	"go/token"
)

func shorthandSliceRewriteOK(lit *ast.CompositeLit) bool {
	t, ok := lit.Type.(*ast.ArrayType)
	if !ok || t.Len != nil {
		return false
	}
	if len(lit.Elts) == 0 {
		// Empty []/map literals lose their type prefix; shorthand [] and {}
		// are not always inferable from context (e.g. targets := [] then append).
		return false
	}
	inferred, ok := inferShorthandElemTypes(lit.Elts)
	if !ok {
		return false
	}
	return shorthandTypesMatch(t.Elt, inferred)
}

func shorthandMapRewriteOK(lit *ast.CompositeLit) bool {
	t, ok := lit.Type.(*ast.MapType)
	if !ok {
		return false
	}
	if len(lit.Elts) == 0 {
		// Empty []/map literals lose their type prefix; shorthand [] and {}
		// are not always inferable from context (e.g. targets := [] then append).
		return false
	}
	var keyInferred, valInferred ast.Expr
	for i, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return false
		}
		k, ok := inferredTypeFromExpr(kv.Key)
		if !ok {
			return false
		}
		v, ok := inferredTypeFromExpr(kv.Value)
		if !ok {
			return false
		}
		if i == 0 {
			keyInferred, valInferred = k, v
			continue
		}
		keyInferred, ok = commonShorthandInferredType(keyInferred, k)
		if !ok {
			return false
		}
		valInferred, ok = commonShorthandInferredType(valInferred, v)
		if !ok {
			return false
		}
	}
	return shorthandTypesMatch(t.Key, keyInferred) && shorthandTypesMatch(t.Value, valInferred)
}

func shorthandSetOfRewriteOK(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	if typeArg, ok := setOfTypeArg(call); ok {
		inferred, ok := inferShorthandElemTypes(call.Args)
		if !ok {
			return false
		}
		return shorthandTypesMatch(typeArg, inferred)
	}
	inferred, ok := inferShorthandElemTypes(call.Args)
	if !ok {
		return false
	}
	return inferred != nil
}

func setOfTypeArg(call *ast.CallExpr) (ast.Expr, bool) {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.IndexExpr:
		if sel, ok := fun.X.(*ast.SelectorExpr); ok && isSetIdent(sel.X) && sel.Sel.Name == "Of" {
			return fun.Index, true
		}
	case *ast.IndexListExpr:
		if len(fun.Indices) == 1 {
			if sel, ok := fun.X.(*ast.SelectorExpr); ok && isSetIdent(sel.X) && sel.Sel.Name == "Of" {
				return fun.Indices[0], true
			}
		}
	}
	return nil, false
}

func inferShorthandElemTypes(elems []ast.Expr) (ast.Expr, bool) {
	var inferred ast.Expr
	for _, el := range elems {
		typ, ok := inferredTypeFromExpr(el)
		if !ok {
			return nil, false
		}
		if inferred == nil {
			inferred = typ
			continue
		}
		inferred, ok = commonShorthandInferredType(inferred, typ)
		if !ok {
			return nil, false
		}
	}
	return inferred, true
}

func inferredTypeFromExpr(e ast.Expr) (ast.Expr, bool) {
	e = ast.Unparen(e)
	switch e := e.(type) {
	case *ast.BasicLit:
		switch e.Kind {
		case token.INT, token.IMAG:
			return &ast.Ident{Name: "int"}, true
		case token.FLOAT:
			return &ast.Ident{Name: "float64"}, true
		case token.STRING:
			return &ast.Ident{Name: "string"}, true
		case token.CHAR:
			return &ast.Ident{Name: "rune"}, true
		}
	case *ast.CompositeLit:
		if e.Type == nil {
			return nil, false
		}
		if typ := compositeLitDeclaredType(e.Type); typ != nil {
			return typ, true
		}
	case *ast.CallExpr:
		if typ := typeConversionTarget(e.Fun); typ != nil {
			return typ, true
		}
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			if inner, ok := inferredTypeFromExpr(e.X); ok {
				return &ast.StarExpr{X: inner}, true
			}
		}
	}
	return nil, false
}

func compositeLitDeclaredType(t ast.Expr) ast.Expr {
	switch t := ast.Unparen(t).(type) {
	case *ast.Ident, *ast.StarExpr, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
		return t
	default:
		return nil
	}
}

func typeConversionTarget(fun ast.Expr) ast.Expr {
	switch fun := ast.Unparen(fun).(type) {
	case *ast.Ident:
		if isTypeNameIdent(fun.Name) {
			return fun
		}
	case *ast.StarExpr:
		if id, ok := fun.X.(*ast.Ident); ok && isTypeNameIdent(id.Name) {
			return fun
		}
	case *ast.SelectorExpr:
		return fun
	}
	return nil
}

func isTypeNameIdent(name string) bool {
	switch name {
	case "bool", "string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"byte", "rune", "float32", "float64", "complex64", "complex128":
		return true
	default:
		return false
	}
}

func commonShorthandInferredType(a, b ast.Expr) (ast.Expr, bool) {
	if shorthandTypeExprEqual(a, b) {
		return a, true
	}
	return nil, false
}

func shorthandTypesMatch(declared, inferred ast.Expr) bool {
	if isInterfaceTypeExpr(declared) {
		return false
	}
	return shorthandTypeExprEqual(declared, inferred)
}

func isInterfaceTypeExpr(t ast.Expr) bool {
	switch t := ast.Unparen(t).(type) {
	case *ast.InterfaceType:
		return true
	case *ast.Ident:
		return t.Name == "any"
	default:
		return false
	}
}

func shorthandTypeExprEqual(a, b ast.Expr) bool {
	a, b = ast.Unparen(a), ast.Unparen(b)
	switch at := a.(type) {
	case *ast.Ident:
		bt, ok := b.(*ast.Ident)
		return ok && at.Name == bt.Name
	case *ast.StarExpr:
		bt, ok := b.(*ast.StarExpr)
		if !ok {
			return false
		}
		return shorthandTypeExprEqual(at.X, bt.X)
	case *ast.SelectorExpr:
		bt, ok := b.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		return selectorExprEqual(at, bt)
	case *ast.IndexExpr:
		bt, ok := b.(*ast.IndexExpr)
		if !ok {
			return false
		}
		return shorthandTypeExprEqual(at.X, bt.X) && shorthandTypeExprEqual(at.Index, bt.Index)
	case *ast.IndexListExpr:
		bt, ok := b.(*ast.IndexListExpr)
		if !ok || len(at.Indices) != len(bt.Indices) {
			return false
		}
		if !shorthandTypeExprEqual(at.X, bt.X) {
			return false
		}
		for i := range at.Indices {
			if !shorthandTypeExprEqual(at.Indices[i], bt.Indices[i]) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func selectorExprEqual(a, b *ast.SelectorExpr) bool {
	if a.Sel.Name != b.Sel.Name {
		return false
	}
	ax, ok := a.X.(*ast.Ident)
	if !ok {
		return shorthandTypeExprEqual(a.X, b.X)
	}
	bx, ok := b.X.(*ast.Ident)
	return ok && ax.Name == bx.Name
}
