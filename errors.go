package main

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

var messageFieldNames = map[string]bool{
	"msg": true, "message": true, "text": true, "description": true,
	"detail": true, "reason": true,
}

type customErrorType struct {
	name          string
	structType    *ast.StructType
	embedOnly     bool   // only errors.Base after rewrite
	messageField  string // field removed when embedOnly
	messageFormat string // fmt.Sprintf format from Error(), if any
	hasExtra      bool
	method        *ast.FuncDecl // Error() method
	setMsgMethod  *ast.FuncDecl
}

type errorsModernizer struct {
	*fileModernizer
	types        map[string]*customErrorType
	importsAdded map[string]bool
	cfg          Config
}

func (m *fileModernizer) modernizeStructuredErrors() (fmtErrorf, customErrors int) {
	em := &errorsModernizer{
		fileModernizer: m,
		types:          map[string]*customErrorType{},
		importsAdded:   map[string]bool{},
		cfg:            m.cfg,
	}
	if m.cfg.ErrorsBaseEmbed || m.cfg.ErrorsBaseSetMsg || m.cfg.ErrorsBaseUsages {
		em.collectCustomErrorTypes()
	}
	if len(em.types) > 0 {
		customErrors = em.rewriteCustomErrorTypes()
		if m.cfg.ErrorsBaseMessageFieldRefs && len(em.pkgEmbed) > 0 {
			customErrors += em.rewriteRemovedMessageFieldRefs()
		}
		if m.cfg.ErrorsBaseUsages {
			customErrors += em.rewriteCustomErrorUsages()
		}
	}
	if m.cfg.ErrorsBasePositionalComposites && len(em.pkgExtraFields) > 0 {
		customErrors += em.rewritePositionalErrorComposites()
	}
	if m.cfg.FmtErrorfToErrorsNew {
		fmtErrorf = em.rewriteFmtErrorfCalls()
	}
	em.pruneUnusedImport("fmt")
	return fmtErrorf, customErrors
}

func (em *errorsModernizer) collectCustomErrorTypes() {
	skipEmbed := em.fileHasSetMsgFactory()
	for _, decl := range em.file.Decls {
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
			if len(st.Fields.List) == 0 {
				continue // sentinel marker type, keep comparable
			}
			if em.hasBaseEmbed(st) {
				continue
			}
			errMethod := em.findErrorMethod(ts.Name.Name)
			if errMethod == nil {
				continue
			}
			info := &customErrorType{
				name:       ts.Name.Name,
				structType: st,
				method:     errMethod,
			}
			em.classifyCustomError(info)
			if skipEmbed && info.setMsgMethod != nil {
				continue
			}
			em.types[ts.Name.Name] = info
		}
	}
}

func (em *errorsModernizer) hasBaseEmbed(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if len(f.Names) > 0 {
			continue
		}
		if sel, ok := f.Type.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "errors" && sel.Sel.Name == "Base" {
				return true
			}
		}
	}
	return false
}

func (em *errorsModernizer) findErrorMethod(typeName string) *ast.FuncDecl {
	var found *ast.FuncDecl
	for _, decl := range em.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		recvType := fn.Recv.List[0].Type
		if !recvMatchesType(recvType, typeName) {
			continue
		}
		if fn.Name.Name != "Error" || !isErrorStringResult(fn.Type) {
			continue
		}
		found = fn
	}
	return found
}

func recvMatchesType(t ast.Expr, typeName string) bool {
	t = ast.Unparen(t)
	switch rt := t.(type) {
	case *ast.Ident:
		return rt.Name == typeName
	case *ast.StarExpr:
		if id, ok := rt.X.(*ast.Ident); ok {
			return id.Name == typeName
		}
	}
	return false
}

func isErrorStringResult(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	ident, ok := ft.Results.List[0].Type.(*ast.Ident)
	return ok && ident.Name == "string"
}

