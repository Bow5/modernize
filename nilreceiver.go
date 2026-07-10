package main

import (
	"go/ast"
	"go/token"
)

// removeNilReceiverGuards deletes `if recv == nil { return/panic … }` in pointer-receiver
// methods. In Bow, those branches are unreachable on direct calls.
func removeNilReceiverGuards(f *ast.File) int {
	count := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil {
			continue
		}
		if len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
			continue
		}
		if plainStarType(fn.Recv.List[0].Type) == nil {
			continue
		}
		recvName := fn.Recv.List[0].Names[0].Name
		fn.Body.List, count = stripNilReceiverGuards(fn.Body.List, recvName, count)
	}
	return count
}

func stripNilReceiverGuards(stmts []ast.Stmt, recvName string, count int) ([]ast.Stmt, int) {
	var out []ast.Stmt
	for _, stmt := range stmts {
		if ifs, ok := stmt.(*ast.IfStmt); ok && isNilReceiverGuard(ifs, recvName) {
			count++
			continue
		}
		out = append(out, stmt)
	}
	return out, count
}

func isNilReceiverGuard(ifStmt *ast.IfStmt, recvName string) bool {
	if ifStmt.Init != nil || ifStmt.Else != nil {
		return false
	}
	if !isRecvNilCondition(ifStmt.Cond, recvName) {
		return false
	}
	return isNilReceiverGuardBody(ifStmt.Body)
}

func isRecvNilCondition(cond ast.Expr, recvName string) bool {
	be, ok := ast.Unparen(cond).(*ast.BinaryExpr)
	if !ok || be.Op != token.EQL {
		return false
	}
	if id, ok := ast.Unparen(be.X).(*ast.Ident); ok && id.Name == recvName && isNilExpr(be.Y) {
		return true
	}
	if id, ok := ast.Unparen(be.Y).(*ast.Ident); ok && id.Name == recvName && isNilExpr(be.X) {
		return true
	}
	return false
}

func isNilReceiverGuardBody(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	for _, stmt := range body.List {
		switch s := stmt.(type) {
		case *ast.ReturnStmt:
			if len(s.Results) != 1 {
				return false
			}
			r := s.Results[0]
			if isNilExpr(r) {
				continue
			}
			if isZeroValueReturn(r) {
				continue
			}
			if call, ok := ast.Unparen(r).(*ast.CallExpr); ok {
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "new" && len(call.Args) == 1 {
					continue
				}
			}
			return false
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "panic" {
					continue
				}
			}
			return false
		default:
			return false
		}
	}
	return true
}

func isZeroValueReturn(e ast.Expr) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		switch x.Kind {
		case token.INT, token.FLOAT, token.IMAG, token.CHAR, token.STRING:
			return x.Value == "0" || x.Value == `""` || x.Value == "false"
		}
	case *ast.Ident:
		return x.Name == "false"
	case *ast.CompositeLit:
		return len(x.Elts) == 0
	}
	return false
}

type nilGuardIndex struct {
	methods map[methodKey]bool
}

func buildNilReceiverGuardIndex(files []*ast.File) *nilGuardIndex {
	idx := &nilGuardIndex{methods: map[methodKey]bool{}}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil || fn.Name == nil {
				continue
			}
			if len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
				continue
			}
			if plainStarType(fn.Recv.List[0].Type) == nil {
				continue
			}
			recvName := fn.Recv.List[0].Names[0].Name
			if !methodHasNilReceiverGuard(fn, recvName) {
				continue
			}
			recv := recvBaseName(fn.Recv)
			if recv == "" {
				continue
			}
			idx.methods[methodKey{recv: recv, name: fn.Name.Name}] = true
		}
	}
	return idx
}

func (idx *nilGuardIndex) has(recvType, methodName string) bool {
	if idx == nil || recvType == "" || methodName == "" {
		return false
	}
	return idx.methods[methodKey{recv: recvType, name: methodName}]
}

func methodHasNilReceiverGuard(fn *ast.FuncDecl, recvName string) bool {
	for _, stmt := range fn.Body.List {
		if ifs, ok := stmt.(*ast.IfStmt); ok && isNilReceiverGuard(ifs, recvName) {
			return true
		}
	}
	return false
}

