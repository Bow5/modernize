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
	funcs   map[string][]ast.Expr
	methods map[methodKey][]ast.Expr
}

type methodKey struct {
	recv string
	name string
}

func buildReturnTypeIndex(files []*ast.File) *returnTypeIndex {
	idx := &returnTypeIndex{
		funcs:   map[string][]ast.Expr{},
		methods: map[methodKey][]ast.Expr{},
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Type == nil {
				continue
			}
			res := flattenAllResultTypes(fn.Type.Results)
			if len(res) == 0 {
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

func flattenAllResultTypes(results *ast.FieldList) []ast.Expr {
	if results == nil {
		return nil
	}
	var out []ast.Expr
	for _, field := range results.List {
		n := max(1, len(field.Names))
		for i := 0; i < n; i++ {
			out = append(out, field.Type)
		}
	}
	return out
}

func resultTypeAt(types []ast.Expr, index int) ast.Expr {
	if index < 0 || index >= len(types) {
		return nil
	}
	return types[index]
}

func singleResultType(results *ast.FieldList) ast.Expr {
	return resultTypeAt(flattenAllResultTypes(results), 0)
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

func resolveExprResultType(idx *returnTypeIndex, mod *moduleFuncIndex, f *ast.File, vars map[string]ast.Expr, e ast.Expr) ast.Expr {
	switch x := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		return resolveCallResultTypeAt(idx, mod, f, nil, nil, vars, nil, x, 0)
	case *ast.NullCondExpr:
		return resolveExprResultType(idx, mod, f, vars, x.X)
	case *ast.SelectorExpr:
		recvType := resolveExprResultType(idx, mod, f, vars, x.X)
		if recvType == nil || x.Sel == nil || idx == nil {
			return nil
		}
		return resultTypeAt(idx.methods[methodKey{recv: typeBaseName(recvType), name: x.Sel.Name}], 0)
	case *ast.Ident:
		if vars != nil {
			return vars[x.Name]
		}
		return nil
	default:
		return nil
	}
}

func resolveCallResultTypeAt(returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, structs *structFieldIndex, fn *ast.FuncDecl, vars map[string]ast.Expr, pkgVars map[string]ast.Expr, call *ast.CallExpr, resultIndex int) ast.Expr {
	if call == nil || returns == nil {
		return nil
	}
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return resultTypeAt(returns.funcs[fun.Name], resultIndex)
	case *ast.SelectorExpr:
		if fun.Sel == nil {
			return nil
		}
		if pkg, ok := fun.X.(*ast.Ident); ok {
			impPath := ""
			if f != nil {
				impPath = importPathForIdent(f, pkg.Name)
			}
			if impPath != "" {
				if modIdx != nil {
					if pkgFuncs, ok := modIdx.byImportPath[impPath]; ok {
						return resultTypeAt([]ast.Expr{pkgFuncs[fun.Sel.Name]}, resultIndex)
					}
				}
				return nil
			}
		}
		recvType := resolveExprType(returns, modIdx, f, structs, fn, vars, pkgVars, fun.X)
		if recvType != nil {
			key := methodKey{recv: typeBaseName(recvType), name: fun.Sel.Name}
			if res := resultTypeAt(returns.methods[key], resultIndex); res != nil {
				return res
			}
		}
		return resultTypeAt(returns.funcs[fun.Sel.Name], resultIndex)
	}
	return nil
}

func resolveSelectorReceiverType(idx *returnTypeIndex, sel *ast.SelectorExpr) string {
	if idx == nil || sel == nil {
		return ""
	}
	res := resolveExprResultType(idx, nil, nil, nil, sel.X)
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

func modernizeNilReceivers(f *ast.File, files []*ast.File, cfg Config, modIdx *moduleFuncIndex) (guards, chains int) {
	guardIdx := buildNilReceiverGuardIndex(files)
	returns := buildReturnTypeIndex(files)
	if cfg.OptionalMethodChains {
		chains = optionalMethodChains(f, files, guardIdx)
		chains += nilablePointerChains(f, files, returns, modIdx)
		chains += nilableMethodGuards(f, files, returns, modIdx)
		chains += rewriteIfNilableChainConditions(f, files, returns, modIdx)
		chains += coalesceOptionalFieldsInCompositeLits(f, buildStructFieldIndex(files))
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

type structFieldIndex struct {
	fields map[string]map[string]ast.Expr
}

func buildStructFieldIndex(files []*ast.File) *structFieldIndex {
	idx := &structFieldIndex{fields: map[string]map[string]ast.Expr{}}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				if idx.fields[ts.Name.Name] == nil {
					idx.fields[ts.Name.Name] = map[string]ast.Expr{}
				}
				for _, field := range st.Fields.List {
					for _, name := range field.Names {
						idx.fields[ts.Name.Name][name.Name] = field.Type
					}
				}
			}
		}
	}
	return idx
}

func (idx *structFieldIndex) fieldType(typeName, field string) ast.Expr {
	if idx == nil || typeName == "" || field == "" {
		return nil
	}
	return idx.fields[typeName][field]
}

type funcVarIndex struct {
	byFunc  map[*ast.FuncDecl]map[string]ast.Expr
	fnFile  map[*ast.FuncDecl]*ast.File
	structs *structFieldIndex
	pkgVars map[string]ast.Expr
}

func buildPkgVarIndex(files []*ast.File) map[string]ast.Expr {
	vars := map[string]ast.Expr{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
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
						// Type inferred later from usage; leave unset for now.
					}
				}
			}
		}
	}
	return vars
}

