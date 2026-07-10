package main

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"
)

type ptrSiteKey struct {
	kind  string // "field", "param", "result", "var"
	owner string // type or func name; empty for locals/package vars
	index int    // param/result index; -1 when unused
	name  string // field, var, or named result/param name
}

type typedField struct {
	key ptrSiteKey
	typ ast.Expr
}

type ptrAnnotator struct {
	fset          *token.FileSet
	files         []*ast.File
	nilable       map[ptrSiteKey]bool
	strictResult  map[ptrSiteKey]bool
	lookupResult  map[ptrSiteKey]bool
	typeNode      map[ptrSiteKey]ast.Expr
	funcs         map[string][]*ast.FuncDecl
}

func newPtrAnnotator(fset *token.FileSet, files []*ast.File) *ptrAnnotator {
	return &ptrAnnotator{
		fset:         fset,
		files:        files,
		nilable:      make(map[ptrSiteKey]bool),
		strictResult: make(map[ptrSiteKey]bool),
		lookupResult: make(map[ptrSiteKey]bool),
		typeNode:     make(map[ptrSiteKey]ast.Expr),
		funcs:        make(map[string][]*ast.FuncDecl),
	}
}

func (a *ptrAnnotator) analyze() {
	a.collectSites()
	for _, f := range a.files {
		a.scanNilEvidence(f)
	}
	a.markLookupResults()
	a.propagateNilableParamToAssignedFields()
	a.dropNilableParamsUsedAsCallArgs()
	a.dropNilableReassignedParams()
	a.finalizeNilableResults()
}

func boolPairResults(fields *ast.FieldList) bool {
	if fields == nil || len(fields.List) != 2 {
		return false
	}
	second := fields.List[1].Type
	id, ok := ast.Unparen(second).(*ast.Ident)
	return ok && id.Name == "bool"
}

func stdErrPairResults(fields *ast.FieldList) bool {
	if fields == nil || len(fields.List) != 2 {
		return false
	}
	return isErrorType(fields.List[1].Type)
}

func errLastResult(fields *ast.FieldList) bool {
	if fields == nil || len(fields.List) == 0 {
		return false
	}
	return isErrorType(fields.List[len(fields.List)-1].Type)
}

func (a *ptrAnnotator) markLookupResults() {
	for _, f := range a.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !a.isLookupResult(fn) || fn.Type == nil || fn.Type.Results == nil || boolPairResults(fn.Type.Results) {
				continue
			}
			// Only annotate lookup results for (T, error) or a single pointer result.
			// Multi-value returns like (T, []string, error) keep strict pointer types at call sites.
			if !stdErrPairResults(fn.Type.Results) && len(fn.Type.Results.List) != 1 {
				continue
			}
			flat := flattenFields("result", fn.Name.Name, fn.Type.Results)
			for _, tf := range flat {
				if plainStarType(tf.typ) != nil {
					a.nilable[tf.key] = true
					a.lookupResult[tf.key] = true
				}
			}
		}
	}
}

func (a *ptrAnnotator) finalizeNilableResults() {
	for key := range a.strictResult {
		if a.lookupResult[key] {
			continue
		}
		delete(a.nilable, key)
	}
}

func (a *ptrAnnotator) isPackageFunc(fn *ast.FuncDecl) bool {
	return fn != nil && fn.Recv == nil
}

func (a *ptrAnnotator) isLookupResult(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Name == nil {
		return false
	}
	name := strings.ToLower(fn.Name.Name)
	switch name {
	case "root", "sizerecursive", "totalchildrenrec":
		return false
	}
	for _, hint := range []string{"find", "search", "lookup", "parent"} {
		if strings.Contains(name, hint) {
			return true
		}
	}
	return false
}

func (a *ptrAnnotator) collectSites() {
	for _, f := range a.files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				a.collectGenDecl(d)
			case *ast.FuncDecl:
				a.funcs[d.Name.Name] = append(a.funcs[d.Name.Name], d)
				a.collectFuncDecl(d)
				if d.Body != nil {
					a.collectLocalVars(d.Name.Name, d.Body)
				}
			}
		}
	}
}

func (a *ptrAnnotator) registerPtrType(key ptrSiteKey, typ ast.Expr) {
	a.typeNode[key] = typ
}

