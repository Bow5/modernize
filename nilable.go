package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
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
	fset            *token.FileSet
	files           []*ast.File
	nilable         map[ptrSiteKey]bool
	chanElemNilable map[ptrSiteKey]bool
	ifaceImplTypes  map[string]bool
	strictResult    map[ptrSiteKey]bool
	lookupResult    map[ptrSiteKey]bool
	typeNode        map[ptrSiteKey]ast.Expr
	funcs           map[string][]*ast.FuncDecl
}

func newPtrAnnotator(fset *token.FileSet, files []*ast.File) *ptrAnnotator {
	return &ptrAnnotator{
		fset:            fset,
		files:           files,
		nilable:         make(map[ptrSiteKey]bool),
		chanElemNilable: make(map[ptrSiteKey]bool),
		ifaceImplTypes:  make(map[string]bool),
		strictResult:    make(map[ptrSiteKey]bool),
		lookupResult:    make(map[ptrSiteKey]bool),
		typeNode:        make(map[ptrSiteKey]ast.Expr),
		funcs:           make(map[string][]*ast.FuncDecl),
	}
}

func (a *ptrAnnotator) analyze() {
	a.collectSites()
	for _, f := range a.files {
		a.collectInterfaceImplTypes(f)
		a.scanNilEvidence(f)
	}
	a.markLookupResults()
	a.propagateNilableParamToAssignedFields()
	a.dropNilableParamsUsedAsCallArgs()
	a.dropNilableReassignedParams()
	a.propagateNilableFromCalleeReturns()
	a.dropNilableResultsUsedInIteration()
	a.dropNilableParamsOnInterfaceImpls()
	a.dropNilableChannelsUsedInSyncOps()
	a.dropNilableSlicesUsedInCollectionOps()
	a.dropNilableResultsAssignedToStrictTargets()
	a.propagateNilableFromChanReceive()
	a.dropNilableFieldsUsedAsCallArgs()
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
	return isErrorType(fields.List[len(fields.List) - 1].Type)
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
				if plainRefType(tf.typ) != nil {
					a.nilable[tf.key] = true
					a.lookupResult[tf.key] = true
				}
			}
		}
	}
}