func (em *errorsModernizer) classifyCustomError(info *customErrorType) {
	info.setMsgMethod = em.findSetMsgMethod(info.name)

	fieldName, format, ok := parseErrorMethodBody(info.method)
	if !ok {
		info.hasExtra = true
		return
	}

	var msgFields []string
	var otherFields int
	for _, f := range info.structType.Fields.List {
		if len(f.Names) == 0 {
			otherFields++
			continue
		}
		for _, n := range f.Names {
			if isStringType(f.Type) && messageFieldNames[n.Name] {
				msgFields = append(msgFields, n.Name)
			} else {
				otherFields++
			}
		}
	}

	if otherFields == 0 && len(msgFields) == 1 && (fieldName == msgFields[0] || fieldName == "") {
		info.embedOnly = true
		info.messageField = msgFields[0]
		info.messageFormat = format
		return
	}
	info.hasExtra = true
}

func (em *errorsModernizer) findSetMsgMethod(typeName string) *ast.FuncDecl {
	for _, decl := range em.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "setMsg" {
			continue
		}
		if recvMatchesType(fn.Recv.List[0].Type, typeName) {
			return fn
		}
	}
	return nil
}

func parseErrorMethodBody(fn *ast.FuncDecl) (fieldName, format string, ok bool) {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return "", "", false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", "", false
	}
	expr := ast.Unparen(ret.Results[0])
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok && isReceiverName(id.Name, fn) {
			return e.Sel.Name, "%s", true
		}
	case *ast.Ident:
		if isReceiverName(e.Name, fn) {
			return "", "%s", true
		}
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "fmt" && sel.Sel.Name == "Sprintf" {
				if lit, ok := e.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					fmtStr, err := strconv.Unquote(lit.Value)
					if err != nil {
						return "", "", false
					}
					if len(e.Args) == 2 {
						if f, ok := fieldFromSelector(e.Args[1], fn); ok {
							return f, fmtStr, true
						}
					}
				}
			}
		}
	}
	return "", "", false
}

func isReceiverName(name string, fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return false
	}
	for _, n := range fn.Recv.List[0].Names {
		if n.Name == name {
			return true
		}
	}
	return name == "e" || name == "err"
}

func fieldFromSelector(e ast.Expr, fn *ast.FuncDecl) (string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if id, ok := sel.X.(*ast.Ident); ok && isReceiverName(id.Name, fn) {
		return sel.Sel.Name, true
	}
	return "", false
}

func isStringType(t ast.Expr) bool {
	ident, ok := t.(*ast.Ident)
	return ok && ident.Name == "string"
}

func (em *errorsModernizer) rewriteCustomErrorTypes() int {
	count := 0
	if em.cfg.ErrorsBaseEmbed {
		for _, info := range em.types {
			if info.embedOnly {
				count += em.rewriteEmbedOnlyType(info)
			} else if info.hasExtra {
				count += em.rewriteExtraFieldType(info)
			}
		}
	}
	if em.cfg.ErrorsBaseSetMsg {
		count += em.rewriteSetMsgFactories()
		count += em.rewriteErrorfWrappers()
	}
	return count
}

func (em *errorsModernizer) rewriteErrorfWrappers() int {
	count := 0
	for _, decl := range em.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 || fn.Body == nil {
			continue
		}
		if fn.Type.Params == nil || len(fn.Type.Params.List) < 2 {
			continue
		}
		retIdent, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
		if !ok {
			continue
		}
		info, ok := em.types[retIdent.Name]
		if !ok || !info.embedOnly {
			continue
		}
		formatName := "format"
		valsName := "vals"
		if len(fn.Type.Params.List[0].Names) > 0 {
			formatName = fn.Type.Params.List[0].Names[0].Name
		}
		if len(fn.Type.Params.List[1].Names) > 0 {
			valsName = fn.Type.Params.List[1].Names[0].Name
		}
		fn.Type.Results.List[0].Type = &ast.Ident{Name: "error"}
		fn.Body.List = []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{&ast.CallExpr{
			Fun: &ast.IndexExpr{
				X: &ast.SelectorExpr{
					X:   &ast.Ident{Name: "errors"},
					Sel: &ast.Ident{Name: "NewCustom"},
				},
				Index: &ast.Ident{Name: retIdent.Name},
			},
			Args: []ast.Expr{
				&ast.Ident{Name: formatName},
				&ast.Ident{Name: valsName},
			},
		}}}}
		em.mark()
		em.ensureImport("errors")
		count++
	}
	return count
}

