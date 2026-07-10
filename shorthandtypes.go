package main

import (
	"go/ast"
	"go/token"
)

func modernizeShorthandTypes(f *ast.File) int {
	count := 0
	f.Decls = rewriteShorthandDecls(f.Decls, &count)
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		rewriteShorthandStmts(fn.Body.List, &count)
		return true
	})
	return count
}

func rewriteShorthandStmts(stmts []ast.Stmt, count *int) {
	for i := range stmts {
		switch s := stmts[i].(type) {
		case *ast.DeclStmt:
			if converted := convertTypeGenDecl(s.Decl); len(converted) == 1 {
				s.Decl = converted[0]
				*count++
			}
		case *ast.BlockStmt:
			rewriteShorthandStmts(s.List, count)
		}
	}
}

func rewriteShorthandDecls(decls []ast.Decl, count *int) []ast.Decl {
	if len(decls) == 0 {
		return decls
	}
	var out []ast.Decl
	for _, d := range decls {
		if converted := convertTypeGenDecl(d); len(converted) > 0 {
			out = append(out, converted...)
			*count += len(converted)
			continue
		}
		out = append(out, d)
	}
	return out
}

func convertTypeGenDecl(d ast.Decl) []ast.Decl {
	gd, ok := d.(*ast.GenDecl)
	if !ok || gd.Tok != token.TYPE || gd.Lparen.IsValid() || len(gd.Specs) == 0 {
		return nil
	}

	var converted []ast.Decl
	for i, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			return nil
		}
		if ts.Assign.IsValid() || ts.TypeParams != nil {
			return nil
		}
		doc := ts.Doc
		if doc == nil && i == 0 {
			doc = gd.Doc
		}
		switch st := ts.Type.(type) {
		case *ast.StructType:
			if st.Fields == nil || !st.Fields.Opening.IsValid() {
				return nil
			}
			converted = append(converted, &ast.StructDecl{
				Doc:    doc,
				Struct: st.Struct,
				Name:   ts.Name,
				Fields: st.Fields,
				Rbrace: st.Fields.Closing,
			})
		case *ast.InterfaceType:
			if st.Methods == nil || !st.Methods.Opening.IsValid() {
				return nil
			}
			converted = append(converted, &ast.InterfaceDecl{
				Doc:       doc,
				Interface: st.Interface,
				Name:      ts.Name,
				Methods:   st.Methods,
				Rbrace:    st.Methods.Closing,
			})
		default:
			return nil
		}
	}
	return converted
}