func (a *ptrAnnotator) finalizeNilableResults() {
	for key := range a.strictResult {
		if a.nilable[key] && key.kind == "field" {
			delete(a.strictResult, key)
			continue
		}
		if a.lookupResult[key] {
			continue
		}
		if key.kind == "result" {
			continue // return nil on some paths → *T?
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
	case "healing":
		return true
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
			case *ast.StructDecl:
				a.collectStructFields(d.Name.Name, &ast.StructType{Fields: d.Fields})
			case *ast.InterfaceDecl:
				a.collectInterfaceMethods(d.Name.Name, &ast.InterfaceType{Methods: d.Methods})
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
			if !ok || plainRefType(vs.Type) == nil {
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
		if plainRefType(field.Type) == nil {
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
		if plainRefType(recv.Type) != nil {
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
		if plainRefType(field.Type) == nil {
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
			if !ok || plainRefType(vs.Type) == nil {
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
	var recvName string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			curFunc = x
			recvName = recvParamName(x)
		case *ast.FuncLit:
			return true
		case *ast.IfStmt:
			if isNilReceiverGuard(x, recvName) {
				return false
			}
		case *ast.AssignStmt:
			a.scanAssign(x, curFunc)
		case *ast.ReturnStmt:
			a.scanReturn(x, curFunc)
		case *ast.CallExpr:
			a.scanCall(x, curFunc)
		case *ast.CompositeLit:
			a.scanCompositeLit(x)
		case *ast.SendStmt:
			a.scanSend(x)
		}
		return true
	})
}

func recvParamName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	if plainRefType(fn.Recv.List[0].Type) == nil {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
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
				typeName, field, ok := a.fieldOwnerName(e, fn)
				if ok {
					key := ptrSiteKey{kind: "field", owner: typeName, name: field, index: -1}
					a.nilable[key] = true
				}
			}
			continue
		}
		if id, ok := ast.Unparen(lhs).(*ast.Ident); ok {
			a.strictResult[ptrSiteKey{kind: "var", owner: owner, name: id.Name, index: -1}] = true
		}
		if sel, ok := ast.Unparen(lhs).(*ast.SelectorExpr); ok {
			if typeName, field, ok := a.fieldOwnerName(sel, fn); ok {
				key := ptrSiteKey{kind: "field", owner: typeName, name: field, index: -1}
				rhs = ast.Unparen(rhs)
				if _, ok := rhs.(*ast.Ident); ok {
					continue
				}
				if call, ok := rhs.(*ast.CallExpr); ok {
					if _, ok := a.calleeResultKey(call, 0); ok {
						continue
					}
				}
				a.strictResult[key] = true
			}
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
		if i >= len(flat) || plainRefType(flat[i].typ) == nil {
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
		if lookup && !isPointerRefType(flat[i].typ) {
			continue
		}
		a.nilable[flat[i].key] = true
	}
}

func isPointerRefType(t ast.Expr) bool {
	_, ok := ast.Unparen(plainRefType(t)).(*ast.StarExpr)
	return ok
}

func (a *ptrAnnotator) scanCall(call *ast.CallExpr, curFunc *ast.FuncDecl) {
	fnName, _, _ := callTarget(call.Fun)
	if fnName == "" {
		return
	}
	for _, fn := range a.funcs[fnName] {
		if !a.callMatchesFunc(call, fn, curFunc) {
			continue
		}
		if fn.Type == nil || fn.Type.Params == nil {
			continue
		}
		flat := flattenFields("param", fn.Name.Name, fn.Type.Params)
		for i, arg := range call.Args {
			if !isNilExpr(arg) || i >= len(flat) {
				continue
			}
			if plainRefType(flat[i].typ) != nil {
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
		if plainRefType(field.Type) == nil {
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
	typeName := compositeLitTypeName(lit)
	if typeName == "" {
		return
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		field, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		key := ptrSiteKey{kind: "field", owner: typeName, name: field.Name, index: -1}
		if a.typeNode[key] == nil {
			continue
		}
		if isNilExpr(kv.Value) {
			a.nilable[key] = true
			continue
		}
		a.strictResult[key] = true
	}
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

func (a *ptrAnnotator) calleeResultKeyAt(call *ast.CallExpr, resultIndex int) (ptrSiteKey, bool) {
	name, _, isMethod := callTarget(call.Fun)
	if name == "" {
		return ptrSiteKey{}, false
	}
	for _, fn := range a.funcs[name] {
		if isMethod {
			if fn.Recv == nil {
				continue
			}
		} else if fn.Recv != nil {
			continue
		}
		if fn.Type == nil || fn.Type.Results == nil {
			continue
		}
		flat := flattenFields("result", fn.Name.Name, fn.Type.Results)
		if resultIndex >= len(flat) {
			continue
		}
		return flat[resultIndex].key, true
	}
	return ptrSiteKey{}, false
}

func (a *ptrAnnotator) calleeResultKey(call *ast.CallExpr, resultIndex int) (ptrSiteKey, bool) {
	key, ok := a.calleeResultKeyAt(call, resultIndex)
	if !ok || !a.nilable[key] {
		return ptrSiteKey{}, false
	}
	return key, true
}

func (a *ptrAnnotator) markAssignTargetNilable(lhs ast.Expr, owner string, fn *ast.FuncDecl) bool {
	switch e := ast.Unparen(lhs).(type) {
	case *ast.Ident:
		key := ptrSiteKey{kind: "var", owner: owner, name: e.Name, index: -1}
		if a.typeNode[key] != nil {
			if a.nilable[key] {
				delete(a.strictResult, key)
				return false
			}
			a.nilable[key] = true
			delete(a.strictResult, key)
			return true
		}
		if fn != nil && fn.Type != nil && fn.Type.Params != nil {
			for _, tf := range flattenFields("param", fn.Name.Name, fn.Type.Params) {
				if tf.key.name != e.Name {
					continue
				}
				if a.nilable[tf.key] {
					delete(a.strictResult, tf.key)
					return false
				}
				a.nilable[tf.key] = true
				delete(a.strictResult, tf.key)
				return true
			}
		}
		if fn != nil && fn.Type != nil && fn.Type.Results != nil {
			for _, tf := range flattenFields("result", fn.Name.Name, fn.Type.Results) {
				if tf.key.name != e.Name {
					continue
				}
				if a.nilable[tf.key] {
					delete(a.strictResult, tf.key)
					return false
				}
				a.nilable[tf.key] = true
				delete(a.strictResult, tf.key)
				return true
			}
		}
		return false
	case *ast.SelectorExpr:
		field := e.Sel.Name
		typeName := ""
		if recv, ok := e.X.(*ast.Ident); ok && fn != nil && fn.Recv != nil {
			recvName, recvType, ok := recvNameAndType(fn.Recv)
			if ok && recv.Name == recvName {
				typeName = recvType
			}
		}
		if typeName == "" {
			var ok bool
			typeName, field, ok = selectorField(e)
			if !ok {
				return false
			}
		}
		key := ptrSiteKey{kind: "field", owner: typeName, name: field, index: -1}
		if a.typeNode[key] == nil {
			return false
		}
		if a.nilable[key] {
			return false
		}
		a.nilable[key] = true
		return true
	default:
		return false
	}
}

func (a *ptrAnnotator) propagateNilableFromCalleeReturns() {
	for a.propagateNilableFromCalleeReturnsOnce() {
	}
}

func (a *ptrAnnotator) propagateNilableFromCalleeReturnsOnce() bool {
	changed := false
	for _, f := range a.files {
		var curFunc *ast.FuncDecl
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				curFunc = x
			case *ast.FuncLit:
				return false
			case *ast.ReturnStmt:
				if curFunc == nil || curFunc.Type == nil || curFunc.Type.Results == nil || boolPairResults(curFunc.Type.Results) {
					return true
				}
				flat := flattenFields("result", curFunc.Name.Name, curFunc.Type.Results)
				for i, expr := range x.Results {
					if i >= len(flat) || plainRefType(flat[i].typ) == nil {
						continue
					}
					call, ok := ast.Unparen(expr).(*ast.CallExpr)
					if !ok {
						continue
					}
					if _, ok := a.calleeResultKey(call, i); !ok {
						continue
					}
					key := flat[i].key
					if !a.nilable[key] {
						changed = true
					}
					a.nilable[key] = true
					delete(a.strictResult, key)
				}
			case *ast.AssignStmt:
				owner := ""
				if curFunc != nil {
					owner = curFunc.Name.Name
				}
				for i, lhs := range x.Lhs {
					var call *ast.CallExpr
					resultIdx := 0
					if len(x.Rhs) == 1 {
						call, _ = ast.Unparen(x.Rhs[0]).(*ast.CallExpr)
						resultIdx = i
					} else {
						call, _ = ast.Unparen(rhsAt(x.Rhs, i)).(*ast.CallExpr)
					}
					if call == nil {
						continue
					}
					if _, ok := a.calleeResultKey(call, resultIdx); !ok {
						continue
					}
					if a.markAssignTargetNilable(lhs, owner, curFunc) {
						changed = true
					}
				}
			case *ast.CompositeLit:
				typeName := compositeLitTypeName(x)
				if typeName == "" {
					return true
				}
				for _, elt := range x.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					field, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					call, ok := ast.Unparen(kv.Value).(*ast.CallExpr)
					if !ok {
						continue
					}
					if _, ok := a.calleeResultKey(call, 0); !ok {
						continue
					}
					key := ptrSiteKey{kind: "field", owner: typeName, name: field.Name, index: -1}
					if a.typeNode[key] == nil {
						continue
					}
					if !a.nilable[key] {
						changed = true
					}
					a.nilable[key] = true
				}
			}
			return true
		})
	}
	return changed
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

func (a *ptrAnnotator) fieldOwnerName(sel *ast.SelectorExpr, fn *ast.FuncDecl) (typeName, field string, ok bool) {
	field = sel.Sel.Name
	if fn != nil && fn.Recv != nil {
		if recvName, recvType, rok := recvNameAndType(fn.Recv); rok && recvType != "" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == recvName {
				return recvType, field, true
			}
		}
	}
	if id, ok := sel.X.(*ast.Ident); ok && fn != nil {
		key := ptrSiteKey{kind: "var", owner: fn.Name.Name, name: id.Name, index: -1}
		if typ := a.typeNode[key]; typ != nil {
			if tn := typeNameFromExpr(typ); tn != "" {
				return tn, field, true
			}
		}
		if tn := a.inferIdentType(fn, id.Name); tn != "" {
			return tn, field, true
		}
	}
	return selectorField(sel)
}

func (a *ptrAnnotator) scanSend(s *ast.SendStmt) {
	if !isNilExpr(s.Value) {
		return
	}
	sel, ok := s.Chan.(*ast.SelectorExpr)
	if !ok {
		return
	}
	field := sel.Sel.Name
	for key, typ := range a.typeNode {
		if key.kind != "field" || key.name != field {
			continue
		}
		if ch, ok := ast.Unparen(typ).(*ast.ChanType); ok && ch.Value != nil {
			a.chanElemNilable[key] = true
		}
	}
}

func (a *ptrAnnotator) markNilableFromChanRecv(lhs ast.Expr, ch ast.Expr, fn *ast.FuncDecl) {
	sel, ok := ast.Unparen(ch).(*ast.SelectorExpr)
	if !ok {
		return
	}
	field := sel.Sel.Name
	for key := range a.chanElemNilable {
		if key.name != field {
			continue
		}
		switch e := ast.Unparen(lhs).(type) {
		case *ast.Ident:
			a.markVarNilable(fn, e.Name)
		case *ast.SelectorExpr:
			if typeName, name, ok := a.fieldOwnerName(e, fn); ok {
				a.nilable[ptrSiteKey{kind: "field", owner: typeName, name: name, index: -1}] = true
			}
		}
		_ = key
	}
}

func (a *ptrAnnotator) propagateNilableFromChanReceive() {
	for _, f := range a.files {
		var curFunc *ast.FuncDecl
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				curFunc = x
			case *ast.FuncLit:
				return true
			case *ast.AssignStmt:
				for i, lhs := range x.Lhs {
					unary, ok := ast.Unparen(rhsAt(x.Rhs, i)).(*ast.UnaryExpr)
					if !ok || unary.Op != token.ARROW {
						continue
					}
					a.markNilableFromChanRecv(lhs, unary.X, curFunc)
				}
			}
			return true
		})
	}
}

func (a *ptrAnnotator) dropNilableFieldsUsedAsCallArgs() {
	for _, f := range a.files {
		var curFunc *ast.FuncDecl
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				curFunc = x
			case *ast.FuncLit:
				return true
			case *ast.CallExpr:
				for _, arg := range x.Args {
					sel, ok := ast.Unparen(arg).(*ast.SelectorExpr)
					if !ok {
						continue
					}
					typeName := a.exprTypeName(sel.X, curFunc)
					if typeName == "" {
						continue
					}
					key := ptrSiteKey{kind: "field", owner: typeName, name: sel.Sel.Name, index: -1}
					delete(a.nilable, key)
				}
			}
			return true
		})
	}
}

