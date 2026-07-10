package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

type moduleFuncIndex struct {
	byImportPath map[string]map[string]ast.Expr
}

func readModulePath(modRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(modRoot, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", nil
}

func buildModuleFuncIndex(modRoot string, pkgs []pkgFiles) (*moduleFuncIndex, error) {
	modPath, err := readModulePath(modRoot)
	if err != nil || modPath == "" {
		return &moduleFuncIndex{byImportPath: map[string]map[string]ast.Expr{}}, nil
	}
	idx := &moduleFuncIndex{byImportPath: map[string]map[string]ast.Expr{}}
	fset := token.NewFileSet()
	for _, pkg := range pkgs {
		importPath := modPath
		if rel, err := filepath.Rel(modRoot, pkg.dir); err == nil && rel != "." {
			importPath = modPath + "/" + filepath.ToSlash(rel)
		}
		for _, path := range pkg.paths {
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				continue
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Name == nil || fn.Type == nil {
					continue
				}
				res := singleResultType(fn.Type.Results)
				if res == nil {
					continue
				}
				if idx.byImportPath[importPath] == nil {
					idx.byImportPath[importPath] = map[string]ast.Expr{}
				}
				idx.byImportPath[importPath][fn.Name.Name] = res
			}
		}
	}
	return idx, nil
}

func importPathForIdent(f *ast.File, ident string) string {
	if f == nil {
		return ""
	}
	for _, decl := range f.Imports {
		if decl.Path == nil {
			continue
		}
		path := strings.Trim(decl.Path.Value, `"`)
		localName := filepath.Base(path)
		if decl.Name != nil {
			localName = decl.Name.Name
		}
		if localName == ident {
			return path
		}
	}
	return ""
}

func resolveCallResultType(local *returnTypeIndex, mod *moduleFuncIndex, f *ast.File, call *ast.CallExpr) ast.Expr {
	if call == nil {
		return nil
	}
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		if local != nil {
			return local.funcs[fun.Name]
		}
		return nil
	case *ast.SelectorExpr:
		if fun.Sel == nil {
			return nil
		}
		if pkg, ok := fun.X.(*ast.Ident); ok {
			if mod != nil && f != nil {
				if impPath := importPathForIdent(f, pkg.Name); impPath != "" {
					if pkgFuncs, ok := mod.byImportPath[impPath]; ok {
						if res, ok := pkgFuncs[fun.Sel.Name]; ok {
							return res
						}
					}
				}
			}
			if local != nil {
				if recvType := resolveExprResultType(local, mod, f, nil, fun.X); recvType != nil {
					if res, ok := local.methods[methodKey{recv: typeBaseName(recvType), name: fun.Sel.Name}]; ok {
						return res
					}
				}
				return local.funcs[fun.Sel.Name]
			}
		}
		if local != nil {
			if recvType := resolveExprResultType(local, mod, f, nil, fun.X); recvType != nil && fun.Sel != nil {
				if res, ok := local.methods[methodKey{recv: typeBaseName(recvType), name: fun.Sel.Name}]; ok {
					return res
				}
			}
		}
	}
	return nil
}