func buildFuncVarIndex(files []*ast.File, returns *returnTypeIndex, modIdx *moduleFuncIndex) *funcVarIndex {
	structs := buildStructFieldIndex(files)
	pkgVars := buildPkgVarIndex(files)
	idx := &funcVarIndex{
		byFunc:  map[*ast.FuncDecl]map[string]ast.Expr{},
		fnFile:  map[*ast.FuncDecl]*ast.File{},
		structs: structs,
		pkgVars: pkgVars,
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			idx.byFunc[fn] = buildFuncVars(fn, f, returns, modIdx, structs, pkgVars)
			idx.fnFile[fn] = f
		}
	}
	return idx
}

func buildFuncVars(fn *ast.FuncDecl, f *ast.File, returns *returnTypeIndex, modIdx *moduleFuncIndex, structs *structFieldIndex, pkgVars map[string]ast.Expr) map[string]ast.Expr {
	vars := map[string]ast.Expr{}
	if fn.Recv != nil && len(fn.Recv.List) > 0 && len(fn.Recv.List[0].Names) > 0 {
		vars[fn.Recv.List[0].Names[0].Name] = fn.Recv.List[0].Type
	}
	if fn.Type != nil && fn.Type.Params != nil {
		for _, tf := range flattenFields("param", fn.Name.Name, fn.Type.Params) {
			if tf.key.name != "" {
				vars[tf.key.name] = tf.typ
			}
		}
	}
	if fn.Type != nil && fn.Type.Results != nil {
		for _, tf := range flattenFields("result", fn.Name.Name, fn.Type.Results) {
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
				if call, ok := ast.Unparen(rhs).(*ast.CallExpr); ok {
					if typ := resolveCallResultTypeAt(returns, modIdx, f, structs, fn, vars, pkgVars, call, i); typ != nil {
						vars[id.Name] = typ
						continue
					}
				}
				if typ := resolveExprType(returns, modIdx, f, structs, fn, vars, pkgVars, rhs); typ != nil {
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
						if typ := resolveExprType(returns, modIdx, f, structs, fn, vars, pkgVars, vs.Values[i]); typ != nil {
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

func fieldTypeFromReceiver(structs *structFieldIndex, recvTyp ast.Expr, field string) ast.Expr {
	if structs == nil {
		return nil
	}
	typeName := typeBaseName(recvTyp)
	if typeName == "" {
		return nil
	}
	return structs.fieldType(typeName, field)
}

func resolveExprType(returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, structs *structFieldIndex, fn *ast.FuncDecl, vars map[string]ast.Expr, pkgVars map[string]ast.Expr, e ast.Expr) ast.Expr {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		if vars != nil {
			if typ := vars[x.Name]; typ != nil {
				return typ
			}
		}
		if pkgVars != nil {
			return pkgVars[x.Name]
		}
		return nil
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if typ := vars[id.Name]; typ != nil {
				if ft := fieldTypeFromReceiver(structs, typ, x.Sel.Name); ft != nil {
					return ft
				}
			}
			if fn != nil && fn.Recv != nil && structs != nil {
				recvName, recvType, ok := recvNameAndType(fn.Recv)
				if ok && id.Name == recvName {
					return structs.fieldType(recvType, x.Sel.Name)
				}
			}
		}
		if inner, ok := x.X.(*ast.SelectorExpr); ok {
			innerTyp := resolveExprType(returns, modIdx, f, structs, fn, vars, pkgVars, inner)
			if ft := fieldTypeFromReceiver(structs, innerTyp, x.Sel.Name); ft != nil {
				return ft
			}
		}
		return nil
	case *ast.CallExpr:
		return resolveCallResultTypeAt(returns, modIdx, f, structs, fn, vars, pkgVars, x, 0)
	case *ast.NullCondExpr:
		return resolveExprType(returns, modIdx, f, structs, fn, vars, pkgVars, x.X)
	default:
		return nil
	}
}

func receiverExprType(returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, structs *structFieldIndex, fn *ast.FuncDecl, vars map[string]ast.Expr, pkgVars map[string]ast.Expr, expr ast.Expr) ast.Expr {
	if id, ok := ast.Unparen(expr).(*ast.Ident); ok {
		if typ := vars[id.Name]; typ != nil {
			return typ
		}
		return pkgVars[id.Name]
	}
	return resolveExprType(returns, modIdx, f, structs, fn, vars, pkgVars, expr)
}

func receiverTypeForSelector(returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, structs *structFieldIndex, fn *ast.FuncDecl, vars map[string]ast.Expr, pkgVars map[string]ast.Expr, sel *ast.SelectorExpr) ast.Expr {
	if sel == nil {
		return nil
	}
	return receiverExprType(returns, modIdx, f, structs, fn, vars, pkgVars, sel.X)
}

func selectorRootIdent(e ast.Expr) string {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		return x.Name
	case *ast.NullCondExpr:
		return selectorRootIdent(x.X)
	case *ast.SelectorExpr:
		return selectorRootIdent(x.X)
	case *ast.CallExpr:
		return ""
	default:
		return ""
	}
}

func copyBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func exitOnNilIdent(ifs *ast.IfStmt) (string, bool) {
	if ifs == nil || ifs.Init != nil || ifs.Else != nil {
		return "", false
	}
	id, nilCheck := identComparedToNil(ifs.Cond)
	if !nilCheck || !bodyOnlyExits(ifs.Body) {
		return "", false
	}
	return id, true
}

func identComparedToNil(cond ast.Expr) (string, bool) {
	be, ok := ast.Unparen(cond).(*ast.BinaryExpr)
	if !ok || be.Op != token.EQL {
		return "", false
	}
	if id, ok := ast.Unparen(be.X).(*ast.Ident); ok && isNilExpr(be.Y) {
		return id.Name, true
	}
	if id, ok := ast.Unparen(be.Y).(*ast.Ident); ok && isNilExpr(be.X) {
		return id.Name, true
	}
	return "", false
}

func bodyOnlyExits(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	for _, stmt := range body.List {
		switch s := stmt.(type) {
		case *ast.ReturnStmt:
			continue
		case *ast.BranchStmt:
			if s.Tok == token.BREAK || s.Tok == token.CONTINUE || s.Tok == token.GOTO {
				continue
			}
			return false
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok {
				return false
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "panic" {
				continue
			}
			return false
		default:
			return false
		}
	}
	return true
}

// nilablePointerChains adds ?. where the receiver expression has type *T?.
func nilablePointerChains(f *ast.File, files []*ast.File, returns *returnTypeIndex, modIdx *moduleFuncIndex) int {
	varIdx := buildFuncVarIndex(files, returns, modIdx)
	count := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		count += nilableChainsInBlock(fn, varIdx, returns, modIdx, f, fn.Body.List, map[string]bool{})
		count += rewriteFuncLitNilableChains(fn, varIdx, returns, modIdx, f, fn.Body.List, map[string]bool{})
	}
	return count
}

func rewriteFuncLitNilableChains(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, stmts []ast.Stmt, narrowed map[string]bool) int {
	count := 0
	var walk func([]ast.Stmt)
	walk = func(list []ast.Stmt) {
		for _, stmt := range list {
			ast.Inspect(stmt, func(n ast.Node) bool {
				fl, ok := n.(*ast.FuncLit)
				if !ok || fl.Body == nil {
					return true
				}
				count += nilableChainsInBlock(fn, varIdx, returns, modIdx, f, fl.Body.List, map[string]bool{})
				walk(fl.Body.List)
				return true
			})
		}
	}
	walk(stmts)
	return count
}

func assignOnNilIdent(ifs *ast.IfStmt) (string, bool) {
	if ifs == nil || ifs.Init != nil || ifs.Else != nil || ifs.Body == nil || len(ifs.Body.List) != 1 {
		return "", false
	}
	id, nilCheck := identComparedToNil(ifs.Cond)
	if !nilCheck {
		return "", false
	}
	assign, ok := ifs.Body.List[0].(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return "", false
	}
	lhs, ok := ast.Unparen(assign.Lhs[0]).(*ast.Ident)
	if !ok || lhs.Name != id || isNilExpr(assign.Rhs[0]) {
		return "", false
	}
	return id, true
}

func nonNilBranchIdent(ifs *ast.IfStmt) (string, bool) {
	if ifs == nil || ifs.Init != nil || ifs.Else != nil {
		return "", false
	}
	be, ok := ast.Unparen(ifs.Cond).(*ast.BinaryExpr)
	if !ok || be.Op != token.NEQ {
		return "", false
	}
	if id, ok := ast.Unparen(be.X).(*ast.Ident); ok && isNilExpr(be.Y) {
		return id.Name, true
	}
	if id, ok := ast.Unparen(be.Y).(*ast.Ident); ok && isNilExpr(be.X) {
		return id.Name, true
	}
	return "", false
}

func narrowFromOrNilContinue(ifs *ast.IfStmt) (string, bool) {
	if ifs == nil || ifs.Init != nil || ifs.Else != nil || ifs.Body == nil {
		return "", false
	}
	if !bodyOnlyContinues(ifs.Body) {
		return "", false
	}
	if id, ok := identComparedToNil(ifs.Cond); ok {
		return id, true
	}
	be, ok := ifs.Cond.(*ast.BinaryExpr)
	if !ok || be.Op != token.LOR {
		return "", false
	}
	if id, ok := identComparedToNil(be.X); ok {
		return id, true
	}
	if id, ok := identComparedToNil(be.Y); ok {
		return id, true
	}
	return "", false
}

func bodyOnlyContinues(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	for _, stmt := range body.List {
		br, ok := stmt.(*ast.BranchStmt)
		if !ok || br.Tok != token.CONTINUE {
			return false
		}
	}
	return true
}

func narrowAfterStmt(stmt ast.Stmt, narrowed map[string]bool) map[string]bool {
	ifs, ok := stmt.(*ast.IfStmt)
	if !ok {
		return narrowed
	}
	if id, ok := exitOnNilIdent(ifs); ok {
		next := copyBoolMap(narrowed)
		next[id] = true
		return next
	}
	if id, ok := assignOnNilIdent(ifs); ok {
		next := copyBoolMap(narrowed)
		next[id] = true
		return next
	}
	if id, ok := narrowFromOrNilContinue(ifs); ok {
		next := copyBoolMap(narrowed)
		next[id] = true
		return next
	}
	return narrowed
}

func nilableChainsInBlock(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, stmts []ast.Stmt, narrowed map[string]bool) int {
	count := 0
	for _, stmt := range stmts {
		count += nilableChainsInStmt(fn, varIdx, returns, modIdx, f, stmt, narrowed)
		narrowed = narrowAfterStmt(stmt, narrowed)
	}
	return count
}

func nilableChainsInStmt(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, stmt ast.Stmt, narrowed map[string]bool) int {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return nilableChainsInBlock(fn, varIdx, returns, modIdx, f, s.List, copyBoolMap(narrowed))
	case *ast.IfStmt:
		count := 0
		if s.Init != nil {
			count += nilableChainsInNode(fn, varIdx, returns, modIdx, f, s.Init, narrowed)
		}
		if s.Cond != nil {
			count += nilableChainsInNode(fn, varIdx, returns, modIdx, f, s.Cond, narrowed)
		}
		inner := copyBoolMap(narrowed)
		if id, ok := exitOnNilIdent(s); ok {
			inner[id] = true
		} else if id, ok := nonNilBranchIdent(s); ok {
			inner[id] = true
		}
		if s.Body != nil {
			count += nilableChainsInBlock(fn, varIdx, returns, modIdx, f, s.Body.List, inner)
		}
		if s.Else != nil {
			count += nilableChainsInStmt(fn, varIdx, returns, modIdx, f, s.Else, narrowed)
		}
		return count
	case *ast.ForStmt:
		count := 0
		if s.Init != nil {
			count += nilableChainsInNode(fn, varIdx, returns, modIdx, f, s.Init, narrowed)
		}
		if s.Cond != nil {
			count += nilableChainsInNode(fn, varIdx, returns, modIdx, f, s.Cond, narrowed)
		}
		if s.Post != nil {
			count += nilableChainsInNode(fn, varIdx, returns, modIdx, f, s.Post, narrowed)
		}
		if s.Body != nil {
			count += nilableChainsInBlock(fn, varIdx, returns, modIdx, f, s.Body.List, copyBoolMap(narrowed))
		}
		return count
	case *ast.RangeStmt:
		count := 0
		if s.X != nil {
			count += nilableChainsInNode(fn, varIdx, returns, modIdx, f, s.X, narrowed)
		}
		if s.Body != nil {
			count += nilableChainsInBlock(fn, varIdx, returns, modIdx, f, s.Body.List, copyBoolMap(narrowed))
		}
		return count
	case *ast.SelectStmt:
		count := 0
		for _, comm := range s.Body.List {
			if clause, ok := comm.(*ast.CommClause); ok && clause.Body != nil {
				count += nilableChainsInBlock(fn, varIdx, returns, modIdx, f, clause.Body, copyBoolMap(narrowed))
			}
		}
		return count
	}
	return nilableChainsInNode(fn, varIdx, returns, modIdx, f, stmt, narrowed)
}

func isAssignLHS(sel *ast.SelectorExpr, root ast.Node) bool {
	if sel == nil {
		return false
	}
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			if ast.Unparen(lhs) == sel {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func isMethodSelector(sel *ast.SelectorExpr, root ast.Node) bool {
	if sel == nil {
		return false
	}
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if s, ok := call.Fun.(*ast.SelectorExpr); ok && s == sel {
			found = true
			return false
		}
		return true
	})
	return found
}

func nilableChainsInNode(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, node ast.Node, narrowed map[string]bool) int {
	count := 0
	ast.Inspect(node, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if isMethodSelector(sel, node) || isAssignLHS(sel, node) {
			return true
		}
		vars := varIdx.byFunc[fn]
		count += processNilableSelector(sel, returns, modIdx, varIdx.fnFile[fn], varIdx.structs, fn, vars, varIdx.pkgVars, narrowed)
		count += maybeCoalesceStringFieldRead(sel, returns, modIdx, varIdx.fnFile[fn], varIdx.structs, fn, vars, varIdx.pkgVars, node)
		return true
	})
	return count
}