func (a *ptrAnnotator) collectGenDecl(g *ast.GenDecl) {
	switch g.Tok {
	case token.TYPE:
		for _, spec := range g.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			a.collectStructFields(ts.Name.Name, ts.Type)
			a.collectInterfaceMethods(ts.Name.Name, ts.Type)
		}
	case token.VAR:
		for _, spec := range g.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || plainStarType(vs.Type) == nil {
				continue
			}
			for _, name := range vs.Names {
				key := ptrSiteKey{kind: "var", name: name.Name, index: -1}
				a.registerPtrType(key, vs.Type)
				if len(vs.Values) == 1 && isNilExpr(vs.Values[0]) {
					a.nilable[key] = true
				}
			}
		}
	}
}

func (a *ptrAnnotator) collectStructFields(typeName string, typ ast.Expr) {
	st, ok := ast.Unparen(typ).(*ast.StructType)
	if !ok || st.Fields == nil {
		return
	}
	for _, field := range st.Fields.List {
		if plainStarType(field.Type) == nil {
			continue
		}
		for _, name := range field.Names {
			key := ptrSiteKey{kind: "field", owner: typeName, name: name.Name, index: -1}
			a.registerPtrType(key, field.Type)
			if fieldHasOmitempty(field) {
				a.nilable[key] = true
			}
		}
	}
}

func (a *ptrAnnotator) collectInterfaceMethods(typeName string, typ ast.Expr) {
	it, ok := ast.Unparen(typ).(*ast.InterfaceType)
	if !ok || it.Methods == nil {
		return
	}
	for _, field := range it.Methods.List {
		ft, ok := field.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		mname := ""
		if len(field.Names) > 0 {
			mname = field.Names[0].Name
		}
		a.collectFuncTypeSites("param", mname, ft.Params)
		a.collectFuncTypeSites("result", mname, ft.Results)
	}
}

func (a *ptrAnnotator) collectFuncDecl(fn *ast.FuncDecl) {
	name := fn.Name.Name
	if fn.Type == nil {
		return
	}
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recv := fn.Recv.List[0]
		if plainStarType(recv.Type) != nil {
			rname := "recv"
			if len(recv.Names) > 0 {
				rname = recv.Names[0].Name
			}
			key := ptrSiteKey{kind: "var", owner: name, name: rname, index: -1}
			a.registerPtrType(key, recv.Type)
		}
	}
	a.collectFuncTypeSites("param", name, fn.Type.Params)
	a.collectFuncTypeSites("result", name, fn.Type.Results)
}

func (a *ptrAnnotator) collectFuncTypeSites(kind, owner string, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	idx := 0
	for _, field := range fields.List {
		if plainStarType(field.Type) == nil {
			idx += max(1, len(field.Names))
			continue
		}
		if len(field.Names) == 0 {
			key := ptrSiteKey{kind: kind, owner: owner, index: idx}
			a.registerPtrType(key, field.Type)
			idx++
			continue
		}
		for _, n := range field.Names {
			key := ptrSiteKey{kind: kind, owner: owner, name: n.Name, index: idx}
			a.registerPtrType(key, field.Type)
			idx++
		}
	}
}

func (a *ptrAnnotator) collectLocalVars(owner string, body *ast.BlockStmt) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		g, ok := n.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			return true
		}
		for _, spec := range g.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || plainStarType(vs.Type) == nil {
				continue
			}
			for _, name := range vs.Names {
				key := ptrSiteKey{kind: "var", owner: owner, name: name.Name, index: -1}
				a.registerPtrType(key, vs.Type)
				if len(vs.Values) == 1 && isNilExpr(vs.Values[0]) {
					a.nilable[key] = true
				}
			}
		}
		return true
	})
}

func fieldHasOmitempty(field *ast.Field) bool {
	// omitempty tags alone are not sufficient evidence for NPT annotation.
	return false
}

func (a *ptrAnnotator) scanNilEvidence(f *ast.File) {
	var curFunc *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			curFunc = x
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			a.scanAssign(x, curFunc)
		case *ast.ReturnStmt:
			a.scanReturn(x, curFunc)
		case *ast.CallExpr:
			a.scanCall(x)
		case *ast.CompositeLit:
			a.scanCompositeLit(x)
		}
		return true
	})
}

func (a *ptrAnnotator) scanAssign(as *ast.AssignStmt, fn *ast.FuncDecl) {
	owner := ""
	if fn != nil {
		owner = fn.Name.Name
	}
	for i, lhs := range as.Lhs {
		rhs := rhsAt(as.Rhs, i)
		if isNilExpr(rhs) {
			switch e := ast.Unparen(lhs).(type) {
			case *ast.Ident:
				a.markVarNilable(fn, e.Name)
			case *ast.SelectorExpr:
				if typeName, field, ok := selectorField(e); ok {
					key := ptrSiteKey{kind: "field", owner: typeName, name: field, index: -1}
					a.nilable[key] = true
				}
			}
			continue
		}
		if id, ok := ast.Unparen(lhs).(*ast.Ident); ok {
			a.strictResult[ptrSiteKey{kind: "var", owner: owner, name: id.Name, index: -1}] = true
		}
	}
}