func (em *errorsModernizer) fileHasSetMsgFactory() bool {
	for _, decl := range em.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && funcUsesSetMsgConstraint(fn) {
			return true
		}
	}
	return false
}

func (em *errorsModernizer) rewriteEmbedOnlyType(info *customErrorType) int {
	st := info.structType
	var newFields []*ast.Field
	for _, f := range st.Fields.List {
		if len(f.Names) == 1 && f.Names[0].Name == info.messageField && isStringType(f.Type) {
			continue
		}
		newFields = append(newFields, f)
	}
	baseField := &ast.Field{Type: &ast.SelectorExpr{
		X:   &ast.Ident{Name: "errors"},
		Sel: &ast.Ident{Name: "Base"},
	}}
	st.Fields.List = append([]*ast.Field{baseField}, newFields...)
	em.mark()
	em.ensureImport("errors")

	em.removeFuncDecl(info.method)
	if info.setMsgMethod != nil {
		em.removeFuncDecl(info.setMsgMethod)
	}
	return 1
}

func (em *errorsModernizer) rewriteExtraFieldType(info *customErrorType) int {
	st := info.structType
	baseField := &ast.Field{Type: &ast.SelectorExpr{
		X:   &ast.Ident{Name: "errors"},
		Sel: &ast.Ident{Name: "Base"},
	}}
	st.Fields.List = append([]*ast.Field{baseField}, st.Fields.List...)
	em.mark()
	em.ensureImport("errors")
	return 1
}

func (em *errorsModernizer) removeFuncDecl(target *ast.FuncDecl) {
	for i, decl := range em.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn == target {
			em.file.Decls = append(em.file.Decls[:i], em.file.Decls[i + 1:]...)
			em.mark()
			return
		}
	}
}

func (em *errorsModernizer) rewriteSetMsgFactories() int {
	count := 0
	ast.Inspect(em.file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		if !funcUsesSetMsgConstraint(fn) {
			return true
		}
		if em.rewriteSetMsgFactory(fn) {
			count++
		}
		return true
	})
	return count
}

func funcUsesSetMsgConstraint(fn *ast.FuncDecl) bool {
	if fn.Type == nil {
		return false
	}
	for _, fields := range []*ast.FieldList{fn.Type.TypeParams, fn.Type.Params} {
		if fields == nil {
			continue
		}
		for _, f := range fields.List {
			if interfaceHasSetMsg(f.Type) {
				return true
			}
		}
	}
	return false
}

func interfaceHasSetMsg(t ast.Expr) bool {
	iface, ok := t.(*ast.InterfaceType)
	if !ok || iface.Methods == nil {
		return false
	}
	for _, m := range iface.Methods.List {
		if ft, ok := m.Type.(*ast.FuncType); ok && len(ft.Params.List) == 1 {
			if id, ok := ft.Params.List[0].Type.(*ast.Ident); ok && id.Name == "string" {
				if m.Names != nil && m.Names[0].Name == "setMsg" {
					return true
				}
			}
		}
	}
	return false
}

