// modernize rewrites (T, error) functions and if err != nil early returns
// to T! and expr! syntax for the Go language fork.
package main

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := "minio"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	var changed int
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		c, e := modernizeFile(path)
		if e != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, e)
			return nil
		}
		if c {
			changed++
			fmt.Println(path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "modernized %d files\n", changed)
}

func modernizeFile(path string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return false, err
	}

	mod := &fileModernizer{fset: fset, file: f}
	ast.Inspect(f, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok {
			mod.simplifyNilReturnsInFunc(fn)
			mod.propagateReturnErrInFunc(fn)
		}
		return true
	})

	if !mod.changed {
		return false, nil
	}

	var out strings.Builder
	if err := format.Node(&out, fset, f); err != nil {
		return false, err
	}
	newSrc := out.String()
	if newSrc == string(src) {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(newSrc), 0)
}

type fileModernizer struct {
	fset    *token.FileSet
	file    *ast.File
	changed bool
}

func (m *fileModernizer) simplifyNilReturnsInFunc(fn *ast.FuncDecl) {
	if fn.Type == nil || fn.Body == nil || !hasResultTypeBang(fn.Type) {
		return
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 2 || !isNilIdent(ret.Results[1]) {
			return true
		}
		ret.Results = []ast.Expr{ret.Results[0]}
		m.mark()
		return true
	})
}

func hasResultTypeBang(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	_, ok := ft.Results.List[0].Type.(*ast.ResultTypeExpr)
	return ok
}

func canPropagateWithBang(ft *ast.FuncType) bool {
	if hasResultTypeBang(ft) {
		return true
	}
	if ft.Results != nil && len(ft.Results.List) == 1 {
		return isErrorType(ft.Results.List[0].Type)
	}
	return false
}

func (m *fileModernizer) propagateReturnErrInFunc(fn *ast.FuncDecl) {
	if fn.Type == nil || fn.Body == nil || !canPropagateWithBang(fn.Type) {
		return
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if b, ok := n.(*ast.BlockStmt); ok {
			m.propagateReturnErrBody(b)
		}
		return true
	})
}

func (m *fileModernizer) propagateReturnErrBody(body *ast.BlockStmt) {
	if body == nil {
		return
	}
	var newList []ast.Stmt
	for i := 0; i < len(body.List); i++ {
		stmt := body.List[i]
		if tryStmt, ok := m.matchIfInitReturnErr(stmt, body.List, i); ok {
			newList = append(newList, tryStmt)
			m.mark()
			continue
		}
		if tryStmt, ok := m.matchAssignReturnErr(stmt, body.List, i); ok {
			newList = append(newList, tryStmt)
			i += 1
			m.mark()
			continue
		}
		newList = append(newList, stmt)
	}
	body.List = newList
}