func rewriteMakeChanElemNilable(f *ast.File, ann *ptrAnnotator) bool {
	if len(ann.chanElemNilable) == 0 {
		return false
	}
	changed := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "make" {
			return true
		}
		ch, ok := ast.Unparen(call.Args[0]).(*ast.ChanType)
		if !ok {
			return true
		}
		arr, ok := ast.Unparen(ch.Value).(*ast.ArrayType)
		if !ok || arr.Len != nil {
			return true
		}
		newVal := setRefNilable(ch.Value, true)
		if newVal == ch.Value {
			return true
		}
		call.Args[0] = &ast.ChanType{Dir: ch.Dir, Value: newVal}
		changed = true
		return true
	})
	return changed
}

func rewriteChanNilSends(f *ast.File, ann *ptrAnnotator) bool {
	changed := false
	ast.Inspect(f, func(n ast.Node) bool {
		s, ok := n.(*ast.SendStmt)
		if !ok || !isNilExpr(s.Value) {
			return true
		}
		sel, ok := s.Chan.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		for key, typ := range ann.typeNode {
			if key.kind != "field" || key.name != sel.Sel.Name {
				continue
			}
			ch, ok := ast.Unparen(typ).(*ast.ChanType)
			if !ok || ch.Value == nil {
				continue
			}
			if arr, ok := ast.Unparen(ch.Value).(*ast.ArrayType); ok && arr.Len == nil {
				s.Value = &ast.CompositeLit{Type: ch.Value}
				changed = true
				break
			}
		}
		return true
	})
	return changed
}