func (em *errorsModernizer) rewriteSetMsgFactory(fn *ast.FuncDecl) bool {
	if fn.Body == nil || !funcUsesSetMsgConstraint(fn) {
		return false
	}
	typeParam := firstTypeParamName(fn)
	if typeParam == "" {
		return false
	}
	ptName := secondTypeParamName(fn)
	if ptName == "" {
		ptName = "PT"
	}
	formatName := "format"
	valsName := "vals"
	if fn.Type.Params != nil && len(fn.Type.Params.List) >= 2 {
		if len(fn.Type.Params.List[0].Names) > 0 {
			formatName = fn.Type.Params.List[0].Names[0].Name
		}
		if len(fn.Type.Params.List[1].Names) > 0 {
			valsName = fn.Type.Params.List[1].Names[0].Name
		}
	}
	fn.Body.List = []ast.Stmt{
		&ast.AssignStmt{
			Tok: token.DEFINE,
			Lhs: []ast.Expr{&ast.Ident{Name: "pt"}},
			Rhs: []ast.Expr{&ast.CallExpr{
				Fun: &ast.Ident{Name: ptName},
				Args: []ast.Expr{&ast.CallExpr{
					Fun:  &ast.Ident{Name: "new"},
					Args: []ast.Expr{&ast.Ident{Name: typeParam}},
				}},
			}},
		},
		&ast.ExprStmt{X: &ast.CallExpr{
			Fun: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "errors"},
				Sel: &ast.Ident{Name: "InitCustom"},
			},
			Args: []ast.Expr{
				&ast.UnaryExpr{
					Op: token.AND,
					X: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "pt"},
						Sel: &ast.Ident{Name: "Base"},
					},
				},
				&ast.Ident{Name: formatName},
				&ast.Ident{Name: valsName},
			},
		}},
		&ast.ReturnStmt{Results: []ast.Expr{&ast.UnaryExpr{Op: token.MUL, X: &ast.Ident{Name: "pt"}}}},
	}
	em.mark()
	em.ensureImport("errors")
	return true
}

func secondTypeParamName(fn *ast.FuncDecl) string {
	if fn.Type.TypeParams == nil || len(fn.Type.TypeParams.List) < 2 {
		return ""
	}
	if len(fn.Type.TypeParams.List[1].Names) > 0 {
		return fn.Type.TypeParams.List[1].Names[0].Name
	}
	return ""
}

func firstTypeParamName(fn *ast.FuncDecl) string {
	if fn.Type.TypeParams == nil || len(fn.Type.TypeParams.List) == 0 {
		return ""
	}
	if len(fn.Type.TypeParams.List[0].Names) > 0 {
		return fn.Type.TypeParams.List[0].Names[0].Name
	}
	return ""
}

func collectPackageEmbedOnlyTypes(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		em := &errorsModernizer{
			fileModernizer: &fileModernizer{file: f},
			types:          map[string]*customErrorType{},
		}
		em.collectCustomErrorTypes()
		for name, info := range em.types {
			if info.embedOnly {
				out[name] = info.messageField
			}
		}
	}
	return out
}

func collectPackageHasExtraErrorTypes(files []*ast.File) map[string][]string {
	out := map[string][]string{}
	for _, f := range files {
		em := &errorsModernizer{
			fileModernizer: &fileModernizer{file: f},
			types:          map[string]*customErrorType{},
		}
		em.collectCustomErrorTypes()
		for name, info := range em.types {
			if !info.hasExtra {
				continue
			}
			var fields []string
			for _, field := range info.structType.Fields.List {
				if len(field.Names) == 0 {
					continue
				}
				for _, n := range field.Names {
					fields = append(fields, n.Name)
				}
			}
			if len(fields) > 0 {
				out[name] = fields
			}
		}
	}
	return out
}

func compositeTypeName(t ast.Expr) (string, bool) {
	switch typ := ast.Unparen(t).(type) {
	case *ast.Ident:
		return typ.Name, true
	case *ast.SelectorExpr:
		return typ.Sel.Name, true
	}
	return "", false
}

func (em *errorsModernizer) rewritePositionalErrorComposites() int {
	count := 0
	ast.Inspect(em.file, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if em.keyPositionalComposite(cl) {
			count++
		}
		return true
	})
	return count
}