func stmtUsesIdent(stmt ast.Stmt, name string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func errUsedInStmts(list []ast.Stmt, start int, errName string) bool {
	for j := start; j < len(list); j++ {
		if stmtUsesIdent(list[j], errName) {
			return true
		}
	}
	return false
}

// if err := call(); err != nil { return err }
func (m *fileModernizer) matchIfInitReturnErr(stmt ast.Stmt, list []ast.Stmt, i int) (ast.Stmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Else != nil || ifStmt.Init == nil {
		return nil, false
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Rhs) != 1 {
		return nil, false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	var errLHS *ast.Ident
	switch len(assign.Lhs) {
	case 1:
		errLHS, ok = assign.Lhs[0].(*ast.Ident)
		if !ok {
			return nil, false
		}
	case 2:
		if !isBlank(assign.Lhs[0]) {
			return nil, false
		}
		errLHS, ok = assign.Lhs[1].(*ast.Ident)
		if !ok {
			return nil, false
		}
	default:
		return nil, false
	}
	errName, ok := errIdentFromCond(ifStmt.Cond)
	if !ok || errName != errLHS.Name {
		return nil, false
	}
	if len(ifStmt.Body.List) != 1 {
		return nil, false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || !isReturnErrOnly(ret, errName) {
		return nil, false
	}
	if errUsedInStmts(list, i+1, errName) {
		return nil, false
	}
	return &ast.ExprStmt{X: &ast.TryExpr{X: call}}, true
}

func isReturnErrOnly(ret *ast.ReturnStmt, errName string) bool {
	return len(ret.Results) == 1 && isErrIdent(ret.Results[0], errName)
}

// err := call(); if err != nil { return err }
func (m *fileModernizer) matchAssignReturnErr(asg ast.Stmt, list []ast.Stmt, i int) (ast.Stmt, bool) {
	assign, ok := asg.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Rhs) != 1 {
		return nil, false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	var errLHS *ast.Ident
	switch len(assign.Lhs) {
	case 1:
		errLHS, ok = assign.Lhs[0].(*ast.Ident)
		if !ok {
			return nil, false
		}
	case 2:
		if !isBlank(assign.Lhs[0]) {
			return nil, false
		}
		errLHS, ok = assign.Lhs[1].(*ast.Ident)
		if !ok {
			return nil, false
		}
	default:
		return nil, false
	}
	if i+1 >= len(list) {
		return nil, false
	}
	ifStmt, ok := list[i+1].(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil {
		return nil, false
	}
	errName, ok := errIdentFromCond(ifStmt.Cond)
	if !ok || errName != errLHS.Name {
		return nil, false
	}
	if len(ifStmt.Body.List) != 1 {
		return nil, false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || !isReturnErrOnly(ret, errName) {
		return nil, false
	}
	if errUsedInStmts(list, i+2, errName) {
		return nil, false
	}
	return &ast.ExprStmt{X: &ast.TryExpr{X: call}}, true
}

func (m *fileModernizer) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.FuncDecl:
		m.modernizeFunc(n)
		return nil
	case *ast.InterfaceType:
		m.modernizeInterface(n)
		return nil
	}
	return m
}

func (m *fileModernizer) mark() {
	m.changed = true
}

func isErrorType(t ast.Expr) bool {
	ident, ok := t.(*ast.Ident)
	return ok && ident.Name == "error"
}

func resultPair(ft *ast.FuncType) (valueType ast.Expr, ok bool) {
	if ft.Results == nil || len(ft.Results.List) != 2 {
		return nil, false
	}
	first := ft.Results.List[0]
	second := ft.Results.List[1]
	if len(first.Names) > 0 || len(second.Names) > 0 {
		return nil, false
	}
	if !isErrorType(second.Type) {
		return nil, false
	}
	if !isSupportedResultType(first.Type) {
		return nil, false
	}
	return first.Type, true
}

func isSupportedResultType(t ast.Expr) bool {
	switch ast.Unparen(t).(type) {
	case *ast.MapType, *ast.ChanType:
		return false
	}
	return true
}

func (m *fileModernizer) convertResultType(ft *ast.FuncType) bool {
	vt, ok := resultPair(ft)
	if !ok {
		return false
	}
	ft.Results = &ast.FieldList{
		List: []*ast.Field{{
			Type: &ast.ResultTypeExpr{X: astutilClone(vt)},
		}},
	}
	m.mark()
	return true
}

func (m *fileModernizer) modernizeFunc(fn *ast.FuncDecl) {
	if fn.Type == nil {
		return
	}
	if !m.convertResultType(fn.Type) {
		return
	}
	vt, ok := m.valueResultType(fn.Type)
	if !ok {
		return
	}
	if fn.Body != nil {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			if b, ok := n.(*ast.BlockStmt); ok {
				m.modernizeBody(b, vt)
			}
			return true
		})
	}
}

func (m *fileModernizer) valueResultType(ft *ast.FuncType) (ast.Expr, bool) {
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return nil, false
	}
	if len(ft.Results.List) == 1 {
		if rt, ok := ft.Results.List[0].Type.(*ast.ResultTypeExpr); ok {
			return rt.X, true
		}
	}
	return resultPair(ft)
}