func ifaceImplTypeName(v ast.Expr) string {
	switch x := ast.Unparen(v).(type) {
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return typeNameFromExpr(x.X)
		}
	case *ast.CompositeLit:
		return typeNameFromExpr(x.Type)
	case *ast.CallExpr:
		if star, ok := ast.Unparen(x.Fun).(*ast.StarExpr); ok {
			return typeNameFromExpr(star.X)
		}
	}
	return ""
}

func (a *ptrAnnotator) collectInterfaceImplTypes(f *ast.File) {
	for _, decl := range f.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, spec := range g.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "_" || len(vs.Values) != 1 {
				continue
			}
			if typeName := ifaceImplTypeName(vs.Values[0]); typeName != "" {
				a.ifaceImplTypes[typeName] = true
			}
		}
	}
}

func (a *ptrAnnotator) dropNilableResultsUsedInIteration() {
	for _, f := range a.files {
		var curFunc *ast.FuncDecl
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				curFunc = x
			case *ast.FuncLit:
				return false
			case *ast.RangeStmt:
				if call, ok := ast.Unparen(x.X).(*ast.CallExpr); ok {
					if key, ok := a.calleeResultKey(call, 0); ok {
						delete(a.nilable, key)
					}
				}
				if id, ok := ast.Unparen(x.X).(*ast.Ident); ok && curFunc != nil {
					a.clearNilableForIterSource(curFunc, id.Name)
				}
			}
			return true
		})
	}
}

func (a *ptrAnnotator) clearNilableForVarSources(fn *ast.FuncDecl, varName string) {
	if fn == nil {
		return
	}
	if fn.Type != nil && fn.Type.Results != nil {
		for _, tf := range flattenFields("result", fn.Name.Name, fn.Type.Results) {
			if tf.key.name == varName {
				delete(a.nilable, tf.key)
			}
		}
	}
	if fn.Body == nil {
		return
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			id, ok := ast.Unparen(lhs).(*ast.Ident)
			if !ok || id.Name != varName {
				continue
			}
			var call *ast.CallExpr
			resultIdx := i
			if len(assign.Rhs) == 1 {
				call, _ = ast.Unparen(assign.Rhs[0]).(*ast.CallExpr)
			} else {
				call, _ = ast.Unparen(rhsAt(assign.Rhs, i)).(*ast.CallExpr)
			}
			if call != nil {
				if key, ok := a.calleeResultKeyAt(call, resultIdx); ok {
					delete(a.nilable, key)
				}
			}
		}
		return true
	})
}

func (a *ptrAnnotator) clearNilableForIterSource(fn *ast.FuncDecl, varName string) {
	a.clearNilableForVarSources(fn, varName)
}

func (a *ptrAnnotator) dropNilableParamsOnInterfaceImpls() {
	for _, fns := range a.funcs {
		for _, fn := range fns {
			if fn.Recv == nil || fn.Type == nil || fn.Type.Params == nil {
				continue
			}
			_, recvType, ok := recvNameAndType(fn.Recv)
			if !ok || !a.ifaceImplTypes[recvType] {
				continue
			}
			for _, tf := range flattenFields("param", fn.Name.Name, fn.Type.Params) {
				delete(a.nilable, tf.key)
			}
		}
	}
}

func (a *ptrAnnotator) dropNilableChannelsUsedInSyncOps() {
	used := make(map[ptrSiteKey]bool)
	for _, f := range a.files {
		var curFunc *ast.FuncDecl
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				curFunc = x
			case *ast.FuncLit:
				return true
			case *ast.CallExpr:
				for _, arg := range x.Args {
					a.markChanExprUsed(arg, curFunc, used)
				}
				if id, ok := x.Fun.(*ast.Ident); ok && len(x.Args) == 1 {
					if id.Name == "len" || id.Name == "close" {
						a.markChanExprUsed(x.Args[0], curFunc, used)
					}
				}
			case *ast.UnaryExpr:
				if x.Op == token.ARROW {
					a.markChanExprUsed(x.X, curFunc, used)
				}
			case *ast.SendStmt:
				a.markChanExprUsed(x.Chan, curFunc, used)
			}
			return true
		})
	}
	for key := range used {
		if typ := a.typeNode[key]; typ != nil {
			if _, ok := ast.Unparen(plainRefType(typ)).(*ast.ChanType); ok {
				delete(a.nilable, key)
			}
		}
	}
}

func (a *ptrAnnotator) markChanExprUsed(e ast.Expr, curFunc *ast.FuncDecl, used map[ptrSiteKey]bool) {
	e = ast.Unparen(e)
	if id, ok := e.(*ast.Ident); ok && curFunc != nil {
		a.clearNilableForVarSources(curFunc, id.Name)
	}
	for _, key := range a.exprRefKeys(e, curFunc) {
		used[key] = true
	}
}

func (a *ptrAnnotator) dropNilableSlicesUsedInCollectionOps() {
	used := make(map[ptrSiteKey]bool)
	for _, f := range a.files {
		var curFunc *ast.FuncDecl
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				curFunc = x
			case *ast.FuncLit:
				return true
			case *ast.RangeStmt:
				a.markSliceExprUsed(x.X, curFunc, used)
			case *ast.IndexExpr:
				a.markSliceExprUsed(x.X, curFunc, used)
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && len(x.Args) > 0 {
					if id.Name == "append" || id.Name == "len" {
						a.markSliceExprUsed(x.Args[0], curFunc, used)
					}
				}
				for _, arg := range x.Args {
					a.markSliceExprUsed(arg, curFunc, used)
				}
			}
			return true
		})
	}
	for key := range used {
		if key.kind == "field" {
			continue
		}
		if typ := a.typeNode[key]; typ != nil {
			if arr, ok := ast.Unparen(plainRefType(typ)).(*ast.ArrayType); ok && arr.Len == nil {
				delete(a.nilable, key)
			}
		}
	}
}

