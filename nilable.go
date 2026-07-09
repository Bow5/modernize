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
	fset      *token.FileSet
	files     []*ast.File
	nilable   map[ptrSiteKey]bool
	typeNode  map[ptrSiteKey]ast.Expr
	funcs     map[string][]*ast.FuncDecl
}

func newPtrAnnotator(fset *token.FileSet, files []*ast.File) *ptrAnnotator {
	return &ptrAnnotator{
		fset:     fset,
		files:    files,
		nilable:  make(map[ptrSiteKey]bool),
		typeNode: make(map[ptrSiteKey]ast.Expr),
		funcs:    make(map[string][]*ast.FuncDecl),
	}
}

func (a *ptrAnnotator) analyze() {
	a.collectSites()
	for _, f := range a.files {
		a.scanNilEvidence(f)
	}
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
				if len(vs.Values) == 0 {
					a.nilable[key] = true
				} else if len(vs.Values) == 1 && isNilExpr(vs.Values[0]) {
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
				if len(vs.Values) == 0 {
					a.nilable[key] = true
				} else if len(vs.Values) == 1 && isNilExpr(vs.Values[0]) {
					a.nilable[key] = true
				}
			}
		}
		return true
	})
}

func fieldHasOmitempty(field *ast.Field) bool {
	if field.Tag == nil {
		return false
	}
	return strings.Contains(field.Tag.Value, "omitempty")
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
	for i, lhs := range as.Lhs {
		if !isNilExpr(rhsAt(as.Rhs, i)) {
			continue
		}
		switch e := ast.Unparen(lhs).(type) {
		case *ast.Ident:
			a.markVarNilable(fn, e.Name)
		case *ast.SelectorExpr:
			if typeName, field, ok := selectorField(e); ok {
				key := ptrSiteKey{kind: "field", owner: typeName, name: field, index: -1}
				a.nilable[key] = true
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
	flat := flattenFields("result", fn.Name.Name, fn.Type.Results)
	for i, expr := range ret.Results {
		if !isNilExpr(expr) || i >= len(flat) {
			continue
		}
		if plainStarType(flat[i].typ) != nil {
			a.nilable[flat[i].key] = true
		}
	}
}

func (a *ptrAnnotator) scanCall(call *ast.CallExpr) {
	fnName, _, _ := callTarget(call.Fun)
	if fnName == "" {
		return
	}
	decls := a.funcs[fnName]
	for _, fn := range decls {
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
	typeName := compositeLitTypeName(lit)
	if typeName == "" {
		return
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok || !isNilExpr(kv.Value) {
			continue
		}
		switch key := kv.Key.(type) {
		case *ast.Ident:
			a.nilable[ptrSiteKey{kind: "field", owner: typeName, name: key.Name, index: -1}] = true
		case *ast.SelectorExpr:
			if _, field, ok := selectorField(key); ok {
				a.nilable[ptrSiteKey{kind: "field", owner: typeName, name: field, index: -1}] = true
			}
		}
	}
}

func compositeLitTypeName(lit *ast.CompositeLit) string {
	switch t := ast.Unparen(lit.Type).(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return ""
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
		if rewriteFilePointerTypes(fset, f, ann) {
			changed[i] = true
		}
	}
	return changed
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
