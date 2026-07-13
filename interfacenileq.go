package main

import (
	"bytes"
	"go/ast"
	"go/importer"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
)

const interfaceNilEqFixme = "//FIXME: Make sure still works after interface == nil change."

func labelInterfaceNilComparisons(fset *token.FileSet, files []*ast.File, importPath string) map[int][]sourceEdit {
	out := map[int][]sourceEdit{}
	if len(files) == 0 {
		return out
	}
	info, ok := typecheckFiles(fset, files, importPath)
	if !ok {
		return out
	}
	for fi, f := range files {
		if f == nil {
			continue
		}
		seen := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok || (be.Op != token.EQL && be.Op != token.NEQ) {
				return true
			}
			var iface ast.Expr
			switch {
			case isNilExpr(be.Y):
				iface = be.X
			case isNilExpr(be.X):
				iface = be.Y
			default:
				return true
			}
			tv, ok := info.Types[iface]
			if !ok || tv.Type == nil {
				return true
			}
			if _, ok := tv.Type.Underlying().(*types.Interface); !ok {
				return true
			}
			if tv.Type == types.Universe.Lookup("error").Type() {
				return true
			}
			pos := fset.Position(be.Pos())
			key := pos.String()
			if seen[key] {
				return true
			}
			seen[key] = true
			lineStart := lineStartOffset(fset, f, pos.Line)
			if lineStart < 0 {
				return true
			}
			if hasInterfaceNilEqFixme(fset, f, lineStart) {
				return true
			}
			out[fi] = append(out[fi], sourceEdit{
				start: lineStart,
				end:   lineStart,
				text:  []byte(interfaceNilEqFixme + "\n"),
			})
			return true
		})
	}
	return out
}

func typecheckFiles(fset *token.FileSet, files []*ast.File, importPath string) (*types.Info, bool) {
	if importPath == "" {
		importPath = "p"
	}
	var parsed []*ast.File
	for _, f := range files {
		if f != nil {
			parsed = append(parsed, f)
		}
	}
	if len(parsed) == 0 {
		return nil, false
	}
	cfg := &types.Config{Importer: importer.Default()}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	_, err := cfg.Check(importPath, fset, parsed, info)
	if err != nil && len(info.Types) == 0 {
		return nil, false
	}
	return info, true
}

func hasInterfaceNilEqFixme(fset *token.FileSet, f *ast.File, lineStart int) bool {
	tf := fset.File(f.Pos())
	if tf == nil {
		return false
	}
	data, err := os.ReadFile(tf.Name())
	if err != nil || lineStart > len(data) {
		return false
	}
	prefix := data[:lineStart]
	if bytes.HasSuffix(prefix, []byte(interfaceNilEqFixme + "\n")) {
		return true
	}
	// Also skip if FIXME is on the same line before the comparison.
	pos := tf.Pos(lineStart)
	line := tf.Position(pos).Line
	lineOff := tf.Offset(tf.LineStart(line))
	if lineOff < lineStart {
		if bytes.Contains(data[lineOff:lineStart], []byte(interfaceNilEqFixme)) {
			return true
		}
	}
	return false
}

func lineStartOffset(fset *token.FileSet, f *ast.File, line int) int {
	if f == nil || line <= 0 {
		return -1
	}
	fname := fset.File(f.Pos())
	if fname == nil {
		return -1
	}
	offset := fname.Offset(fname.LineStart(line))
	return offset
}

func packageImportPath(modRoot, pkgDir string) string {
	modPath, err := readModulePath(modRoot)
	if err != nil || modPath == "" {
		return ""
	}
	rel, err := filepath.Rel(modRoot, pkgDir)
	if err != nil || rel == "." {
		return modPath
	}
	return modPath + "/" + strings.ReplaceAll(rel, "\\", "/")
}