func (a *ptrAnnotator) markVarNilable(fn *ast.FuncDecl, name string) {
	owner := ""
	if fn != nil {
		owner = fn.Name.Name
	}
	a.nilable[ptrSiteKey{kind: "var", owner: owner, name: name, index: -1}] = true
}

func rhsAt(rhs []ast.Expr, i int) ast.Expr {
	if len(rhs) == 1 {
		return rhs[0]
	}
	if i < len(rhs) {
		return rhs[i]
	}
	return nil
}

func (a *ptrAnnotator) scanReturn(ret *ast.ReturnStmt, fn *ast.FuncDecl) {
	if fn == nil || fn.Type == nil || fn.Type.Results == nil {
		return
	}
	if boolPairResults(fn.Type.Results) {
		return
	}
	flat := flattenFields("result", fn.Name.Name, fn.Type.Results)
	lookup := a.isLookupResult(fn)
	for i, expr := range ret.Results {
		if i >= len(flat) || plainStarType(flat[i].typ) == nil {
			continue
		}
		if lookup {
			a.lookupResult[flat[i].key] = true
		}
		if !isNilExpr(expr) {
			a.strictResult[flat[i].key] = true
			continue
		}
		if len(ret.Results) == 1 {
			a.nilable[flat[i].key] = true
			continue
		}
		// return nil, ... in multi-value returns is almost always an error path zero value.
		if i == 0 && isNilExpr(expr) {
			continue
		}
		// Only nilable-annotate non-error slots when the last result is error.
		// Pairs like (T, *RemoteErr) must keep strict signatures for callbacks.
		if !errLastResult(fn.Type.Results) {
			continue
		}
		a.nilable[flat[i].key] = true
	}
}

func (a *ptrAnnotator) scanCall(call *ast.CallExpr) {
	fnName, _, _ := callTarget(call.Fun)
	if fnName == "" {
		return
	}
	for _, fn := range a.funcs[fnName] {
		if fn.Type == nil || fn.Type.Params == nil {
			continue
		}
		flat := flattenFields("param", fn.Name.Name, fn.Type.Params)
		for i, arg := range call.Args {
			if !isNilExpr(arg) || i >= len(flat) {
				continue
			}
			if plainStarType(flat[i].typ) != nil {
				a.nilable[flat[i].key] = true
			}
		}
	}
}

func flattenFields(kind, owner string, fields *ast.FieldList) []typedField {
	if fields == nil {
		return nil
	}
	var out []typedField
	idx := 0
	for _, field := range fields.List {
		if plainStarType(field.Type) == nil {
			idx += max(1, len(field.Names))
			continue
		}
		if len(field.Names) == 0 {
			out = append(out, typedField{
				key: ptrSiteKey{kind: kind, owner: owner, index: idx},
				typ: field.Type,
			})
			idx++
			continue
		}
		for _, n := range field.Names {
			out = append(out, typedField{
				key: ptrSiteKey{kind: kind, owner: owner, name: n.Name, index: idx},
				typ: field.Type,
			})
			idx++
		}
	}
	return out
}

func callTarget(fun ast.Expr) (name, recv string, isMethod bool) {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return f.Name, "", false
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			return f.Sel.Name, id.Name, true
		}
		return f.Sel.Name, "", true
	default:
		return "", "", false
	}
}

func (a *ptrAnnotator) scanCompositeLit(lit *ast.CompositeLit) {
	// Struct fields set to nil in composite literals are often optional
	// sentinels; do not infer *T? on the field type from those alone.
}

func compositeLitTypeName(lit *ast.CompositeLit) string {
	switch t := ast.Unparen(lit.Type).(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.UnaryExpr:
		if t.Op == token.AND {
			return compositeLitTypeName(&ast.CompositeLit{Type: t.X})
		}
	default:
		return ""
	}
	return ""
}