func maybeCoalesceStringFieldRead(sel *ast.SelectorExpr, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, structs *structFieldIndex, fn *ast.FuncDecl, vars map[string]ast.Expr, pkgVars map[string]ast.Expr, root ast.Node) int {
	if sel == nil || sel.Sel == nil || !alreadyNullCond(sel.X) || !selectorUsedAsCallArg(sel, root) {
		return 0
	}
	recvType := receiverTypeForSelector(returns, modIdx, f, structs, fn, vars, pkgVars, sel)
	typeName := typeBaseName(recvType)
	if typeName == "" || structs == nil {
		return 0
	}
	if !isStringishType(structs.fieldType(typeName, sel.Sel.Name)) {
		return 0
	}
	selParentReplace(root, sel, &ast.BinaryExpr{
		X:    sel,
		Op:   token.NULLCOALESCE,
		Y:    &ast.BasicLit{Kind: token.STRING, Value: `""`},
	})
	return 1
}

func selectorUsedAsCallArg(sel *ast.SelectorExpr, root ast.Node) bool {
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, arg := range call.Args {
			if ast.Unparen(arg) == sel {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func selParentReplace(root ast.Node, old ast.Expr, new ast.Expr) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			for i, arg := range x.Args {
				if ast.Unparen(arg) == old {
					x.Args[i] = new
				}
			}
		case *ast.KeyValueExpr:
			if ast.Unparen(x.Value) == old {
				x.Value = new
			}
		}
		return true
	})
}