func (em *errorsModernizer) keyPositionalComposite(cl *ast.CompositeLit) bool {
	if len(cl.Elts) == 0 {
		return false
	}
	for _, elt := range cl.Elts {
		if _, ok := elt.(*ast.KeyValueExpr); ok {
			return false
		}
	}
	typeName, ok := compositeTypeName(cl.Type)
	if !ok {
		return false
	}
	fields, ok := em.pkgExtraFields[typeName]
	if !ok || len(fields) == 0 {
		return false
	}
	if len(cl.Elts) > len(fields) {
		return false
	}
	newElts := make([]ast.Expr, len(cl.Elts))
	for i, elt := range cl.Elts {
		newElts[i] = &ast.KeyValueExpr{
			Key:   &ast.Ident{Name: fields[i]},
			Value: elt,
		}
	}
	cl.Elts = newElts
	em.mark()
	return true
}

func (em *errorsModernizer) importLocalName(path string) string {
	for _, imp := range em.file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return pathBaseName(path)
	}
	return ""
}

func pathBaseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i + 1:]
	}
	return path
}

func (em *errorsModernizer) pruneUnusedImport(path string) bool {
	local := em.importLocalName(path)
	if local == "" {
		return false
	}
	used := false
	ast.Inspect(em.file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if ok && id.Name == local {
			used = true
			return false
		}
		return true
	})
	if used {
		return false
	}
	return em.removeImport(path)
}

func (em *errorsModernizer) removeImport(path string) bool {
	for di, decl := range em.file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		for si, spec := range gen.Specs {
			imp, ok := spec.(*ast.ImportSpec)
			if !ok {
				continue
			}
			p, _ := strconv.Unquote(imp.Path.Value)
			if p != path {
				continue
			}
			gen.Specs = append(gen.Specs[:si], gen.Specs[si + 1:]...)
			if len(gen.Specs) == 0 {
				em.file.Decls = append(em.file.Decls[:di], em.file.Decls[di + 1:]...)
			}
			em.mark()
			return true
		}
	}
	return false
}

func rewriteSelectorToBaseMessage(sel *ast.SelectorExpr) {
	oldX := sel.X
	sel.X = &ast.SelectorExpr{
		X:   oldX,
		Sel: &ast.Ident{Name: "Base"},
	}
	sel.Sel = &ast.Ident{Name: "Message"}
}

func (em *errorsModernizer) rewriteRemovedMessageFieldRefs() int {
	count := 0
	for typeName, msgField := range em.pkgEmbed {
		for _, decl := range em.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			locals := em.embedOnlyTypeIdents(fn, typeName)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					if node.Tok == token.DEFINE || node.Tok == token.ASSIGN {
						for i, rhs := range node.Rhs {
							if i >= len(node.Lhs) {
								break
							}
							id, ok := node.Lhs[i].(*ast.Ident)
							if !ok {
								continue
							}
							if em.exprIsEmbedOnlyType(rhs, typeName) {
								locals[id.Name] = true
							}
						}
					}
				case *ast.SelectorExpr:
					if node.Sel.Name != msgField {
						return true
					}
					id, ok := node.X.(*ast.Ident)
					if !ok || !locals[id.Name] {
						return true
					}
					rewriteSelectorToBaseMessage(node)
					em.mark()
					count++
				}
				return true
			})
		}
	}
	return count
}

func fieldTypeMatches(t ast.Expr, typeName string) bool {
	t = ast.Unparen(t)
	switch rt := t.(type) {
	case *ast.Ident:
		return rt.Name == typeName
	case *ast.StarExpr:
		return fieldTypeMatches(rt.X, typeName)
	}
	return false
}

func (em *errorsModernizer) embedOnlyTypeIdents(fn *ast.FuncDecl, typeName string) map[string]bool {
	out := map[string]bool{}
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recv := fn.Recv.List[0]
		if recvMatchesType(recv.Type, typeName) {
			for _, name := range recv.Names {
				out[name.Name] = true
			}
			if len(recv.Names) == 0 {
				out["_"] = true // unnamed receiver rare; skip
			}
		}
	}
	if fn.Type != nil && fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			if !fieldTypeMatches(field.Type, typeName) {
				continue
			}
			for _, name := range field.Names {
				out[name.Name] = true
			}
		}
	}
	return out
}