func (a *ptrAnnotator) propagateNilableParamToAssignedFields() {
	for _, f := range a.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Type == nil || fn.Body == nil {
				continue
			}
			recvName, recvTypeName, ok := recvNameAndType(fn.Recv)
			if !ok || recvTypeName == "" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || assign.Tok != token.ASSIGN {
					return true
				}
				for i, lhs := range assign.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					recv, ok := sel.X.(*ast.Ident)
					if !ok || recv.Name != recvName {
						continue
					}
					rhs := rhsAt(assign.Rhs, i)
					id, ok := ast.Unparen(rhs).(*ast.Ident)
					if !ok {
						continue
					}
					for _, tf := range flattenFields("param", fn.Name.Name, fn.Type.Params) {
						if tf.key.name != id.Name || !a.nilable[tf.key] {
							continue
						}
						fieldKey := ptrSiteKey{kind: "field", owner: recvTypeName, name: sel.Sel.Name, index: -1}
						a.nilable[fieldKey] = true
					}
				}
				return true
			})
		}
	}
}

func (a *ptrAnnotator) dropNilableReassignedParams() {
	for _, f := range a.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Type == nil || fn.Type.Params == nil || fn.Body == nil {
				continue
			}
			paramKeys := make(map[string]ptrSiteKey)
			for _, tf := range flattenFields("param", fn.Name.Name, fn.Type.Params) {
				if tf.key.name == "" || !a.nilable[tf.key] {
					continue
				}
				paramKeys[tf.key.name] = tf.key
			}
			if len(paramKeys) == 0 {
				continue
			}
			reassigned := make(map[string]bool)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || assign.Tok != token.ASSIGN {
					return true
				}
				for _, lhs := range assign.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok {
						continue
					}
					if _, ok := paramKeys[id.Name]; ok {
						reassigned[id.Name] = true
					}
				}
				return true
			})
			for name := range reassigned {
				delete(a.nilable, paramKeys[name])
			}
		}
	}
}

func (a *ptrAnnotator) dropNilableParamsUsedAsCallArgs() {
	for _, f := range a.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Type == nil || fn.Type.Params == nil || fn.Body == nil {
				continue
			}
			nilableParams := make(map[string]ptrSiteKey)
			for _, tf := range flattenFields("param", fn.Name.Name, fn.Type.Params) {
				if tf.key.name == "" || !a.nilable[tf.key] {
					continue
				}
				nilableParams[tf.key.name] = tf.key
			}
			if len(nilableParams) == 0 {
				continue
			}
			usedInCall := make(map[string]bool)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				for _, arg := range call.Args {
					id, ok := ast.Unparen(arg).(*ast.Ident)
					if !ok {
						continue
					}
					if _, ok := nilableParams[id.Name]; ok {
						usedInCall[id.Name] = true
					}
				}
				return true
			})
			for name := range usedInCall {
				delete(a.nilable, nilableParams[name])
			}
		}
	}
}

func selectorField(sel *ast.SelectorExpr) (typeName, field string, ok bool) {
	switch x := sel.X.(type) {
	case *ast.Ident:
		return x.Name, sel.Sel.Name, true
	default:
		return "", sel.Sel.Name, false
	}
}

func applyPtrAnnotations(fset *token.FileSet, files []*ast.File) []bool {
	ann := newPtrAnnotator(fset, files)
	ann.analyze()
	changed := make([]bool, len(files))
	for i, f := range files {
		fileChanged := false
		if rewriteFilePointerTypes(fset, f, ann) {
			fileChanged = true
		}
		if fixNilReturnsForStrictPointerResults(f, ann) {
			fileChanged = true
		}
		if fixNilReceiverReturnsInMethods(f, ann) {
			fileChanged = true
		}
		if splitNilOrReturnGuards(f, ann) {
			fileChanged = true
		}
		if fixFindPassthroughReturns(f, ann) {
			fileChanged = true
		}
		if syncMethodReturnsForNilableFields(fset, f, ann) {
			fileChanged = true
		}
		changed[i] = fileChanged
	}
	return changed
}

func syncMethodReturnsForNilableFields(fset *token.FileSet, f *ast.File, ann *ptrAnnotator) bool {
	changed := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		if plainStarType(fn.Type.Results.List[0].Type) == nil {
			continue
		}
		recvName, recvTypeName, ok := recvNameAndType(fn.Recv)
		if !ok || recvTypeName == "" || fn.Body == nil {
			continue
		}
		fieldName := returnedFieldName(fn.Body, recvName)
		if fieldName == "" {
			continue
		}
		key := ptrSiteKey{kind: "field", owner: recvTypeName, name: fieldName, index: -1}
		if !ann.nilable[key] {
			continue
		}
		if newTyp, ok := rewriteTypeAt(fset, f, fn.Type.Results.List[0].Type, true); ok {
			fn.Type.Results.List[0].Type = newTyp
			changed = true
		}
	}
	return changed
}