func processNilableSelector(sel *ast.SelectorExpr, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, structs *structFieldIndex, fn *ast.FuncDecl, vars map[string]ast.Expr, pkgVars map[string]ast.Expr, narrowed map[string]bool) int {
	if sel == nil || sel.Sel == nil {
		return 0
	}
	if root := selectorRootIdent(sel.X); root != "" && narrowed[root] {
		return 0
	}
	if !isNilablePointerType(receiverTypeForSelector(returns, modIdx, f, structs, fn, vars, pkgVars, sel)) {
		return 0
	}
	if alreadyNullCond(sel.X) {
		return 0
	}
	sel.X = wrapNullCond(sel.X)
	return 1
}

func nilableMethodGuards(f *ast.File, files []*ast.File, returns *returnTypeIndex, modIdx *moduleFuncIndex) int {
	varIdx := buildFuncVarIndex(files, returns, modIdx)
	count := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		fn.Body.List, count = rewriteNilableMethodStmts(fn, varIdx, returns, modIdx, f, fn.Body.List, map[string]bool{}, count)
		count += rewriteFuncLitBodies(fn, varIdx, returns, modIdx, f, fn.Body.List, map[string]bool{}, 0)
	}
	return count
}

func rewriteNilableMethodStmts(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, stmts []ast.Stmt, narrowed map[string]bool, count int) ([]ast.Stmt, int) {
	var out []ast.Stmt
	for i := 0; i < len(stmts); i++ {
		stmt := stmts[i]
		rewritten, stmtCount, skip := rewriteNilableMethodStmt(fn, varIdx, returns, modIdx, f, stmt, narrowed, stmts, i)
		count += stmtCount
		out = append(out, rewritten...)
		i += skip
		narrowed = narrowAfterStmt(stmt, narrowed)
	}
	return out, count
}

