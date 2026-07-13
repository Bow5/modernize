package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

type interpSegment struct {
	literal string
	expr    ast.Expr
	format  string // without leading %
}

func modernizeInterpolatedStrings(fset *token.FileSet, f *ast.File, src []byte, typesInfo *types.Info) (edits []sourceEdit, count int) {
	// Escape literal braces in existing strings before building new interpolated
	// strings; otherwise mux-style holes like {key:.*} get misclassified.
	edits, n := escapeLiteralBraces(fset, f, src, edits)
	count += n
	// Order: Sprintf → *f funcs → errors.New → concat.
	byteSliceParents := byteSliceParentsOfSprintf(f)
	edits, n = rewriteSprintfToInterp(fset, f, src, edits, byteSliceParents)
	count += n
	edits, n = rewriteFormatFuncsToNonF(fset, f, src, edits, byteSliceParents)
	count += n
	edits, n = rewriteErrorsNewToInterp(fset, f, src, edits)
	count += n
	edits, n = rewriteConcatToInterp(fset, f, src, edits, typesInfo)
	count += n
	return edits, count
}

func rewriteSprintfToInterp(fset *token.FileSet, f *ast.File, src []byte, edits []sourceEdit, byteSliceParents map[*ast.CallExpr]*ast.CallExpr) ([]sourceEdit, int) {
	skip := byteSliceStringLits(f)
	count := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isFmtIdent(sel.X) || sel.Sel.Name != "Sprintf" {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && skip[lit] {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !isDoubleQuoted(lit.Value) {
			return true
		}
		format, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		verbs, stat, ok := parsePrintfFormat(format)
		if !ok || len(verbs) != len(call.Args) - 1 {
			return true
		}
		var segs []interpSegment
		argIdx := 0
		for _, part := range stat {
			if part.verb {
				if argIdx >= len(call.Args) - 1 {
					return true
				}
				segs = append(segs, interpSegment{
					expr:   call.Args[argIdx + 1],
					format: printfVerbToInterp(verbs[argIdx]),
				})
				argIdx++
				continue
			}
			segs = append(segs, interpSegment{literal: part.text})
		}
		if argIdx != len(call.Args) - 1 {
			return true
		}
		if !interpolatedSegmentsSafe(fset, segs) {
			return true
		}
		text, ok := renderInterpolatedString(fset, segs)
		if !ok {
			return true
		}
		rewrite := call
		if outer, ok := byteSliceParents[call]; ok {
			text = "[]byte(" + text + ")"
			rewrite = outer
		}
		start := fset.Position(rewrite.Pos()).Offset
		end := fset.Position(rewrite.End()).Offset
		edits = append(edits, sourceEdit{start: start, end: end, text: []byte(text)})
		count++
		return true
	})
	return edits, count
}

func rewriteFormatFuncsToNonF(fset *token.FileSet, f *ast.File, src []byte, edits []sourceEdit, byteSliceParents map[*ast.CallExpr]*ast.CallExpr) ([]sourceEdit, int) {
	skip := byteSliceStringLits(f)
	count := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		rw, ok := matchFormatFuncRewrite(sel)
		if !ok {
			return true
		}
		if rw.method == "Errorf" && isFmtErrorf(call) {
			return true // structured_errors handles fmt.Errorf → errors.New
		}
		if rw.formatIndex >= len(call.Args) {
			return true
		}
		formatArg := call.Args[rw.formatIndex]
		if lit, ok := formatArg.(*ast.BasicLit); ok && skip[lit] {
			return true
		}
		valueArgs := call.Args[rw.formatIndex + 1:]
		interp, ok := formatCallArgsToInterpString(fset, formatArg, valueArgs)
		if !ok {
			return true
		}
		var text string
		if rw.method == "Errorf" && isConfigIdent(sel.X) {
			text, ok = renderConfigErrorCall(fset, sel.X, interp)
		} else {
			text, ok = renderNonFCall(fset, sel.X, rw.nonF, rw.formatIndex, call.Args[:rw.formatIndex], interp)
		}
		if !ok {
			return true
		}
		rewrite := call
		if outer, ok := byteSliceParents[call]; ok {
			text = "[]byte(" + text + ")"
			rewrite = outer
		}
		start := fset.Position(rewrite.Pos()).Offset
		end := fset.Position(rewrite.End()).Offset
		edits = append(edits, sourceEdit{start: start, end: end, text: []byte(text)})
		count++
		return true
	})
	return edits, count
}