func recvNameAndType(recv *ast.FieldList) (recvName, typeName string, ok bool) {
	if recv == nil || len(recv.List) != 1 || len(recv.List[0].Names) != 1 {
		return "", "", false
	}
	recvName = recv.List[0].Names[0].Name
	return recvName, typeNameFromExpr(recv.List[0].Type), true
}

func typeNameFromExpr(t ast.Expr) string {
	switch x := ast.Unparen(t).(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return typeNameFromExpr(x.X)
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.IndexListExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

func returnedFieldName(body *ast.BlockStmt, recvName string) string {
	if body == nil {
		return ""
	}
	var field string
	ast.Inspect(body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		sel, ok := ast.Unparen(ret.Results[0]).(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != recvName {
			return true
		}
		field = sel.Sel.Name
		return true
	})
	return field
}

func rewriteFilePointerTypes(fset *token.FileSet, f *ast.File, ann *ptrAnnotator) bool {
	changed := false
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if rewriteGenDeclTypes(fset, f, d, ann) {
				changed = true
			}
		case *ast.FuncDecl:
			if rewriteFuncDeclTypes(fset, f, d, ann) {
				changed = true
			}
			if d.Body != nil && rewriteLocalVarTypes(fset, f, d.Name.Name, d.Body, ann) {
				changed = true
			}
		}
	}
	return changed
}

func rewriteGenDeclTypes(fset *token.FileSet, f *ast.File, g *ast.GenDecl, ann *ptrAnnotator) bool {
	changed := false
	switch g.Tok {
	case token.TYPE:
		for _, spec := range g.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if rewriteStructFieldTypes(fset, f, ts.Name.Name, ts.Type, ann) {
				changed = true
			}
			if rewriteInterfaceMethodTypes(fset, f, ts.Type, ann) {
				changed = true
			}
		}
	case token.VAR:
		for _, spec := range g.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || plainStarType(vs.Type) == nil {
				continue
			}
			for _, name := range vs.Names {
				key := ptrSiteKey{kind: "var", name: name.Name, index: -1}
				if newTyp, ok := rewriteTypeAt(fset, f, vs.Type, ann.nilable[key]); ok {
					vs.Type = newTyp
					changed = true
				}
			}
		}
	}
	return changed
}

func rewriteStructFieldTypes(fset *token.FileSet, f *ast.File, typeName string, typ ast.Expr, ann *ptrAnnotator) bool {
	st, ok := ast.Unparen(typ).(*ast.StructType)
	if !ok || st.Fields == nil {
		return false
	}
	changed := false
	for _, field := range st.Fields.List {
		if plainStarType(field.Type) == nil {
			continue
		}
		for _, name := range field.Names {
			key := ptrSiteKey{kind: "field", owner: typeName, name: name.Name, index: -1}
			if newTyp, ok := rewriteTypeAt(fset, f, field.Type, ann.nilable[key]); ok {
				field.Type = newTyp
				changed = true
			}
		}
	}
	return changed
}

func rewriteInterfaceMethodTypes(fset *token.FileSet, f *ast.File, typ ast.Expr, ann *ptrAnnotator) bool {
	it, ok := ast.Unparen(typ).(*ast.InterfaceType)
	if !ok || it.Methods == nil {
		return false
	}
	changed := false
	for _, field := range it.Methods.List {
		ft, ok := field.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		mname := ""
		if len(field.Names) > 0 {
			mname = field.Names[0].Name
		}
		if rewriteFieldListTypes(fset, f, "param", mname, ft.Params, ann) {
			changed = true
		}
		if rewriteFieldListTypes(fset, f, "result", mname, ft.Results, ann) {
			changed = true
		}
	}
	return changed
}

func rewriteFuncDeclTypes(fset *token.FileSet, f *ast.File, fn *ast.FuncDecl, ann *ptrAnnotator) bool {
	if fn.Type == nil {
		return false
	}
	changed := false
	name := fn.Name.Name
	if fn.Recv != nil {
		for _, recv := range fn.Recv.List {
			if plainStarType(recv.Type) == nil {
				continue
			}
			rname := "recv"
			if len(recv.Names) > 0 {
				rname = recv.Names[0].Name
			}
			key := ptrSiteKey{kind: "var", owner: name, name: rname, index: -1}
			if newTyp, ok := rewriteTypeAt(fset, f, recv.Type, ann.nilable[key]); ok {
				recv.Type = newTyp
				changed = true
			}
		}
	}
	if rewriteFieldListTypes(fset, f, "param", name, fn.Type.Params, ann) {
		changed = true
	}
	if rewriteFieldListTypes(fset, f, "result", name, fn.Type.Results, ann) {
		changed = true
	}
	return changed
}