func rewriteFuncLitBodies(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, stmts []ast.Stmt, narrowed map[string]bool, count int) int {
	var walk func([]ast.Stmt)
	walk = func(list []ast.Stmt) {
		for _, stmt := range list {
			ast.Inspect(stmt, func(n ast.Node) bool {
				fl, ok := n.(*ast.FuncLit)
				if !ok || fl.Body == nil {
					return true
				}
				var added int
				fl.Body.List, added = rewriteNilableMethodStmts(fn, varIdx, returns, modIdx, f, fl.Body.List, map[string]bool{}, 0)
				count += added
				walk(fl.Body.List)
				return true
			})
		}
	}
	walk(stmts)
	return count
}

func rewriteNilableMethodStmt(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, stmt ast.Stmt, narrowed map[string]bool, block []ast.Stmt, blockIdx int) ([]ast.Stmt, int, int) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		list, n := rewriteNilableMethodStmts(fn, varIdx, returns, modIdx, f, s.List, copyBoolMap(narrowed), 0)
		s.List = list
		return []ast.Stmt{s}, n, 0
	case *ast.IfStmt:
		count := 0
		inner := copyBoolMap(narrowed)
		if id, ok := exitOnNilIdent(s); ok {
			inner[id] = true
		} else if id, ok := nonNilBranchIdent(s); ok {
			inner[id] = true
		} else if id, ok := assignOnNilIdent(s); ok {
			inner[id] = true
		} else if id, ok := narrowFromOrNilContinue(s); ok {
			inner[id] = true
		}
		if s.Body != nil {
			list, n := rewriteNilableMethodStmts(fn, varIdx, returns, modIdx, f, s.Body.List, inner, 0)
			s.Body.List = list
			count += n
		}
		if s.Else != nil {
			elseStmts, n, _ := rewriteNilableMethodStmt(fn, varIdx, returns, modIdx, f, s.Else, narrowed, nil, -1)
			count += n
			if len(elseStmts) == 1 {
				s.Else = elseStmts[0]
			}
		}
		return []ast.Stmt{s}, count, 0
	case *ast.ForStmt:
		if s.Body != nil {
			list, n := rewriteNilableMethodStmts(fn, varIdx, returns, modIdx, f, s.Body.List, copyBoolMap(narrowed), 0)
			s.Body.List = list
			return []ast.Stmt{s}, n, 0
		}
	case *ast.RangeStmt:
		if call, ok := ast.Unparen(s.X).(*ast.CallExpr); ok {
			if guard, ok := nilableMethodGuardFromCall(fn, varIdx, returns, modIdx, f, call, narrowed); ok {
				sel := call.Fun.(*ast.SelectorExpr)
				rangeStmt := &ast.RangeStmt{
					Key:   s.Key,
					Value: s.Value,
					Tok:   s.Tok,
					X:     guardedMethodCall(sel.X, call, sel),
					Body:  s.Body,
				}
				guard.Body.List = []ast.Stmt{rangeStmt}
				if s.Body != nil {
					list, n := rewriteNilableMethodStmts(fn, varIdx, returns, modIdx, f, guard.Body.List, copyBoolMap(narrowed), 0)
					guard.Body.List = list
					return []ast.Stmt{guard}, n + 1, 0
				}
				return []ast.Stmt{guard}, 1, 0
			}
		}
		if s.Body != nil {
			list, n := rewriteNilableMethodStmts(fn, varIdx, returns, modIdx, f, s.Body.List, copyBoolMap(narrowed), 0)
			s.Body.List = list
			return []ast.Stmt{s}, n, 0
		}
	case *ast.ExprStmt:
		if stmts, n, skip, ok := nilableRootStmtGuard(fn, varIdx, f, s, narrowed, block, blockIdx); ok {
			return stmts, n, skip
		}
		if guard, ok := nilableMethodGuardFromCall(fn, varIdx, returns, modIdx, f, s.X, narrowed); ok {
			return []ast.Stmt{guard}, 1, 0
		}
		if guard, ok := nilableStarDerefGuard(fn, varIdx, returns, modIdx, f, s, narrowed); ok {
			return []ast.Stmt{guard}, 1, 0
		}
	case *ast.AssignStmt:
		if stmts, n, skip, ok := nilableRootStmtGuard(fn, varIdx, f, s, narrowed, block, blockIdx); ok {
			return stmts, n, skip
		}
		if len(s.Lhs) == 1 {
			if guard, ok := nilableFieldAssignGuard(fn, varIdx, returns, modIdx, f, s, narrowed); ok {
				return []ast.Stmt{guard}, 1, 0
			}
		}
		if len(s.Rhs) == 1 {
			if stmts, skip, ok := nilableMethodGuardFromAssign(fn, varIdx, returns, modIdx, f, s, narrowed, block, blockIdx); ok {
				return stmts, len(stmts), skip
			}
		}
		if guard, ok := nilableStarDerefGuard(fn, varIdx, returns, modIdx, f, s, narrowed); ok {
			return []ast.Stmt{guard}, 1, 0
		}
	case *ast.SelectStmt:
		count := 0
		for _, comm := range s.Body.List {
			if clause, ok := comm.(*ast.CommClause); ok && clause.Body != nil {
				list, n := rewriteNilableMethodStmts(fn, varIdx, returns, modIdx, f, clause.Body, copyBoolMap(narrowed), 0)
				clause.Body = list
				count += n
			}
		}
		return []ast.Stmt{s}, count, 0
	}
	return []ast.Stmt{stmt}, 0, 0
}

