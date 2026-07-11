package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

type interpSegment struct {
	literal string
	expr    ast.Expr
	format  string // without leading %
}

func modernizeInterpolatedStrings(fset *token.FileSet, f *ast.File, src []byte) (edits []sourceEdit, count int) {
	// Order: Sprintf → concat → escape literal braces in remaining strings.
	edits, n := rewriteSprintfToInterp(fset, f, src, edits)
	count += n
	edits, n = rewriteConcatToInterp(fset, f, src, edits)
	count += n
	edits, n = escapeLiteralBraces(fset, f, src, edits)
	count += n
	return edits, count
}

func rewriteSprintfToInterp(fset *token.FileSet, f *ast.File, src []byte, edits []sourceEdit) ([]sourceEdit, int) {
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
		if !ok || len(verbs) != len(call.Args)-1 {
			return true
		}
		var segs []interpSegment
		argIdx := 0
		for _, part := range stat {
			if part.verb {
				if argIdx >= len(call.Args)-1 {
					return true
				}
				segs = append(segs, interpSegment{
					expr:   call.Args[argIdx+1],
					format: printfVerbToInterp(verbs[argIdx]),
				})
				argIdx++
				continue
			}
			segs = append(segs, interpSegment{literal: part.text})
		}
		if argIdx != len(call.Args)-1 {
			return true
		}
		text, ok := renderInterpolatedString(fset, segs)
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

func rewriteConcatToInterp(fset *token.FileSet, f *ast.File, src []byte, edits []sourceEdit) ([]sourceEdit, int) {
	var adds []*ast.BinaryExpr
	ast.Inspect(f, func(n ast.Node) bool {
		if be, ok := n.(*ast.BinaryExpr); ok && be.Op == token.ADD {
			adds = append(adds, be)
		}
		return true
	})
	count := 0
	for _, be := range adds {
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
		escaped, changed := escapeNonInterpBraces(lit.Value)
		if !changed {
			return true
		}
		start := fset.Position(lit.Pos()).Offset
		end := fset.Position(lit.End()).Offset
		edits = append(edits, sourceEdit{start: start, end: end, text: []byte(escaped)})
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
	return len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"'
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
		if i+1 < len(format) && format[i+1] == '%' {
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
		b.WriteByte('{')
		b.WriteString(exprBuf.String())
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

func escapeNonInterpBraces(quoted string) (string, bool) {
	if !isDoubleQuoted(quoted) {
		return quoted, false
	}
	body := quoted[1 : len(quoted)-1]
	var out strings.Builder
	out.WriteByte('"')
	changed := false
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if ch == '\\' && i+1 < len(body) {
			out.WriteByte('\\')
			out.WriteByte(body[i+1])
			i++
			continue
		}
		if ch == '{' {
			if end, isHole := scanInterpHole(body, i); isHole {
				out.WriteString(body[i : end+1])
				i = end
				continue
			}
			out.WriteString(`\{`)
			changed = true
			continue
		}
		if ch == '}' {
			out.WriteString(`\}`)
			changed = true
			continue
		}
		out.WriteByte(ch)
	}
	out.WriteByte('"')
	return out.String(), changed
}

func scanInterpHole(body string, start int) (end int, ok bool) {
	if start >= len(body) || body[start] != '{' {
		return 0, false
	}
	close := strings.IndexByte(body[start+1:], '}')
	if close < 0 {
		return 0, false
	}
	end = start + 1 + close
	inside := body[start+1 : end]
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
	return strings.TrimSpace(inside[:colon]), strings.TrimSpace(inside[colon+1:])
}