type returnTypeIndex struct {
	funcs   map[string]ast.Expr
	methods map[methodKey]ast.Expr
}

type methodKey struct {
	recv string
	name string
}

func buildReturnTypeIndex(files []*ast.File) *returnTypeIndex {
	idx := &returnTypeIndex{
		funcs:   map[string]ast.Expr{},
		methods: map[methodKey]ast.Expr{},
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Type == nil {
				continue
			}
			res := singleResultType(fn.Type.Results)
			if res == nil {
				continue
			}
			if fn.Recv == nil {
				idx.funcs[fn.Name.Name] = res
				continue
			}
			recv := recvBaseName(fn.Recv)
			if recv == "" {
				continue
			}
			idx.methods[methodKey{recv: recv, name: fn.Name.Name}] = res
		}
	}
	return idx
}

func singleResultType(results *ast.FieldList) ast.Expr {
	if results == nil || len(results.List) != 1 {
		return nil
	}
	return results.List[0].Type
}

func recvBaseName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	return typeBaseName(recv.List[0].Type)
}

func typeBaseName(t ast.Expr) string {
	t = ast.Unparen(t)
	if ne, ok := t.(*ast.NilableTypeExpr); ok {
		t = ast.Unparen(ne.X)
	}
	if star, ok := t.(*ast.StarExpr); ok {
		return typeNameFromExpr(star.X)
	}
	return typeNameFromExpr(t)
}

func resolveCallResultType(idx *returnTypeIndex, call *ast.CallExpr) ast.Expr {
	if idx == nil || call == nil {
		return nil
	}
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return idx.funcs[fun.Name]
	case *ast.SelectorExpr:
		if fun.Sel == nil {
			return nil
		}
		if recvType := resolveExprResultType(idx, fun.X); recvType != nil {
			if res, ok := idx.methods[methodKey{recv: typeBaseName(recvType), name: fun.Sel.Name}]; ok {
				return res
			}
		}
		return idx.funcs[fun.Sel.Name]
	default:
		return nil
	}
}

func resolveExprResultType(idx *returnTypeIndex, e ast.Expr) ast.Expr {
	switch x := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		return resolveCallResultType(idx, x)
	case *ast.NullCondExpr:
		return resolveExprResultType(idx, x.X)
	case *ast.SelectorExpr:
		recvType := resolveExprResultType(idx, x.X)
		if recvType == nil || x.Sel == nil {
			return nil
		}
		return idx.methods[methodKey{recv: typeBaseName(recvType), name: x.Sel.Name}]
	default:
		return nil
	}
}

func resolveSelectorReceiverType(idx *returnTypeIndex, sel *ast.SelectorExpr) string {
	if idx == nil || sel == nil {
		return ""
	}
	res := resolveExprResultType(idx, sel.X)
	if res == nil {
		return ""
	}
	return typeBaseName(res)
}

// optionalMethodChains adds ?. only where it preserves upstream behavior: the
// called method has `if recv == nil { return nil / zero }`, which short-circuits
// the same way as ?. in Bow (nil receiver calls panic at the call site).
func optionalMethodChains(f *ast.File, files []*ast.File, guardIdx *nilGuardIndex) int {
	returns := buildReturnTypeIndex(files)
	count := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		count += processOptionalSelector(sel, returns, guardIdx)
		return true
	})
	return count
}

func processOptionalSelector(sel *ast.SelectorExpr, returns *returnTypeIndex, guardIdx *nilGuardIndex) int {
	if sel == nil || sel.Sel == nil {
		return 0
	}
	count := 0
	if innerCall, ok := ast.Unparen(sel.X).(*ast.CallExpr); ok {
		if innerSel, ok := innerCall.Fun.(*ast.SelectorExpr); ok {
			count += processOptionalSelector(innerSel, returns, guardIdx)
		}
	}
	recvType := resolveSelectorReceiverType(returns, sel)
	if !guardIdx.has(recvType, sel.Sel.Name) {
		return count
	}
	if alreadyNullCond(sel.X) {
		return count
	}
	sel.X = wrapNullCond(sel.X)
	return count + 1
}

func alreadyNullCond(e ast.Expr) bool {
	_, ok := ast.Unparen(e).(*ast.NullCondExpr)
	return ok
}