func methodCallFromExpr(expr ast.Expr) (*ast.CallExpr, *ast.SelectorExpr, bool) {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if ok {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return call, sel, ok && sel.Sel != nil
	}
	if u, ok := ast.Unparen(expr).(*ast.UnaryExpr); ok && u.Op == token.ARROW {
		call, ok = ast.Unparen(u.X).(*ast.CallExpr)
		if !ok {
			return nil, nil, false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return call, sel, ok && sel.Sel != nil
	}
	return nil, nil, false
}

func guardedMethodCall(recv ast.Expr, call *ast.CallExpr, sel *ast.SelectorExpr) ast.Expr {
	guarded := &ast.CallExpr{
		Fun:  &ast.SelectorExpr{X: &ast.Ident{Name: "_recv"}, Sel: sel.Sel},
		Args: call.Args,
	}
	if call.Ellipsis != 0 {
		guarded.Ellipsis = call.Ellipsis
	}
	return guarded
}

func nilableMethodGuardFromCall(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, expr ast.Expr, narrowed map[string]bool) (*ast.IfStmt, bool) {
	call, sel, ok := methodCallFromExpr(expr)
	if !ok {
		return nil, false
	}
	vars := varIdx.byFunc[fn]
	if root := selectorRootIdent(sel.X); root != "" && narrowed[root] {
		return nil, false
	}
	if !isNilablePointerType(receiverTypeForSelector(returns, modIdx, varIdx.fnFile[fn], varIdx.structs, fn, vars, varIdx.pkgVars, sel)) {
		return nil, false
	}
	recv := sel.X
	bodyExpr := guardedMethodCall(recv, call, sel)
	if u, ok := ast.Unparen(expr).(*ast.UnaryExpr); ok && u.Op == token.ARROW {
		bodyExpr = &ast.UnaryExpr{Op: token.ARROW, X: bodyExpr}
	}
	return &ast.IfStmt{
		Init: &ast.AssignStmt{
			Tok: token.DEFINE,
			Lhs: []ast.Expr{&ast.Ident{Name: "_recv"}},
			Rhs: []ast.Expr{recv},
		},
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: "_recv"},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: bodyExpr}}},
	}, true
}

func nilableMethodGuardFromAssign(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, assign *ast.AssignStmt, narrowed map[string]bool, block []ast.Stmt, blockIdx int) ([]ast.Stmt, int, bool) {
	call, sel, ok := methodCallFromExpr(assign.Rhs[0])
	if !ok {
		return nil, 0, false
	}
	guard, ok := nilableMethodGuardFromCall(fn, varIdx, returns, modIdx, f, call, narrowed)
	if !ok {
		return nil, 0, false
	}
	bodyExpr := guardedMethodCall(sel.X, call, sel)
	if u, ok := ast.Unparen(assign.Rhs[0]).(*ast.UnaryExpr); ok && u.Op == token.ARROW {
		bodyExpr = &ast.UnaryExpr{Op: token.ARROW, X: bodyExpr}
	}
	guardTok := assign.Tok
	skip := 0
	refNames := assignRefNames(assign)
	if block != nil && blockIdx >= 0 && len(refNames) > 0 && anyUsedLater(block, blockIdx, refNames) {
		last := lastReferencingStmt(block, blockIdx, refNames)
		if last > blockIdx {
			skip = last - blockIdx
			guard.Body.List = append(guard.Body.List, block[blockIdx+1:last+1]...)
		}
	}
	if id, ok := ast.Unparen(assign.Lhs[0]).(*ast.Ident); ok && assign.Tok == token.DEFINE && block != nil && identUsedLater(block, blockIdx, id.Name) && skip == 0 {
		vars := varIdx.byFunc[fn]
		typ := assignTypeFromRHS(returns, modIdx, f, varIdx.structs, fn, vars, varIdx.pkgVars, assign.Rhs[0])
		if typ != nil {
			guardTok = token.ASSIGN
			return []ast.Stmt{
				&ast.DeclStmt{Decl: &ast.GenDecl{
					Tok: token.VAR,
					Specs: []ast.Spec{&ast.ValueSpec{
						Names: []*ast.Ident{{Name: id.Name}},
						Type:  typ,
					}},
				}},
				&ast.IfStmt{
					Init: guard.Init,
					Cond: guard.Cond,
					Body: &ast.BlockStmt{List: []ast.Stmt{&ast.AssignStmt{
						Tok: token.ASSIGN,
						Lhs: assign.Lhs,
						Rhs: []ast.Expr{bodyExpr},
					}}},
				},
			}, 0, true
		}
	}
	guard.Body.List = append([]ast.Stmt{&ast.AssignStmt{
		Tok: guardTok,
		Lhs: assign.Lhs,
		Rhs: []ast.Expr{bodyExpr},
	}}, guard.Body.List...)
	return []ast.Stmt{guard}, skip, true
}

func assignRefNames(assign *ast.AssignStmt) []string {
	var names []string
	for _, lhs := range assign.Lhs {
		if id, ok := ast.Unparen(lhs).(*ast.Ident); ok {
			names = append(names, id.Name)
		}
	}
	return names
}