func (em *errorsModernizer) exprIsEmbedOnlyType(e ast.Expr, typeName string) bool {
	e = ast.Unparen(e)
	switch x := e.(type) {
	case *ast.CompositeLit:
		if id, ok := x.Type.(*ast.Ident); ok {
			return id.Name == typeName
		}
	case *ast.CallExpr:
		switch fun := x.Fun.(type) {
		case *ast.IndexExpr:
			return indexExprNamesType(fun, typeName, "NewCustom")
		case *ast.IndexListExpr:
			return indexExprNamesType(fun, typeName, "NewCustom")
		}
	}
	return false
}

func indexExprNamesType(fun ast.Expr, typeName, method string) bool {
	var idx ast.Expr
	switch fe := fun.(type) {
	case *ast.IndexExpr:
		idx = fe.Index
		fun = fe.X
	case *ast.IndexListExpr:
		if len(fe.Indices) == 0 {
			return false
		}
		idx = fe.Indices[0]
		fun = fe.X
	default:
		return false
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "errors" || sel.Sel.Name != method {
		return false
	}
	tid, ok := idx.(*ast.Ident)
	return ok && tid.Name == typeName
}

func (em *errorsModernizer) rewriteCustomErrorUsages() int {
	count := 0
	ast.Inspect(em.file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if c := em.rewriteAssignComposite(node); c > 0 {
				count += c
			}
		case *ast.ReturnStmt:
			if c := em.rewriteReturnCompositeLit(node); c > 0 {
				count += c
			}
		case *ast.GenDecl:
			if node.Tok == token.VAR {
				for _, spec := range node.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, v := range vs.Values {
						if newExpr, ok := em.embedOnlyCompositeToNewCustom(v); ok {
							vs.Values[i] = newExpr
							em.mark()
							count++
						}
					}
				}
			}
		}
		return true
	})
	count += em.rewriteConstructorReturns()
	return count
}

func (em *errorsModernizer) rewriteAssignComposite(assign *ast.AssignStmt) int {
	count := 0
	for i, rhs := range assign.Rhs {
		if newExpr, ok := em.embedOnlyCompositeToNewCustom(rhs); ok {
			assign.Rhs[i] = newExpr
			em.mark()
			count++
		}
	}
	return count
}

func (em *errorsModernizer) embedOnlyCompositeToNewCustom(e ast.Expr) (ast.Expr, bool) {
	switch x := ast.Unparen(e).(type) {
	case *ast.CompositeLit:
		typeName, info := em.compositeLitType(x)
		if info == nil || !info.embedOnly {
			return nil, false
		}
		newExpr := em.newCustomExpr(typeName, info, x)
		return newExpr, newExpr != nil
	case *ast.UnaryExpr:
		if x.Op != token.AND {
			return nil, false
		}
		cl, ok := x.X.(*ast.CompositeLit)
		if !ok {
			return nil, false
		}
		typeName, info := em.compositeLitType(cl)
		if info == nil || !info.embedOnly {
			return nil, false
		}
		newExpr := em.newCustomExpr(typeName, info, cl)
		if newExpr == nil {
			return nil, false
		}
		return newExpr, true
	default:
		return nil, false
	}
}

func (em *errorsModernizer) rewriteReturnCompositeLit(ret *ast.ReturnStmt) int {
	count := 0
	for i, r := range ret.Results {
		if newExpr, ok := em.embedOnlyCompositeToNewCustom(r); ok {
			ret.Results[i] = newExpr
			em.mark()
			count++
		}
	}
	return count
}

func (em *errorsModernizer) compositeLitType(cl *ast.CompositeLit) (string, *customErrorType) {
	switch t := cl.Type.(type) {
	case *ast.Ident:
		info := em.types[t.Name]
		return t.Name, info
	case *ast.SelectorExpr:
		return "", nil
	}
	return "", nil
}