func (a *ptrAnnotator) markSliceExprUsed(e ast.Expr, curFunc *ast.FuncDecl, used map[ptrSiteKey]bool) {
	e = ast.Unparen(e)
	if id, ok := e.(*ast.Ident); ok && curFunc != nil {
		a.clearNilableForVarSources(curFunc, id.Name)
	}
	for _, key := range a.exprRefKeys(e, curFunc) {
		used[key] = true
	}
}

func (a *ptrAnnotator) dropNilableResultsAssignedToStrictTargets() {
	for _, f := range a.files {
		var curFunc *ast.FuncDecl
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				curFunc = x
			case *ast.FuncLit:
				return false
			case *ast.AssignStmt:
				owner := ""
				if curFunc != nil {
					owner = curFunc.Name.Name
				}
				for i, lhs := range x.Lhs {
					id, ok := ast.Unparen(lhs).(*ast.Ident)
					if !ok {
						continue
					}
					key := ptrSiteKey{kind: "var", owner: owner, name: id.Name, index: -1}
					if !a.isStrictRefType(key) {
						continue
					}
					var call *ast.CallExpr
					resultIdx := i
					if len(x.Rhs) == 1 {
						call, _ = ast.Unparen(x.Rhs[0]).(*ast.CallExpr)
					} else {
						call, _ = ast.Unparen(rhsAt(x.Rhs, i)).(*ast.CallExpr)
					}
					if call != nil {
						if rk, ok := a.calleeResultKeyAt(call, resultIdx); ok {
							delete(a.nilable, rk)
						}
					}
				}
			case *ast.ReturnStmt:
				if curFunc == nil || curFunc.Type == nil || curFunc.Type.Results == nil {
					return true
				}
				flat := flattenFields("result", curFunc.Name.Name, curFunc.Type.Results)
				for i, expr := range x.Results {
					if i >= len(flat) || !a.isStrictRefType(flat[i].key) {
						continue
					}
					call, ok := ast.Unparen(expr).(*ast.CallExpr)
					if !ok {
						continue
					}
					if rk, ok := a.calleeResultKeyAt(call, i); ok {
						delete(a.nilable, rk)
					}
				}
			}
			return true
		})
	}
}

func (a *ptrAnnotator) isStrictRefType(key ptrSiteKey) bool {
	typ := a.typeNode[key]
	if typ == nil || plainRefType(typ) == nil {
		return false
	}
	if _, ok := ast.Unparen(typ).(*ast.NilableTypeExpr); ok {
		return false
	}
	switch ast.Unparen(plainRefType(typ)).(type) {
	case *ast.ArrayType, *ast.ChanType, *ast.MapType, *ast.StarExpr:
		return true
	}
	return false
}

func (a *ptrAnnotator) isStrictNonNilableContainer(key ptrSiteKey) bool {
	return a.isStrictRefType(key)
}

func (a *ptrAnnotator) exprRefKeys(e ast.Expr, curFunc *ast.FuncDecl) []ptrSiteKey {
	e = ast.Unparen(e)
	switch x := e.(type) {
	case *ast.Ident:
		var keys []ptrSiteKey
		if curFunc != nil {
			keys = append(keys, ptrSiteKey{kind: "var", owner: curFunc.Name.Name, name: x.Name, index: -1})
			if curFunc.Type != nil {
				if curFunc.Type.Results != nil {
					for _, tf := range flattenFields("result", curFunc.Name.Name, curFunc.Type.Results) {
						if tf.key.name == x.Name {
							keys = append(keys, tf.key)
						}
					}
				}
				if curFunc.Type.Params != nil {
					for _, tf := range flattenFields("param", curFunc.Name.Name, curFunc.Type.Params) {
						if tf.key.name == x.Name {
							keys = append(keys, tf.key)
						}
					}
				}
			}
			return keys
		}
		return []ptrSiteKey{{kind: "var", name: x.Name, index: -1}}
	case *ast.SelectorExpr:
		if typeName, field, ok := a.fieldOwnerName(x, curFunc); ok {
			return []ptrSiteKey{{kind: "field", owner: typeName, name: field, index: -1}}
		}
	}
	return nil
}

func (a *ptrAnnotator) callMatchesFunc(call *ast.CallExpr, fn *ast.FuncDecl, curFunc *ast.FuncDecl) bool {
	if fn == nil || fn.Name == nil {
		return false
	}
	name, _, isMethod := callTarget(call.Fun)
	if fn.Name.Name != name {
		return false
	}
	if fn.Recv == nil {
		return !isMethod
	}
	if !isMethod {
		return false
	}
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recvType := a.exprTypeName(sel.X, curFunc)
	if recvType == "" {
		return false
	}
	_, wantType, ok := recvNameAndType(fn.Recv)
	if !ok || wantType == "" {
		return false
	}
	return typesMatch(recvType, wantType)
}

func typesMatch(got, want string) bool {
	if got == want {
		return true
	}
	if strings.TrimPrefix(got, "*") == want {
		return true
	}
	if got == "*" + want {
		return true
	}
	return false
}