func isConfigIdent(x ast.Expr) bool {
	id, ok := ast.Unparen(x).(*ast.Ident)
	return ok && id.Name == "config"
}

// renderConfigErrorCall rewrites config.Errorf(...) to config.Error[ErrConfigGeneric](...).
// The type parameter is required because config.Error is generic and cannot be inferred
// from an interpolated string argument alone.
func renderConfigErrorCall(fset *token.FileSet, recv ast.Expr, interp string) (string, bool) {
	var b strings.Builder
	if err := format.Node(&b, fset, recv); err != nil {
		return "", false
	}
	b.WriteString(".Error[ErrConfigGeneric](")
	b.WriteString(interp)
	b.WriteByte(')')
	return b.String(), true
}

func renderNonFCall(fset *token.FileSet, recv ast.Expr, nonF string, formatIndex int, prefixArgs []ast.Expr, interp string) (string, bool) {
	var b strings.Builder
	if err := format.Node(&b, fset, recv); err != nil {
		return "", false
	}
	b.WriteByte('.')
	b.WriteString(nonF)
	b.WriteByte('(')
	for i, arg := range prefixArgs {
		if i > 0 {
			b.WriteString(", ")
		}
		var argBuf bytes.Buffer
		if err := format.Node(&argBuf, fset, arg); err != nil {
			return "", false
		}
		b.WriteString(argBuf.String())
	}
	if len(prefixArgs) > 0 {
		b.WriteString(", ")
	}
	b.WriteString(interp)
	b.WriteByte(')')
	return b.String(), true
}

func rewriteErrorsNewToInterp(fset *token.FileSet, f *ast.File, src []byte, edits []sourceEdit) ([]sourceEdit, int) {
	skip := byteSliceStringLits(f)
	count := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 || !isErrorsNew(call) {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && skip[lit] {
			return true
		}
		interp, ok := formatCallArgsToInterpString(fset, call.Args[0], call.Args[1:])
		if !ok {
			return true
		}
		text, ok := renderNonFCall(fset, &ast.Ident{Name: "errors"}, "New", 0, nil, interp)
		if !ok {
			return true
		}
		start := fset.Position(call.Pos()).Offset
		end := fset.Position(call.End()).Offset
		edits = append(edits, sourceEdit{start: start, end: end, text: []byte(text)})
		count++
		return true
	})
	return edits, count
}

func rewriteConcatToInterp(fset *token.FileSet, f *ast.File, src []byte, edits []sourceEdit, typesInfo *types.Info) ([]sourceEdit, int) {
	var adds []*ast.BinaryExpr
	ast.Inspect(f, func(n ast.Node) bool {
		if be, ok := n.(*ast.BinaryExpr); ok && be.Op == token.ADD {
			adds = append(adds, be)
		}
		return true
	})
	count := 0
	constExprs := constInitExprSet(f)
	for _, be := range adds {
		if constExprs[be] {
			continue
		}
		if isInteriorAddChain(be, adds) {
			continue
		}
		segs, ok := collectConcatSegments(be)
		if !ok || len(segs) < 2 {
			continue
		}
		hasNonLiteral := false
		for _, s := range segs {
			if s.expr != nil {
				hasNonLiteral = true
				break
			}
		}
		if !hasNonLiteral {
			continue
		}
		if !concatIncludesStringLiteral(segs) {
			continue
		}
		if typesInfo != nil {
			if t := typesInfo.TypeOf(be); t == nil || !isTypesString(t) {
				continue
			}
		}
		if concatExprHasStringLiteral(segs) {
			continue
		}
		if concatExprHasSlice(segs) {
			continue
		}
		if !interpolatedSegmentsSafe(fset, segs) {
			continue
		}
		text, ok := renderInterpolatedString(fset, segs)
		if !ok {
			continue
		}
		start := fset.Position(be.Pos()).Offset
		end := fset.Position(be.End()).Offset
		edits = append(edits, sourceEdit{start: start, end: end, text: []byte(text)})
		count++
	}
	return edits, count
}