func (em *errorsModernizer) newCustomExpr(typeName string, info *customErrorType, cl *ast.CompositeLit) ast.Expr {
	msgExpr := em.messageExprFromComposite(cl, info)
	if msgExpr == nil {
		return nil
	}
	return em.newCustomFromValue(typeName, info, msgExpr)
}

func (em *errorsModernizer) messageExprFromComposite(cl *ast.CompositeLit, info *customErrorType) ast.Expr {
	for _, elt := range cl.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == info.messageField {
				return kv.Value
			}
		}
	}
	if len(cl.Elts) == 1 {
		if info.messageField != "" {
			return nil
		}
		return cl.Elts[0]
	}
	return nil
}

func (em *errorsModernizer) newCustomFromValue(typeName string, info *customErrorType, value ast.Expr) ast.Expr {
	em.ensureImport("errors")
	args := []ast.Expr{value}
	if info.messageFormat != "" && info.messageFormat != "%s" {
		formatLit := &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(info.messageFormat)}
		args = []ast.Expr{formatLit, value}
	}
	return &ast.CallExpr{
		Fun: &ast.IndexExpr{
			X: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "errors"},
				Sel: &ast.Ident{Name: "NewCustom"},
			},
			Index: &ast.Ident{Name: typeName},
		},
		Args: args,
	}
}

func (em *errorsModernizer) rewriteConstructorReturns() int {
	count := 0
	for _, decl := range em.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !isConstructorFunc(fn) {
			continue
		}
		var newList []ast.Stmt
		changed := false
		for _, stmt := range fn.Body.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				newList = append(newList, stmt)
				continue
			}
			if wrapped, ok := em.wrapConstructorReturn(ret); ok {
				newList = append(newList, wrapped...)
				changed = true
				count++
				continue
			}
			newList = append(newList, stmt)
		}
		if changed {
			fn.Body.List = newList
			em.mark()
		}
	}
	return count
}

func (em *errorsModernizer) wrapConstructorReturn(ret *ast.ReturnStmt) ([]ast.Stmt, bool) {
	var cl *ast.CompositeLit
	ptr := false
	switch r := ret.Results[0].(type) {
	case *ast.CompositeLit:
		cl = r
	case *ast.UnaryExpr:
		if r.Op == token.AND {
			if lit, ok := r.X.(*ast.CompositeLit); ok {
				cl = lit
				ptr = true
			}
		}
	}
	if cl == nil {
		return nil, false
	}
	_, info := em.compositeLitType(cl)
	if info == nil || !info.hasExtra {
		return nil, false
	}
	tmpName := "e"
	var rhs ast.Expr = astutilCloneComposite(cl)
	if ptr {
		rhs = &ast.UnaryExpr{Op: token.AND, X: rhs}
	}
	init := &ast.AssignStmt{
		Tok: token.DEFINE,
		Lhs: []ast.Expr{&ast.Ident{Name: tmpName}},
		Rhs: []ast.Expr{rhs},
	}
	baseSel := &ast.SelectorExpr{X: &ast.Ident{Name: tmpName}, Sel: &ast.Ident{Name: "Base"}}
	initCustom := &ast.ExprStmt{X: &ast.CallExpr{
		Fun: &ast.SelectorExpr{X: &ast.Ident{Name: "errors"}, Sel: &ast.Ident{Name: "InitCustom"}},
		Args: []ast.Expr{
			&ast.UnaryExpr{Op: token.AND, X: baseSel},
			&ast.BasicLit{Kind: token.STRING, Value: `"%s"`},
			&ast.CallExpr{
				Fun: &ast.SelectorExpr{X: &ast.Ident{Name: tmpName}, Sel: &ast.Ident{Name: "Error"}},
			},
		},
	}}
	retStmt := &ast.ReturnStmt{Results: []ast.Expr{&ast.Ident{Name: tmpName}}}
	em.ensureImport("errors")
	return []ast.Stmt{init, initCustom, retStmt}, true
}