func (m *fileModernizer) modernizeInterface(it *ast.InterfaceType) {
	if it.Methods == nil {
		return
	}
	for _, field := range it.Methods.List {
		ft, ok := field.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		m.convertResultType(ft)
	}
}

func (m *fileModernizer) modernizeBody(body *ast.BlockStmt, vt ast.Expr) {
	if body == nil {
		return
	}
	var newList []ast.Stmt
	for i := 0; i < len(body.List); i++ {
		stmt := body.List[i]
		if asg, ok := m.matchAssignErrCheck(stmt, body.List, i, vt); ok {
			newList = append(newList, asg)
			i += 1
			m.mark()
			continue
		}
		if tryStmt, ok := m.matchIfInitErr(stmt, vt); ok {
			newList = append(newList, tryStmt)
			m.mark()
			continue
		}
		if tryStmt, ok := m.matchIfAssignErr(stmt, vt); ok {
			newList = append(newList, tryStmt)
			m.mark()
			continue
		}
		newList = append(newList, stmt)
	}
	body.List = newList
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

func errIdentFromCond(cond ast.Expr) (string, bool) {
	be, ok := cond.(*ast.BinaryExpr)
	if !ok {
		return "", false
	}
	if be.Op != token.NEQ {
		return "", false
	}
	if id, ok := be.X.(*ast.Ident); ok && isNilIdent(be.Y) {
		return id.Name, true
	}
	if id, ok := be.Y.(*ast.Ident); ok && isNilIdent(be.X) {
		return id.Name, true
	}
	return "", false
}

func isZeroReturn(ret *ast.ReturnStmt, vt ast.Expr, errName string) bool {
	if len(ret.Results) != 2 {
		return false
	}
	if !isErrIdent(ret.Results[1], errName) {
		return false
	}
	return zeroMatches(ret.Results[0], vt)
}

func isErrIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func zeroMatches(z, vt ast.Expr) bool {
	z = ast.Unparen(z)
	switch z := z.(type) {
	case *ast.BasicLit:
		return z.Value == "0" || z.Value == "0.0" || z.Value == "false" || z.Value == "nil" || z.Value == "\"\""
	case *ast.Ident:
		if z.Name == "nil" {
			return true
		}
		if id, ok := vt.(*ast.Ident); ok {
			return z.Name == id.Name+"{}"
		}
		return false
	case *ast.CompositeLit:
		return true
	case *ast.CallExpr:
		if sel, ok := z.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "make" {
			return true
		}
		return false
	default:
		return false
	}
}

// if err := call(); err != nil { return zero, err }
func (m *fileModernizer) matchIfInitErr(stmt ast.Stmt, vt ast.Expr) (ast.Stmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Else != nil || ifStmt.Init == nil {
		return nil, false
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE {
		return nil, false
	}
	if len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
		errLHS, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return nil, false
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return nil, false
		}
		errName, ok := errIdentFromCond(ifStmt.Cond)
		if !ok || errName != errLHS.Name {
			return nil, false
		}
		if len(ifStmt.Body.List) != 1 {
			return nil, false
		}
		ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
		if !ok || !isZeroReturn(ret, vt, errName) {
			return nil, false
		}
		return &ast.ExprStmt{X: &ast.TryExpr{X: call}}, true
	}
	if len(assign.Lhs) == 2 && len(assign.Rhs) == 1 {
		errLHS, ok := assign.Lhs[1].(*ast.Ident)
		if !ok {
			return nil, false
		}
		valLHS := assign.Lhs[0]
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return nil, false
		}
		errName, ok := errIdentFromCond(ifStmt.Cond)
		if !ok || errName != errLHS.Name {
			return nil, false
		}
		if len(ifStmt.Body.List) != 1 {
			return nil, false
		}
		ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
		if !ok || !isZeroReturn(ret, vt, errName) {
			return nil, false
		}
		if isBlank(valLHS) {
			return &ast.ExprStmt{X: &ast.TryExpr{X: call}}, true
		}
		return &ast.AssignStmt{
			Tok: token.DEFINE,
			Lhs: []ast.Expr{astutilClone(valLHS)},
			Rhs: []ast.Expr{&ast.TryExpr{X: call}},
		}, true
	}
	return nil, false
}