func anyUsedLater(block []ast.Stmt, start int, names []string) bool {
	for i := start + 1; i < len(block); i++ {
		for _, name := range names {
			if identUsedInNode(block[i], name) {
				return true
			}
		}
	}
	return false
}

func lastReferencingStmt(block []ast.Stmt, start int, names []string) int {
	last := start
	for i := start + 1; i < len(block); i++ {
		for _, name := range names {
			if identUsedInNode(block[i], name) {
				last = i
				break
			}
		}
	}
	return last
}

func identUsedLater(block []ast.Stmt, start int, name string) bool {
	for i := start + 1; i < len(block); i++ {
		if identUsedInNode(block[i], name) {
			return true
		}
	}
	return false
}

func identUsedInNode(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func assignTypeFromRHS(returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, structs *structFieldIndex, fn *ast.FuncDecl, vars map[string]ast.Expr, pkgVars map[string]ast.Expr, rhs ast.Expr) ast.Expr {
	expr := ast.Unparen(rhs)
	if u, ok := expr.(*ast.UnaryExpr); ok && u.Op == token.ARROW {
		expr = ast.Unparen(u.X)
	}
	if call, ok := expr.(*ast.CallExpr); ok {
		res := resolveCallResultTypeAt(returns, modIdx, f, structs, fn, vars, pkgVars, call, 0)
		if ch, ok := ast.Unparen(res).(*ast.ChanType); ok {
			return ch.Value
		}
	}
	return resolveExprType(returns, modIdx, f, structs, fn, vars, pkgVars, rhs)
}

func nilableRootStmtGuard(fn *ast.FuncDecl, varIdx *funcVarIndex, f *ast.File, stmt ast.Stmt, narrowed map[string]bool, block []ast.Stmt, blockIdx int) ([]ast.Stmt, int, int, bool) {
	vars := varIdx.byFunc[fn]
	root := nilableRootInStmt(stmt, vars, varIdx.pkgVars, narrowed)
	if root == "" {
		return nil, 0, 0, false
	}
	rewriteRootAccessInStmt(stmt, root, "_root")
	body := []ast.Stmt{stmt}
	skip := 0
	if block != nil && blockIdx >= 0 {
		last := lastReferencingStmt(block, blockIdx, []string{root})
		if last > blockIdx {
			for i := blockIdx + 1; i <= last; i++ {
				rewriteRootAccessInStmt(block[i], root, "_root")
				body = append(body, block[i])
			}
			skip = last - blockIdx
		}
	}
	guard := &ast.IfStmt{
		Init: &ast.AssignStmt{
			Tok: token.DEFINE,
			Lhs: []ast.Expr{&ast.Ident{Name: "_root"}},
			Rhs: []ast.Expr{&ast.Ident{Name: root}},
		},
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: "_root"},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{List: body},
	}
	return []ast.Stmt{guard}, 1, skip, true
}

func nilableRootInStmt(stmt ast.Stmt, vars map[string]ast.Expr, pkgVars map[string]ast.Expr, narrowed map[string]bool) string {
	var found string
	ast.Inspect(stmt, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		root := selectorRootIdent(sel.X)
		if root == "" || narrowed[root] {
			return true
		}
		if !isNilablePointerType(vars[root]) && !isNilablePointerType(pkgVars[root]) {
			return true
		}
		found = root
		return false
	})
	return found
}

func rewriteRootAccessInStmt(stmt ast.Stmt, root, temp string) {
	ast.Inspect(stmt, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || selectorRootIdent(sel.X) != root {
			return true
		}
		sel.X = &ast.Ident{Name: temp}
		return true
	})
}

func nilableStarDerefIdent(fn *ast.FuncDecl, varIdx *funcVarIndex, stmt ast.Stmt, narrowed map[string]bool) string {
	vars := varIdx.byFunc[fn]
	var found string
	ast.Inspect(stmt, func(n ast.Node) bool {
		var id *ast.Ident
		switch x := n.(type) {
		case *ast.UnaryExpr:
			if x.Op != token.MUL {
				return true
			}
			id, _ = ast.Unparen(x.X).(*ast.Ident)
		case *ast.StarExpr:
			id, _ = ast.Unparen(x.X).(*ast.Ident)
		default:
			return true
		}
		if id == nil || narrowed[id.Name] || !isNilablePointerType(vars[id.Name]) {
			return true
		}
		found = id.Name
		return false
	})
	return found
}

func rewriteStarDerefIdent(stmt ast.Stmt, id, temp string) {
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.UnaryExpr:
			if x.Op != token.MUL {
				return true
			}
			if xid, ok := ast.Unparen(x.X).(*ast.Ident); ok && xid.Name == id {
				x.X = &ast.Ident{Name: temp}
			}
		case *ast.StarExpr:
			if xid, ok := ast.Unparen(x.X).(*ast.Ident); ok && xid.Name == id {
				x.X = &ast.Ident{Name: temp}
			}
		}
		return true
	})
}

func nilableStarDerefGuard(fn *ast.FuncDecl, varIdx *funcVarIndex, _ *returnTypeIndex, _ *moduleFuncIndex, _ *ast.File, stmt ast.Stmt, narrowed map[string]bool) (*ast.IfStmt, bool) {
	id := nilableStarDerefIdent(fn, varIdx, stmt, narrowed)
	if id == "" {
		return nil, false
	}
	rewriteStarDerefIdent(stmt, id, "_deref")
	return &ast.IfStmt{
		Init: &ast.AssignStmt{
			Tok: token.DEFINE,
			Lhs: []ast.Expr{&ast.Ident{Name: "_deref"}},
			Rhs: []ast.Expr{&ast.Ident{Name: id}},
		},
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: "_deref"},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{List: []ast.Stmt{stmt}},
	}, true
}