func isInteriorAddChain(be *ast.BinaryExpr, adds []*ast.BinaryExpr) bool {
	for _, other := range adds {
		if x, ok := other.X.(*ast.BinaryExpr); ok && x == be {
			return true
		}
	}
	return false
}

func concatIncludesStringLiteral(segs []interpSegment) bool {
	for _, s := range segs {
		if s.literal != "" {
			return true
		}
	}
	return false
}

func concatExprHasStringLiteral(segs []interpSegment) bool {
	for _, s := range segs {
		if exprHasStringLiteral(s.expr) {
			return true
		}
	}
	return false
}

func exprHasStringLiteral(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			found = true
			return false
		}
		return true
	})
	return found
}

func concatExprHasSlice(segs []interpSegment) bool {
	for _, s := range segs {
		if exprHasSlice(s.expr) {
			return true
		}
	}
	return false
}

func exprHasSlice(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.IndexExpr, *ast.SliceExpr:
			found = true
			return false
		}
		return true
	})
	return found
}

func isByteSliceCast(call *ast.CallExpr) bool {
	arr, ok := call.Fun.(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return false
	}
	id, ok := arr.Elt.(*ast.Ident)
	return ok && id.Name == "byte"
}

func byteSliceStringLits(f *ast.File) map[*ast.BasicLit]bool {
	skip := map[*ast.BasicLit]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isByteSliceCast(call) {
			return true
		}
		for _, arg := range call.Args {
			if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				skip[lit] = true
			}
		}
		return true
	})
	return skip
}

func byteSliceParentsOfSprintf(f *ast.File) map[*ast.CallExpr]*ast.CallExpr {
	parents := map[*ast.CallExpr]*ast.CallExpr{}
	ast.Inspect(f, func(n ast.Node) bool {
		outer, ok := n.(*ast.CallExpr)
		if !ok || !isByteSliceCast(outer) || len(outer.Args) != 1 {
			return true
		}
		inner, ok := outer.Args[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := inner.Fun.(*ast.SelectorExpr); ok && isFmtIdent(sel.X) {
			switch sel.Sel.Name {
			case "Sprintf", "Printf", "Fprintf":
				parents[inner] = outer
			}
		}
		return true
	})
	return parents
}

func constInitExprSet(f *ast.File) map[*ast.BinaryExpr]bool {
	set := map[*ast.BinaryExpr]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, val := range vs.Values {
				ast.Inspect(val, func(n ast.Node) bool {
					if be, ok := n.(*ast.BinaryExpr); ok && be.Op == token.ADD {
						set[be] = true
					}
					return true
				})
			}
		}
	}
	return set
}

func collectConcatSegments(expr ast.Expr) ([]interpSegment, bool) {
	be, ok := expr.(*ast.BinaryExpr)
	if !ok || be.Op != token.ADD {
		if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING && isDoubleQuoted(lit.Value) {
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return nil, false
			}
			return []interpSegment{{literal: s}}, true
		}
		return []interpSegment{{expr: expr}}, true
	}
	left, ok := collectConcatSegments(be.X)
	if !ok {
		return nil, false
	}
	right, ok := collectConcatSegments(be.Y)
	if !ok {
		return nil, false
	}
	return append(left, right...), true
}

func escapeLiteralBraces(fset *token.FileSet, f *ast.File, src []byte, edits []sourceEdit) ([]sourceEdit, int) {
	skip := byteSliceStringLits(f)
	count := 0
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !isDoubleQuoted(lit.Value) || skip[lit] {
			return true
		}
		rewritten, changed := rewriteLiteralBraces(lit.Value)
		if !changed {
			return true
		}
		start := fset.Position(lit.Pos()).Offset
		end := fset.Position(lit.End()).Offset
		edits = append(edits, sourceEdit{start: start, end: end, text: []byte(rewritten)})
		count++
		return true
	})
	return edits, count
}