func isConstructorFunc(fn *ast.FuncDecl) bool {
	if fn.Name == nil {
		return false
	}
	name := fn.Name.Name
	return strings.HasPrefix(name, "new") || strings.HasPrefix(name, "New")
}

func astutilCloneComposite(cl *ast.CompositeLit) *ast.CompositeLit {
	elts := make([]ast.Expr, len(cl.Elts))
	for i, e := range cl.Elts {
		elts[i] = astutilCloneExpr(e)
	}
	return &ast.CompositeLit{Type: cl.Type, Elts: elts}
}

func astutilCloneExpr(e ast.Expr) ast.Expr {
	switch x := e.(type) {
	case *ast.KeyValueExpr:
		return &ast.KeyValueExpr{Key: x.Key, Value: astutilCloneExpr(x.Value)}
	case *ast.Ident:
		return &ast.Ident{Name: x.Name}
	case *ast.BasicLit:
		return &ast.BasicLit{Kind: x.Kind, Value: x.Value}
	case *ast.SelectorExpr:
		return &ast.SelectorExpr{X: astutilCloneExpr(x.X), Sel: x.Sel}
	case *ast.CallExpr:
		args := make([]ast.Expr, len(x.Args))
		for i, a := range x.Args {
			args[i] = astutilCloneExpr(a)
		}
		return &ast.CallExpr{Fun: astutilCloneExpr(x.Fun), Args: args}
	default:
		return x
	}
}

func (em *errorsModernizer) rewriteFmtErrorfCalls() int {
	if !em.hasStdErrorsImport() && em.usesNonStdErrors() {
		return 0
	}
	count := 0
	ast.Inspect(em.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isFmtErrorf(call) {
			return true
		}
		if !em.canReplaceFmtErrorf(call) {
			return true
		}
		em.replaceFmtErrorfWithNew(call)
		count++
		return true
	})
	return count
}

func (em *errorsModernizer) hasStdErrorsImport() bool {
	for _, imp := range em.file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path == "errors" && imp.Name == nil {
			return true
		}
	}
	return false
}

func (em *errorsModernizer) usesNonStdErrors() bool {
	for _, imp := range em.file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if strings.HasSuffix(path, "/errors") && path != "errors" {
			return true
		}
	}
	return false
}

func isFmtErrorf(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "fmt" && sel.Sel.Name == "Errorf"
}

func (em *errorsModernizer) canReplaceFmtErrorf(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	format, ok := call.Args[0].(*ast.BasicLit)
	if !ok || format.Kind != token.STRING {
		return false
	}
	fmtStr, err := strconv.Unquote(format.Value)
	if err != nil {
		return false
	}
	return !strings.Contains(fmtStr, "%w")
}

func (em *errorsModernizer) replaceFmtErrorfWithNew(call *ast.CallExpr) {
	lit, ok := formatCallArgsToInterpLit(em.fset, call.Args)
	if !ok {
		return
	}
	call.Fun = &ast.SelectorExpr{
		X:   &ast.Ident{Name: "errors"},
		Sel: &ast.Ident{Name: "New"},
	}
	call.Args = []ast.Expr{lit}
	em.ensureImport("errors")
	em.mark()
}

func (em *errorsModernizer) ensureImport(path string) {
	if em.importsAdded[path] {
		return
	}
	for _, imp := range em.file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p == path {
			em.importsAdded[path] = true
			return
		}
	}
	spec := &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: `"` + path + `"`}}
	for _, decl := range em.file.Decls {
		if g, ok := decl.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			g.Specs = append(g.Specs, spec)
			em.importsAdded[path] = true
			return
		}
	}
	em.file.Decls = append([]ast.Decl{&ast.GenDecl{
		Tok:   token.IMPORT,
		Specs: []ast.Spec{spec},
	}}, em.file.Decls...)
	em.importsAdded[path] = true
}