func rewriteFieldListTypes(fset *token.FileSet, f *ast.File, kind, owner string, fields *ast.FieldList, ann *ptrAnnotator) bool {
	if fields == nil {
		return false
	}
	changed := false
	idx := 0
	for _, field := range fields.List {
		if plainStarType(field.Type) == nil {
			idx += max(1, len(field.Names))
			continue
		}
		if len(field.Names) == 0 {
			key := ptrSiteKey{kind: kind, owner: owner, index: idx}
			if newTyp, ok := rewriteTypeAt(fset, f, field.Type, ann.nilable[key]); ok {
				field.Type = newTyp
				changed = true
			}
			idx++
			continue
		}
		for _, n := range field.Names {
			key := ptrSiteKey{kind: kind, owner: owner, name: n.Name, index: idx}
			if newTyp, ok := rewriteTypeAt(fset, f, field.Type, ann.nilable[key]); ok {
				field.Type = newTyp
				changed = true
			}
			idx++
		}
	}
	return changed
}

func rewriteLocalVarTypes(fset *token.FileSet, f *ast.File, owner string, body *ast.BlockStmt, ann *ptrAnnotator) bool {
	changed := false
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		g, ok := n.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			return true
		}
		for _, spec := range g.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || plainStarType(vs.Type) == nil {
				continue
			}
			for _, name := range vs.Names {
				key := ptrSiteKey{kind: "var", owner: owner, name: name.Name, index: -1}
				if newTyp, ok := rewriteTypeAt(fset, f, vs.Type, ann.nilable[key]); ok {
					vs.Type = newTyp
					changed = true
				}
			}
		}
		return true
	})
	return changed
}

func rewriteTypeAt(fset *token.FileSet, f *ast.File, typ ast.Expr, nilable bool) (ast.Expr, bool) {
	if nptDisabledAt(fset, typ.Pos(), []*ast.File{f}) {
		return typ, false
	}
	newTyp := setPointerNilable(typ, nilable)
	return newTyp, newTyp != typ
}

func nptDisabledAt(fset *token.FileSet, pos token.Pos, files []*ast.File) bool {
	if !pos.IsValid() {
		return false
	}
	for _, f := range files {
		if pos < f.Pos() || pos > f.End() {
			continue
		}
		for _, r := range f.NilablePointersRegions {
			if r.Mode == "disable" && pos >= r.Start && (!r.End.IsValid() || pos < r.End) {
				return true
			}
		}
	}
	return false
}

func plainStarType(t ast.Expr) *ast.StarExpr {
	t = ast.Unparen(t)
	if nilable, ok := t.(*ast.NilableTypeExpr); ok {
		t = ast.Unparen(nilable.X)
	}
	star, ok := t.(*ast.StarExpr)
	if !ok || !isMigratablePointer(star) {
		return nil
	}
	return star
}

func isMigratablePointer(star *ast.StarExpr) bool {
	switch x := ast.Unparen(star.X).(type) {
	case *ast.Ident:
		if x.Name == "unsafe" {
			return false
		}
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && id.Name == "unsafe" && x.Sel.Name == "Pointer" {
			return false
		}
	}
	return true
}

func setPointerNilable(t ast.Expr, nilable bool) ast.Expr {
	t = ast.Unparen(t)
	if nilable {
		if _, ok := t.(*ast.NilableTypeExpr); ok {
			return t
		}
		star, ok := t.(*ast.StarExpr)
		if !ok {
			return t
		}
		return &ast.NilableTypeExpr{X: star, QPos: star.End()}
	}
	if n, ok := t.(*ast.NilableTypeExpr); ok {
		return n.X
	}
	return t
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func isNilExpr(e ast.Expr) bool {
	if e == nil {
		return false
	}
	id, ok := ast.Unparen(e).(*ast.Ident)
	return ok && id.Name == "nil"
}

type nilablePointersDirective struct {
	pos  token.Pos
	mode string
}

func collectNilablePointersDirectives(file *ast.File) []nilablePointersDirective {
	if file == nil {
		return nil
	}
	var dirs []nilablePointersDirective
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			if !strings.HasPrefix(text, "go:nilable_pointers ") {
				continue
			}
			f := strings.Fields(text)
			if len(f) != 2 {
				continue
			}
			switch f[1] {
			case "enable", "disable", "warnings", "end":
				dirs = append(dirs, nilablePointersDirective{pos: c.Pos(), mode: f[1]})
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].pos < dirs[j].pos })
	return dirs
}