func (a *ptrAnnotator) exprTypeName(e ast.Expr, curFunc *ast.FuncDecl) string {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		if curFunc != nil {
			if curFunc.Recv != nil {
				recvName, recvType, ok := recvNameAndType(curFunc.Recv)
				if ok && x.Name == recvName {
					return recvType
				}
			}
			if curFunc.Type != nil {
				if curFunc.Type.Params != nil {
					for _, tf := range flattenFields("param", curFunc.Name.Name, curFunc.Type.Params) {
						if tf.key.name == x.Name {
							return typeNameFromExpr(tf.typ)
						}
					}
				}
				if curFunc.Type.Results != nil {
					for _, tf := range flattenFields("result", curFunc.Name.Name, curFunc.Type.Results) {
						if tf.key.name == x.Name {
							return typeNameFromExpr(tf.typ)
						}
					}
				}
			}
			key := ptrSiteKey{kind: "var", owner: curFunc.Name.Name, name: x.Name, index: -1}
			if typ := a.typeNode[key]; typ != nil {
				return typeNameFromExpr(typ)
			}
		}
		key := ptrSiteKey{kind: "var", name: x.Name, index: -1}
		if typ := a.typeNode[key]; typ != nil {
			return typeNameFromExpr(typ)
		}
		if curFunc != nil {
			if tn := a.inferIdentType(curFunc, x.Name); tn != "" {
				return tn
			}
		}
	case *ast.SelectorExpr:
		if typeName, _, ok := a.fieldOwnerName(x, curFunc); ok {
			key := ptrSiteKey{kind: "field", owner: typeName, name: x.Sel.Name, index: -1}
			if typ := a.typeNode[key]; typ != nil {
				return typeNameFromExpr(typ)
			}
		}
	}
	return ""
}

func selectorField(sel *ast.SelectorExpr) (typeName, field string, ok bool) {
	switch x := sel.X.(type) {
	case *ast.Ident:
		return x.Name, sel.Sel.Name, true
	default:
		return "", sel.Sel.Name, false
	}
}

func (a *ptrAnnotator) countVerifiedNonNilPointers() int {
	n := 0
	for key, typ := range a.typeNode {
		if plainRefType(typ) == nil {
			continue
		}
		if a.nilable[key] {
			continue
		}
		if nptDisabledAt(a.fset, typ.Pos(), a.files) {
			continue
		}
		n++
	}
	return n
}

var moduleNilableSliceFields map[string]ast.Expr

func buildModuleNilableSliceFields(pkgs []pkgFiles) map[string]ast.Expr {
	out := make(map[string]ast.Expr)
	for _, pkg := range pkgs {
		fset := token.NewFileSet()
		files := make([]*ast.File, len(pkg.paths))
		okPkg := true
		for i, path := range pkg.paths {
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				okPkg = false
				break
			}
			f.NilablePointersRegions = buildNilablePointersRegions(collectNilablePointersDirectives(f))
			files[i] = f
		}
		if !okPkg || len(files) == 0 {
			continue
		}
		ann := newPtrAnnotator(fset, files)
		ann.analyze()
		for key, nilable := range ann.nilable {
			if !nilable || key.kind != "field" {
				continue
			}
			typ := ann.typeNode[key]
			ref := plainRefType(typ)
			arr, ok := ref.(*ast.ArrayType)
			if !ok || arr.Len != nil {
				continue
			}
			out[key.owner + "." + key.name] = ref
		}
	}
	return out
}

func moduleNilableFieldBySuffix(field string) (ast.Expr, bool) {
	var typ ast.Expr
	var count int
	for ref, t := range moduleNilableSliceFields {
		if !strings.HasSuffix(ref, "." + field) {
			continue
		}
		if count > 0 {
			return nil, false
		}
		typ = t
		count++
	}
	return typ, count == 1
}

func (a *ptrAnnotator) appendNilableSliceCoalesceEdit(edits *[]sourceEdit, arg ast.Expr, curFunc *ast.FuncDecl, path string, fset *token.FileSet) {
	var typ ast.Expr
	var expr ast.Expr
	switch e := ast.Unparen(arg).(type) {
	case *ast.SelectorExpr:
		if ownerType, field, ok := a.fieldOwnerName(e, curFunc); ok {
			key := ptrSiteKey{kind: "field", owner: ownerType, name: field, index: -1}
			if a.nilable[key] {
				typ = a.typeNode[key]
				expr = e
				break
			}
			if typ, ok = moduleNilableSliceFields[ownerType + "." + field]; ok {
				expr = e
				break
			}
		}
		typeName := a.exprTypeName(e.X, curFunc)
		var ok bool
		typ, ok = moduleNilableSliceFields[typeName + "." + e.Sel.Name]
		if !ok {
			typ, ok = moduleNilableFieldBySuffix(e.Sel.Name)
		}
		if !ok {
			return
		}
		expr = e
	case *ast.Ident:
		owner := ""
		if curFunc != nil {
			owner = curFunc.Name.Name
		}
		key := ptrSiteKey{kind: "var", owner: owner, name: e.Name, index: -1}
		if !a.nilable[key] {
			return
		}
		typ = a.typeNode[key]
		expr = e
	default:
		return
	}
	ref := plainRefType(typ)
	if ref == nil {
		return
	}
	if _, ok := ast.Unparen(ref).(*ast.ArrayType); !ok {
		return
	}
	start := fset.Position(expr.Pos()).Offset
	end := fset.Position(expr.End()).Offset
	zero := formatTypeZero(ref)
	*edits = append(*edits, sourceEdit{
		start: start,
		end:   end,
		text:  []byte("(" + stringFromFile(path, start, end) + " ?? " + zero + ")"),
	})
}