func nilableFieldAssignGuard(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, assign *ast.AssignStmt, narrowed map[string]bool) (*ast.IfStmt, bool) {
	sel, ok := ast.Unparen(assign.Lhs[0]).(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || len(assign.Rhs) != 1 {
		return nil, false
	}
	vars := varIdx.byFunc[fn]
	if root := selectorRootIdent(sel.X); root != "" && narrowed[root] {
		return nil, false
	}
	if !isNilablePointerType(receiverTypeForSelector(returns, modIdx, varIdx.fnFile[fn], varIdx.structs, fn, vars, varIdx.pkgVars, sel)) {
		return nil, false
	}
	recv := sel.X
	return &ast.IfStmt{
		Init: &ast.AssignStmt{
			Tok: token.DEFINE,
			Lhs: []ast.Expr{&ast.Ident{Name: "_recv"}},
			Rhs: []ast.Expr{recv},
		},
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: "_recv"},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{List: []ast.Stmt{&ast.AssignStmt{
			Tok: assign.Tok,
			Lhs: []ast.Expr{&ast.SelectorExpr{X: &ast.Ident{Name: "_recv"}, Sel: sel.Sel}},
			Rhs: assign.Rhs,
		}}},
	}, true
}

func nilableChainCallRoot(expr ast.Expr) ast.Expr {
	if nc, ok := ast.Unparen(expr).(*ast.NullCondExpr); ok {
		return nc.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	return nilableChainCallRoot(sel.X)
}

func rebuildSelectorChain(expr ast.Expr, recv ast.Expr) ast.Expr {
	if _, ok := ast.Unparen(expr).(*ast.NullCondExpr); ok {
		return recv
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return expr
	}
	return &ast.SelectorExpr{X: rebuildSelectorChain(sel.X, recv), Sel: sel.Sel}
}

func hasNullCondChain(expr ast.Expr) bool {
	if _, ok := ast.Unparen(expr).(*ast.NullCondExpr); ok {
		return true
	}
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		return hasNullCondChain(sel.X)
	}
	return false
}

func rewriteNilableChainCompare(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, expr ast.Expr) (ast.Stmt, ast.Expr, bool) {
	be, ok := expr.(*ast.BinaryExpr)
	if !ok || !hasNullCondChain(be.X) {
		return nil, expr, false
	}
	root := nilableChainCallRoot(be.X)
	if root == nil {
		return nil, expr, false
	}
	init := &ast.AssignStmt{
		Tok: token.DEFINE,
		Lhs: []ast.Expr{&ast.Ident{Name: "_recv"}},
		Rhs: []ast.Expr{root},
	}
	recv := &ast.Ident{Name: "_recv"}
	cond := &ast.BinaryExpr{
		X: &ast.BinaryExpr{
			X:  recv,
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Op: token.LAND,
		Y: &ast.BinaryExpr{
			X:  rebuildSelectorChain(be.X, recv),
			Op: be.Op,
			Y:  be.Y,
		},
	}
	return init, cond, true
}

func rewriteNilableChainCondition(fn *ast.FuncDecl, varIdx *funcVarIndex, returns *returnTypeIndex, modIdx *moduleFuncIndex, f *ast.File, cond ast.Expr) (ast.Stmt, ast.Expr, bool) {
	if be, ok := cond.(*ast.BinaryExpr); ok && be.Op == token.LAND {
		if init, right, ok := rewriteNilableChainCompare(fn, varIdx, returns, modIdx, f, be.Y); ok {
			return init, &ast.BinaryExpr{X: be.X, Op: token.LAND, Y: right}, true
		}
		if init, left, ok := rewriteNilableChainCompare(fn, varIdx, returns, modIdx, f, be.X); ok {
			return init, &ast.BinaryExpr{X: left, Op: token.LAND, Y: be.Y}, true
		}
	}
	return rewriteNilableChainCompare(fn, varIdx, returns, modIdx, f, cond)
}

func rewriteIfNilableChainConditions(f *ast.File, files []*ast.File, returns *returnTypeIndex, modIdx *moduleFuncIndex) int {
	varIdx := buildFuncVarIndex(files, returns, modIdx)
	count := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ifs, ok := n.(*ast.IfStmt)
			if !ok || ifs.Cond == nil {
				return true
			}
			init, cond, ok := rewriteNilableChainCondition(fn, varIdx, returns, modIdx, f, ifs.Cond)
			if !ok {
				return true
			}
			if ifs.Init == nil {
				ifs.Init = init
			}
			ifs.Cond = cond
			count++
			return true
		})
	}
	return count
}

func coalesceOptionalFieldsInCompositeLits(f *ast.File, structs *structFieldIndex) int {
	count := 0
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		typeName := compositeLitTypeName(lit)
		if typeName == "" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			sel, ok := ast.Unparen(kv.Value).(*ast.SelectorExpr)
			if !ok || !alreadyNullCond(sel.X) {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || structs == nil {
				continue
			}
			ft := structs.fieldType(typeName, key.Name)
			if !isStringishType(ft) {
				continue
			}
			kv.Value = &ast.BinaryExpr{
				X:    kv.Value,
				Op:   token.NULLCOALESCE,
				Y:    &ast.BasicLit{Kind: token.STRING, Value: `""`},
			}
			count++
		}
		return true
	})
	return count
}

func isStringishType(t ast.Expr) bool {
	if t == nil {
		return false
	}
	t = ast.Unparen(t)
	if ne, ok := t.(*ast.NilableTypeExpr); ok {
		t = ast.Unparen(ne.X)
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name == "string"
	}
	return false
}