func fixNilReturnsForStrictPointerResults(f *ast.File, ann *ptrAnnotator) bool {
	changed := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Type == nil || fn.Type.Results == nil || fn.Body == nil {
			continue
		}
		flat := flattenFields("result", fn.Name.Name, fn.Type.Results)
		if len(flat) != 1 || plainStarType(flat[0].typ) == nil {
			continue
		}
		if ann.nilable[flat[0].key] {
			continue
		}
		fixNilReturnsInBlock(fn.Body, flat[0].typ, &changed)
	}
	return changed
}

func fixNilReturnsInBlock(body *ast.BlockStmt, typ ast.Expr, changed *bool) {
	if body == nil {
		return
	}
	for _, st := range body.List {
		switch s := st.(type) {
		case *ast.ReturnStmt:
			if len(s.Results) == 1 && isNilExpr(s.Results[0]) {
				s.Results[0] = zeroPointerExpr(typ)
				*changed = true
			}
		case *ast.BlockStmt:
			fixNilReturnsInBlock(s, typ, changed)
		case *ast.IfStmt:
			if s.Body != nil {
				fixNilReturnsInBlock(s.Body, typ, changed)
			}
			if elseBlk, ok := s.Else.(*ast.BlockStmt); ok {
				fixNilReturnsInBlock(elseBlk, typ, changed)
			} else if elseIf, ok := s.Else.(*ast.IfStmt); ok && elseIf.Body != nil {
				fixNilReturnsInBlock(elseIf.Body, typ, changed)
			}
		case *ast.ForStmt:
			if s.Body != nil {
				fixNilReturnsInBlock(s.Body, typ, changed)
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				fixNilReturnsInBlock(s.Body, typ, changed)
			}
		case *ast.SwitchStmt:
			if s.Body != nil {
				for _, cc := range s.Body.List {
					if clause, ok := cc.(*ast.CaseClause); ok {
						fixNilReturnsInBlock(&ast.BlockStmt{List: clause.Body}, typ, changed)
					}
				}
			}
		case *ast.TypeSwitchStmt:
			if s.Body != nil {
				for _, cc := range s.Body.List {
					if clause, ok := cc.(*ast.CaseClause); ok {
						fixNilReturnsInBlock(&ast.BlockStmt{List: clause.Body}, typ, changed)
					}
				}
			}
		}
	}
}

func zeroPointerExpr(typ ast.Expr) ast.Expr {
	star := plainStarType(typ)
	if star == nil {
		return &ast.Ident{Name: "nil"}
	}
	return &ast.CallExpr{Fun: &ast.Ident{Name: "new"}, Args: []ast.Expr{star.X}}
}