func rewriteNilableSliceFieldArgs(f *ast.File, fset *token.FileSet, ann *ptrAnnotator, path string) bool {
	var edits []sourceEdit
	var curFunc *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			curFunc = x
		case *ast.FuncLit:
			return true
		case *ast.CallExpr:
			for _, arg := range x.Args {
				ann.appendNilableSliceCoalesceEdit(&edits, arg, curFunc, path, fset)
			}
		}
		return true
	})
	if len(edits) == 0 {
		return false
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	newSrc := applySourceEdits(src, edits)
	if err := os.WriteFile(path, newSrc, 0); err != nil {
		return false
	}
	// re-parse file into f for subsequent writes
	reparsed, err := parser.ParseFile(fset, path, newSrc, parser.ParseComments)
	if err != nil {
		return false
	}
	reparsed.NilablePointersRegions = f.NilablePointersRegions
	*f = *reparsed
	return true
}

func coalesceModuleSliceFieldCallArgs(f *ast.File, fset *token.FileSet, path string) bool {
	if len(moduleNilableSliceFields) == 0 {
		return false
	}
	var edits []sourceEdit
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, arg := range call.Args {
			sel, ok := ast.Unparen(arg).(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				continue
			}
			typ, ok := moduleNilableFieldBySuffix(sel.Sel.Name)
			if !ok {
				continue
			}
			ref := plainRefType(typ)
			if ref == nil {
				continue
			}
			if _, ok := ast.Unparen(ref).(*ast.ArrayType); !ok {
				continue
			}
			start := fset.Position(sel.Pos()).Offset
			end := fset.Position(sel.End()).Offset
			snippet := stringFromFile(path, start, end)
			if strings.Contains(snippet, "??") {
				continue
			}
			zero := formatTypeZero(ref)
			edits = append(edits, sourceEdit{
				start: start,
				end:   end,
				text:  []byte("(" + stringFromFile(path, start, end) + " ?? " + zero + ")"),
			})
		}
		return true
	})
	if len(edits) == 0 {
		return false
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	newSrc := applySourceEdits(src, edits)
	if err := os.WriteFile(path, newSrc, 0); err != nil {
		return false
	}
	reparsed, err := parser.ParseFile(fset, path, newSrc, parser.ParseComments)
	if err != nil {
		return false
	}
	reparsed.NilablePointersRegions = f.NilablePointersRegions
	*f = *reparsed
	return true
}

func stringFromFile(path string, start, end int) string {
	src, err := os.ReadFile(path)
	if err != nil || start < 0 || end > len(src) || start >= end {
		return ""
	}
	return string(src[start:end])
}

func formatTypeZero(typ ast.Expr) string {
	ref := plainRefType(typ)
	if ref == nil {
		return "{}"
	}
	var buf bytes.Buffer
	_ = format.Node(&buf, token.NewFileSet(), ref)
	return buf.String() + "{}"
}

func applyPtrAnnotations(fset *token.FileSet, paths []string, files []*ast.File) (changed []bool, verifiedNonNil int) {
	ann := newPtrAnnotator(fset, files)
	ann.analyze()
	verifiedNonNil = ann.countVerifiedNonNilPointers()
	changed = make([]bool, len(files))
	for i, f := range files {
		path := ""
		if i < len(paths) {
			path = paths[i]
		}
		fileChanged := false
		if rewriteFilePointerTypes(fset, f, ann) {
			fileChanged = true
		}
		if rewriteChanNilSends(f, ann) {
			fileChanged = true
		}
		if rewriteNilableSliceFieldArgs(f, fset, ann, path) {
			fileChanged = true
		}
		if coalesceModuleSliceFieldCallArgs(f, fset, path) {
			fileChanged = true
		}
		if splitNilOrReturnGuards(fset, f, ann) {
			fileChanged = true
		}
		if fixFindPassthroughReturns(fset, f, ann) {
			fileChanged = true
		}
		if syncMethodReturnsForNilableFields(fset, f, ann) {
			fileChanged = true
		}
		changed[i] = fileChanged
	}
	return changed, verifiedNonNil
}

func syncMethodReturnsForNilableFields(fset *token.FileSet, f *ast.File, ann *ptrAnnotator) bool {
	changed := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		if plainRefType(fn.Type.Results.List[0].Type) == nil {
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

func (a *ptrAnnotator) inferIdentType(fn *ast.FuncDecl, name string) string {
	if fn == nil || fn.Body == nil {
		return ""
	}
	var typeName string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE {
			return true
		}
		for i, lhs := range assign.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name != name {
				continue
			}
			rhs := ast.Unparen(rhsAt(assign.Rhs, i))
			if fe, ok := rhs.(*ast.ForceExpr); ok {
				rhs = ast.Unparen(fe.X)
			}
			if call, ok := rhs.(*ast.CallExpr); ok {
				if tn := a.calleeResultTypeName(call, fn); tn != "" {
					typeName = tn
					return false
				}
			}
		}
		return true
	})
	return typeName
}