func isFmtIdent(x ast.Expr) bool {
	id, ok := ast.Unparen(x).(*ast.Ident)
	return ok && id.Name == "fmt"
}

func isDoubleQuoted(val string) bool {
	return len(val) >= 2 && val[0] == '"' && val[len(val) - 1] == '"'
}

type printfPart struct {
	text string
	verb bool
}

func parsePrintfFormat(format string) (verbs []string, parts []printfPart, ok bool) {
	i := 0
	for i < len(format) {
		if format[i] != '%' {
			j := i
			for j < len(format) && format[j] != '%' {
				j++
			}
			parts = append(parts, printfPart{text: format[i:j]})
			i = j
			continue
		}
		if i + 1 < len(format) && format[i + 1] == '%' {
			parts = append(parts, printfPart{text: "%"})
			i += 2
			continue
		}
		verb, n, ok := scanPrintfVerb(format[i:])
		if !ok {
			return nil, nil, false
		}
		verbs = append(verbs, verb)
		parts = append(parts, printfPart{verb: true})
		i += n
	}
	return verbs, parts, true
}

func scanPrintfVerb(s string) (verb string, n int, ok bool) {
	if len(s) < 2 || s[0] != '%' {
		return "", 0, false
	}
	i := 1
	for i < len(s) && strings.ContainsRune("0123456789.*-+ #0", rune(s[i])) {
		i++
	}
	if i >= len(s) {
		return "", 0, false
	}
	i++ // verb letter
	return s[1:i], i, true
}

func printfVerbToInterp(verb string) string {
	v := strings.TrimSpace(verb)
	if v == "" || v == "v" || v == "s" {
		return ""
	}
	return v
}

func interpolatedSegmentsSafe(fset *token.FileSet, segs []interpSegment) bool {
	for _, seg := range segs {
		if seg.expr == nil {
			continue
		}
		var exprBuf bytes.Buffer
		if err := format.Node(&exprBuf, fset, seg.expr); err != nil {
			return false
		}
		if strings.Contains(exprBuf.String(), `"`) {
			return false
		}
	}
	return true
}

func renderInterpolatedString(fset *token.FileSet, segs []interpSegment) (string, bool) {
	var b strings.Builder
	b.WriteByte('"')
	for _, seg := range segs {
		if seg.expr == nil {
			writeEscapedStringContent(&b, seg.literal)
			continue
		}
		var exprBuf bytes.Buffer
		if err := format.Node(&exprBuf, fset, seg.expr); err != nil {
			return "", false
		}
		exprSrc := exprBuf.String()
		b.WriteByte('{')
		b.WriteString(exprSrc)
		if seg.format != "" {
			b.WriteByte(':')
			b.WriteString(seg.format)
		}
		b.WriteByte('}')
	}
	b.WriteByte('"')
	return b.String(), true
}

func writeEscapedStringContent(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteByte(ch)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if ch < 0x20 {
				b.WriteString(fmt.Sprintf(`\x%02x`, ch))
			} else {
				b.WriteByte(ch)
			}
		}
	}
}

// rewriteLiteralBraces rewrites double-quoted strings that contain route/query
// templates (e.g. "{key:.*}") or other non-interpolation braces. Prefer raw
// backtick strings; fall back to \{ \} escapes when a raw string is unsafe.
func rewriteLiteralBraces(quoted string) (string, bool) {
	if !isDoubleQuoted(quoted) {
		return quoted, false
	}
	body, err := strconv.Unquote(quoted)
	if err != nil {
		return quoted, false
	}
	if !strings.ContainsAny(body, "{}") {
		return quoted, false
	}
	if stringHasRealInterpHoles(body) {
		escaped, changed := escapeNonInterpolationBraces(body)
		if !changed {
			return quoted, false
		}
		return doubleQuotedFromContent(escaped), true
	}
	if canUseRawString(body) {
		return "`" + body + "`", true
	}
	escaped, changed := escapeNonInterpolationBraces(body)
	if !changed {
		return quoted, false
	}
	return doubleQuotedFromContent(escaped), true
}