func fixNilReceiverReturnsInMethods(f *ast.File, ann *ptrAnnotator) bool {
	changed := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || ann.isPackageFunc(fn) || fn.Recv == nil || fn.Type == nil || fn.Type.Results == nil || fn.Body == nil {
			continue
		}
		flat := flattenFields("result", fn.Name.Name, fn.Type.Results)
		if len(flat) != 1 || plainStarType(flat[0].typ) == nil || ann.nilable[flat[0].key] {
			continue
		}
		recvName := fn.Recv.List[0].Names[0].Name
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ifs, ok := n.(*ast.IfStmt)
			if !ok || ifs.Init != nil || ifs.Else != nil {
				return true
			}
			cond, ok := ifs.Cond.(*ast.BinaryExpr)
			if !ok || cond.Op != token.EQL {
				return true
			}
			id, ok := cond.X.(*ast.Ident)
			if !ok || id.Name != recvName || !isNilExpr(cond.Y) {
				return true
			}
			if len(ifs.Body.List) != 1 {
				return true
			}
			ret, ok := ifs.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 || !isNilExpr(ret.Results[0]) {
				return true
			}
			ret.Results[0] = &ast.CallExpr{
				Fun:  &ast.Ident{Name: "panic"},
				Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"nil receiver"`}},
			}
			changed = true
			return true
		})
	}
	return changed
}

func splitNilOrReturnGuards(f *ast.File, ann *ptrAnnotator) bool {
	changed := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Type == nil || fn.Type.Results == nil || fn.Body == nil {
			continue
		}
		flat := flattenFields("result", fn.Name.Name, fn.Type.Results)
		if len(flat) != 1 || plainStarType(flat[0].typ) == nil || ann.nilable[flat[0].key] {
			continue
		}
		if splitNilOrReturnInBlock(fn.Body, flat[0].typ, &changed) {
			changed = true
		}
	}
	return changed
}

func splitNilOrReturnInBlock(body *ast.BlockStmt, resTyp ast.Expr, changed *bool) bool {
	if body == nil {
		return false
	}
	local := false
	for i := 0; i < len(body.List); i++ {
		ifs, ok := body.List[i].(*ast.IfStmt)
		if !ok || ifs.Init != nil || ifs.Else != nil {
			continue
		}
		op, ok := ast.Unparen(ifs.Cond).(*ast.BinaryExpr)
		if !ok || op.Op != token.LOR {
			continue
		}
		varName, ok := nilGuardVarName(op.X)
		if !ok {
			continue
		}
		ret, ok := lastReturn(ifs.Body)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		id, ok := ast.Unparen(ret.Results[0]).(*ast.Ident)
		if !ok || id.Name != varName {
			continue
		}
		nilIf := &ast.IfStmt{
			Cond: &ast.BinaryExpr{X: &ast.Ident{Name: varName}, Op: token.EQL, Y: &ast.Ident{Name: "nil"}},
			Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{zeroPointerExpr(resTyp)}}}},
		}
		ifs.Cond = op.Y
		body.List[i] = nilIf
		body.List = append(body.List[:i+1], append([]ast.Stmt{ifs}, body.List[i+1:]...)...)
		local = true
		*changed = true
		i++
	}
	return local
}

func nilGuardVarName(cond ast.Expr) (string, bool) {
	op, ok := ast.Unparen(cond).(*ast.BinaryExpr)
	if !ok || op.Op != token.EQL {
		return "", false
	}
	if isNilExpr(op.Y) {
		if id, ok := ast.Unparen(op.X).(*ast.Ident); ok {
			return id.Name, true
		}
	}
	if isNilExpr(op.X) {
		if id, ok := ast.Unparen(op.Y).(*ast.Ident); ok {
			return id.Name, true
		}
	}
	return "", false
}

func lastReturn(body *ast.BlockStmt) (*ast.ReturnStmt, bool) {
	if body == nil {
		return nil, false
	}
	for i := len(body.List) - 1; i >= 0; i-- {
		if ret, ok := body.List[i].(*ast.ReturnStmt); ok {
			return ret, true
		}
	}
	return nil, false
}

func fixFindPassthroughReturns(f *ast.File, ann *ptrAnnotator) bool {
	changed := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Type == nil || fn.Type.Results == nil || fn.Body == nil || boolPairResults(fn.Type.Results) {
			continue
		}
		if len(fn.Type.Results.List) != 1 {
			continue
		}
		flat := flattenFields("result", fn.Name.Name, fn.Type.Results)
		if len(flat) != 1 || plainStarType(flat[0].typ) == nil || ann.nilable[flat[0].key] {
			continue
		}
		for i, st := range fn.Body.List {
			ret, ok := st.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			call, ok := ast.Unparen(ret.Results[0]).(*ast.CallExpr)
			if !ok {
				continue
			}
			name, _, _ := callTarget(call.Fun)
			if !strings.Contains(strings.ToLower(name), "find") {
				continue
			}
			fn.Body.List[i] = &ast.AssignStmt{
				Tok: token.DEFINE,
				Lhs: []ast.Expr{&ast.Ident{Name: "_r"}},
				Rhs: []ast.Expr{call},
			}
			fn.Body.List = append(fn.Body.List[:i+1], append([]ast.Stmt{
				&ast.IfStmt{
					Cond: &ast.BinaryExpr{X: &ast.Ident{Name: "_r"}, Op: token.NEQ, Y: &ast.Ident{Name: "nil"}},
					Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{&ast.Ident{Name: "_r"}}}}},
				},
				&ast.ReturnStmt{Results: []ast.Expr{zeroPointerExpr(flat[0].typ)}},
			}, fn.Body.List[i+1:]...)...)
			changed = true
			break
		}
	}
	return changed
}

func buildNilablePointersRegions(dirs []nilablePointersDirective) []ast.NilablePointersRegion {
	var regions []ast.NilablePointersRegion
	var open *ast.NilablePointersRegion
	for _, d := range dirs {
		switch d.mode {
		case "end":
			if open != nil {
				open.End = d.pos
				regions = append(regions, *open)
				open = nil
			}
		case "enable", "disable", "warnings":
			if open != nil {
				open.End = d.pos
				regions = append(regions, *open)
			}
			open = &ast.NilablePointersRegion{Start: d.pos, Mode: d.mode}
		}
	}
	if open != nil {
		regions = append(regions, *open)
	}
	return regions
}