func wrapNullCond(e ast.Expr) ast.Expr {
	if alreadyNullCond(e) {
		return e
	}
	return &ast.NullCondExpr{
		X:    e,
		QPos: e.End(),
	}
}

func modernizeNilReceivers(f *ast.File, files []*ast.File, cfg Config) (guards, chains int) {
	guardIdx := buildNilReceiverGuardIndex(files)
	returns := buildReturnTypeIndex(files)
	if cfg.OptionalMethodChains {
		chains = optionalMethodChains(f, files, guardIdx)
		chains += nilablePointerChains(f, files, returns)
	}
	if cfg.RemoveNilReceiverGuards {
		guards = removeNilReceiverGuards(f)
	}
	return guards, chains
}

func isNilablePointerType(t ast.Expr) bool {
	ne, ok := ast.Unparen(t).(*ast.NilableTypeExpr)
	if !ok {
		return false
	}
	_, ok = ast.Unparen(ne.X).(*ast.StarExpr)
	return ok
}

type funcVarIndex struct {
	byFunc map[*ast.FuncDecl]map[string]ast.Expr
}

func buildFuncVarIndex(files []*ast.File, returns *returnTypeIndex) *funcVarIndex {
	idx := &funcVarIndex{byFunc: map[*ast.FuncDecl]map[string]ast.Expr{}}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			idx.byFunc[fn] = buildFuncVars(fn, returns)
		}
	}
	return idx
}

func buildFuncVars(fn *ast.FuncDecl, returns *returnTypeIndex) map[string]ast.Expr {
	vars := map[string]ast.Expr{}
	if fn.Type != nil && fn.Type.Params != nil {
		for _, tf := range flattenFields("param", fn.Name.Name, fn.Type.Params) {
			if tf.key.name != "" {
				vars[tf.key.name] = tf.typ
			}
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				id, ok := ast.Unparen(lhs).(*ast.Ident)
				if !ok {
					continue
				}
				var rhs ast.Expr
				if len(x.Rhs) == 1 {
					rhs = x.Rhs[0]
				} else {
					rhs = rhsAt(x.Rhs, i)
				}
				if typ := resolveExprType(returns, vars, rhs); typ != nil {
					vars[id.Name] = typ
				}
			}
		case *ast.GenDecl:
			if x.Tok != token.VAR {
				return true
			}
			for _, spec := range x.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if vs.Type != nil {
						vars[name.Name] = vs.Type
						continue
					}
					if i < len(vs.Values) {
						if typ := resolveExprType(returns, vars, vs.Values[i]); typ != nil {
							vars[name.Name] = typ
						}
					}
				}
			}
		}
		return true
	})
	return vars
}

func resolveExprType(returns *returnTypeIndex, vars map[string]ast.Expr, e ast.Expr) ast.Expr {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		return vars[x.Name]
	case *ast.CallExpr:
		return resolveCallResultType(returns, x)
	case *ast.NullCondExpr:
		return resolveExprType(returns, vars, x.X)
	default:
		return nil
	}
}

func receiverTypeForSelector(returns *returnTypeIndex, vars map[string]ast.Expr, sel *ast.SelectorExpr) ast.Expr {
	if sel == nil {
		return nil
	}
	return resolveExprType(returns, vars, sel.X)
}

// nilablePointerChains adds ?. where the receiver expression has type *T?.
func nilablePointerChains(f *ast.File, files []*ast.File, returns *returnTypeIndex) int {
	varIdx := buildFuncVarIndex(files, returns)
	count := 0
	var curFunc *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			curFunc = x
		case *ast.FuncLit:
			return false
		case *ast.SelectorExpr:
			vars := map[string]ast.Expr{}
			if curFunc != nil {
				vars = varIdx.byFunc[curFunc]
			}
			count += processNilableSelector(x, returns, vars)
		}
		return true
	})
	return count
}

func processNilableSelector(sel *ast.SelectorExpr, returns *returnTypeIndex, vars map[string]ast.Expr) int {
	if sel == nil || sel.Sel == nil {
		return 0
	}
	if !isNilablePointerType(receiverTypeForSelector(returns, vars, sel)) {
		return 0
	}
	if alreadyNullCond(sel.X) {
		return 0
	}
	sel.X = wrapNullCond(sel.X)
	return 1
}