func (a *ptrAnnotator) calleeResultTypeName(call *ast.CallExpr, fn *ast.FuncDecl) string {
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok && sel.Sel != nil {
		recvType := ""
		if id, ok := sel.X.(*ast.Ident); ok && fn != nil {
			key := ptrSiteKey{kind: "var", owner: fn.Name.Name, name: id.Name, index: -1}
			if typ := a.typeNode[key]; typ != nil {
				recvType = typeNameFromExpr(typ)
			}
			if recvType == "" && fn.Type != nil && fn.Type.Params != nil {
				for _, tf := range flattenFields("param", fn.Name.Name, fn.Type.Params) {
					if tf.key.name == id.Name {
						recvType = typeBaseName(tf.typ)
						break
					}
				}
			}
		}
		for _, m := range a.funcs[sel.Sel.Name] {
			if m.Recv == nil || m.Type == nil || m.Type.Results == nil || len(m.Type.Results.List) != 1 {
				continue
			}
			_, wantType, ok := recvNameAndType(m.Recv)
			if !ok || (recvType != "" && !typesMatch(recvType, wantType)) {
				continue
			}
			return typeNameFromExpr(m.Type.Results.List[0].Type)
		}
	}
	name, _, _ := callTarget(call.Fun)
	for _, fn := range a.funcs[name] {
		if fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		return typeNameFromExpr(fn.Type.Results.List[0].Type)
	}
	return ""
}

func typeNameFromExpr(t ast.Expr) string {
	switch x := ast.Unparen(t).(type) {
	case *ast.ResultTypeExpr:
		return typeNameFromExpr(x.X)
	case *ast.NilableTypeExpr:
		return typeNameFromExpr(x.X)
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
		case *ast.StructDecl:
			if rewriteStructFieldTypes(fset, f, d.Name.Name, &ast.StructType{Fields: d.Fields}, ann) {
				changed = true
			}
		case *ast.InterfaceDecl:
			if rewriteInterfaceMethodTypes(fset, f, &ast.InterfaceType{Methods: d.Methods}, ann) {
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
			if !ok || plainRefType(vs.Type) == nil {
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
		if plainRefType(field.Type) == nil {
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
		if ft.Results != nil && !stdErrPairResults(ft.Results) {
			if rewriteFieldListTypes(fset, f, "result", mname, ft.Results, ann) {
				changed = true
			}
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
			if plainRefType(recv.Type) == nil {
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
	if fn.Type.Results != nil && !stdErrPairResults(fn.Type.Results) {
		if rewriteFieldListTypes(fset, f, "result", name, fn.Type.Results, ann) {
			changed = true
		}
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
		if plainRefType(field.Type) == nil {
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
			if !ok || plainRefType(vs.Type) == nil {
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

func plainRefType(t ast.Expr) ast.Expr {
	t = ast.Unparen(t)
	if nilable, ok := t.(*ast.NilableTypeExpr); ok {
		t = ast.Unparen(nilable.X)
	}
	switch x := t.(type) {
	case *ast.StarExpr:
		if isMigratablePointer(x) {
			return t
		}
	case *ast.ArrayType:
		if x.Len == nil {
			return t
		}
	case *ast.MapType:
		return t
	case *ast.ChanType:
		return t
	}
	return nil
}

func plainStarType(t ast.Expr) *ast.StarExpr {
	t = plainRefType(t)
	star, ok := t.(*ast.StarExpr)
	if !ok {
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

func setRefNilable(t ast.Expr, nilable bool) ast.Expr {
	t = ast.Unparen(t)
	if nilable {
		if _, ok := t.(*ast.NilableTypeExpr); ok {
			return t
		}
		if plainRefType(t) == nil {
			return t
		}
		return &ast.NilableTypeExpr{X: t, QPos: t.End()}
	}
	if n, ok := t.(*ast.NilableTypeExpr); ok {
		return n.X
	}
	return t
}

func setPointerNilable(t ast.Expr, nilable bool) ast.Expr {
	return setRefNilable(t, nilable)
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

func splitNilOrReturnGuards(fset *token.FileSet, f *ast.File, ann *ptrAnnotator) bool {
	changed := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Type == nil || fn.Type.Results == nil || fn.Body == nil {
			continue
		}
		flat := flattenFields("result", fn.Name.Name, fn.Type.Results)
		if len(flat) != 1 || plainRefType(flat[0].typ) == nil || ann.nilable[flat[0].key] {
			continue
		}
		if splitNilOrReturnInBlock(fn.Body, &changed) {
			ann.nilable[flat[0].key] = true
			if newTyp, ok := rewriteTypeAt(fset, f, fn.Type.Results.List[0].Type, true); ok {
				fn.Type.Results.List[0].Type = newTyp
			}
			changed = true
		}
	}
	return changed
}

func splitNilOrReturnInBlock(body *ast.BlockStmt, changed *bool) bool {
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
			Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{&ast.Ident{Name: "nil"}}}}},
		}
		ifs.Cond = op.Y
		body.List[i] = nilIf
		body.List = append(body.List[:i + 1], append([]ast.Stmt{ifs}, body.List[i + 1:]...)...)
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

func fixFindPassthroughReturns(fset *token.FileSet, f *ast.File, ann *ptrAnnotator) bool {
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
		if len(flat) != 1 || plainRefType(flat[0].typ) == nil || ann.nilable[flat[0].key] {
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
			fn.Body.List = append(fn.Body.List[:i + 1], append([]ast.Stmt{
				&ast.IfStmt{
					Cond: &ast.BinaryExpr{X: &ast.Ident{Name: "_r"}, Op: token.NEQ, Y: &ast.Ident{Name: "nil"}},
					Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{&ast.Ident{Name: "_r"}}}}},
				},
				&ast.ReturnStmt{Results: []ast.Expr{&ast.Ident{Name: "nil"}}},
			}, fn.Body.List[i + 1:]...)...)
			ann.nilable[flat[0].key] = true
			if newTyp, ok := rewriteTypeAt(fset, f, fn.Type.Results.List[0].Type, true); ok {
				fn.Type.Results.List[0].Type = newTyp
			}
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