// if err = call(); err != nil { return zero, err } — error-only call
func (m *fileModernizer) matchIfAssignErr(stmt ast.Stmt, vt ast.Expr) (ast.Stmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Else != nil || ifStmt.Init == nil {
		return nil, false
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return nil, false
	}
	errLHS, ok := assign.Lhs[0].(*ast.Ident)
	if !ok {
		return nil, false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	errName, ok := errIdentFromCond(ifStmt.Cond)
	if !ok || errName != errLHS.Name {
		return nil, false
	}
	if len(ifStmt.Body.List) != 1 {
		return nil, false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || !isZeroReturn(ret, vt, errName) {
		return nil, false
	}
	return &ast.ExprStmt{X: &ast.TryExpr{X: call}}, true
}

func (m *fileModernizer) matchAssignErrCheck(asg ast.Stmt, list []ast.Stmt, i int, vt ast.Expr) (ast.Stmt, bool) {
	assign, ok := asg.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
		return nil, false
	}
	errLHS, ok := assign.Lhs[1].(*ast.Ident)
	if !ok {
		return nil, false
	}
	valLHS := assign.Lhs[0]
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	if i+1 >= len(list) {
		return nil, false
	}
	ifStmt, ok := list[i+1].(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil {
		return nil, false
	}
	errName, ok := errIdentFromCond(ifStmt.Cond)
	if !ok || errName != errLHS.Name {
		return nil, false
	}
	if len(ifStmt.Body.List) != 1 {
		return nil, false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || !isZeroReturn(ret, vt, errName) {
		return nil, false
	}
	if isBlank(valLHS) {
		return &ast.ExprStmt{X: &ast.TryExpr{X: call}}, true
	}
	return &ast.AssignStmt{
		Tok: token.DEFINE,
		Lhs: []ast.Expr{astutilClone(valLHS)},
		Rhs: []ast.Expr{&ast.TryExpr{X: call}},
	}, true
}

func isBlank(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "_"
}

func astutilClone(e ast.Expr) ast.Expr {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		return &ast.Ident{Name: e.Name}
	case *ast.StarExpr:
		return &ast.StarExpr{X: astutilClone(e.X)}
	case *ast.ArrayType:
		return &ast.ArrayType{Len: astutilClone(e.Len), Elt: astutilClone(e.Elt)}
	case *ast.MapType:
		return &ast.MapType{Key: astutilClone(e.Key), Value: astutilClone(e.Value)}
	case *ast.SelectorExpr:
		return &ast.SelectorExpr{X: astutilClone(e.X), Sel: e.Sel}
	case *ast.IndexExpr:
		return &ast.IndexExpr{X: astutilClone(e.X), Index: astutilClone(e.Index)}
	case *ast.IndexListExpr:
		indices := make([]ast.Expr, len(e.Indices))
		for i, ix := range e.Indices {
			indices[i] = astutilClone(ix)
		}
		return &ast.IndexListExpr{X: astutilClone(e.X), Indices: indices}
	case *ast.InterfaceType:
		return &ast.InterfaceType{Methods: e.Methods}
	case *ast.StructType:
		return &ast.StructType{Fields: e.Fields}
	case *ast.ChanType:
		return &ast.ChanType{Dir: e.Dir, Value: astutilClone(e.Value)}
	case *ast.FuncType:
		return &ast.FuncType{Func: e.Func, Params: e.Params, Results: e.Results}
	case *ast.BasicLit:
		return &ast.BasicLit{Kind: e.Kind, Value: e.Value}
	case *ast.Ellipsis:
		return &ast.Ellipsis{Elt: astutilClone(e.Elt)}
	default:
		return e
	}
}