func canUseRawString(body string) bool {
	return !strings.Contains(body, "`")
}

func doubleQuotedFromContent(body string) string {
	return strconv.Quote(body)
}

// isMuxFormatSpec reports whether the portion after ':' in a brace hole is a
// gorilla/mux route/query pattern rather than printf-style interpolation.
func isMuxFormatSpec(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.Contains(s, ".+") || strings.Contains(s, ".*") {
		return true
	}
	if strings.ContainsAny(s, "[]") {
		return true
	}
	return false
}

func isMuxTemplateHole(inside string) bool {
	_, format := splitInterpInside(inside)
	return isMuxFormatSpec(format)
}

func isRealInterpHole(body string, start int) bool {
	end, ok := scanInterpHole(body, start)
	if !ok {
		return false
	}
	inside := body[start + 1 : end]
	exprPart, format := splitInterpInside(inside)
	if format == "" {
		// {name} without format is a gorilla/mux path/query param, not interpolation.
		return false
	}
	if isMuxTemplateHole(inside) {
		return false
	}
	if strings.TrimSpace(exprPart) == "" {
		return false
	}
	return true
}

func stringHasRealInterpHoles(body string) bool {
	for i := 0; i < len(body); i++ {
		if body[i] != '{' {
			continue
		}
		if i > 0 && body[i - 1] == '\\' {
			continue
		}
		if isRealInterpHole(body, i) {
			return true
		}
	}
	return false
}

// escapeNonInterpolationBraces escapes mux templates and other literal braces,
// leaving real interpolation holes unchanged.
func escapeNonInterpolationBraces(body string) (string, bool) {
	var out strings.Builder
	changed := false
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if ch == '\\' && i + 1 < len(body) {
			out.WriteByte('\\')
			out.WriteByte(body[i + 1])
			i++
			continue
		}
		if ch != '{' {
			out.WriteByte(ch)
			continue
		}
		end, ok := scanInterpHole(body, i)
		if !ok {
			out.WriteString(`\{`)
			changed = true
			continue
		}
		inside := body[i + 1 : end]
		if isMuxTemplateHole(inside) {
			out.WriteString(`\{`)
			out.WriteString(inside)
			out.WriteString(`\}`)
			changed = true
			i = end
			continue
		}
		if isRealInterpHole(body, i) {
			out.WriteString(body[i : end + 1])
			i = end
			continue
		}
		out.WriteString(`\{`)
		out.WriteString(inside)
		out.WriteString(`\}`)
		changed = true
		i = end
	}
	return out.String(), changed
}

func escapeNonInterpBraces(quoted string) (string, bool) {
	return rewriteLiteralBraces(quoted)
}

func scanInterpHole(body string, start int) (end int, ok bool) {
	if start >= len(body) || body[start] != '{' {
		return 0, false
	}
	close := strings.IndexByte(body[start + 1:], '}')
	if close < 0 {
		return 0, false
	}
	end = start + 1 + close
	inside := body[start + 1 : end]
	exprPart, _ := splitInterpInside(inside)
	if strings.TrimSpace(exprPart) == "" {
		return 0, false
	}
	test := fmt.Sprintf("package p; func _() { _ = %s }", exprPart)
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, "hole.go", test, 0)
	return end, err == nil
}

func splitInterpInside(inside string) (expr, format string) {
	colon := strings.IndexByte(inside, ':')
	if colon < 0 {
		return strings.TrimSpace(inside), ""
	}
	return strings.TrimSpace(inside[:colon]), strings.TrimSpace(inside[colon + 1:])
}

func isTypesString(t types.Type) bool {
	if t == nil {
		return false
	}
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Kind() == types.String
}
