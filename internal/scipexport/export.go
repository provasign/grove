// Package scipexport writes Grove's graph as a SCIP index (MKT-10 step 1):
// one document per indexed file, a definition occurrence at each symbol's
// name, reference occurrences at resolved call, read and write positions and
// at type names inside the symbols that use a type, and implementation
// relationships for extends, implements and overrides edges. Encoded with
// protowire against scip.proto's field numbers, as internal/native reads it.
package scipexport

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/provasign/grove/internal/core"
)

// scip.proto field and enum numbers this writer emits.
const (
	indexMetadata  = 1
	indexDocuments = 2

	metaVersion      = 1
	metaToolInfo     = 2
	metaProjectRoot  = 3
	metaTextEncoding = 4
	toolName         = 1
	toolVersion      = 2
	textEncodingUTF8 = 1

	docPath             = 1
	docOccurrences      = 2
	docSymbols          = 3
	docLanguage         = 4
	docPositionEncoding = 6
	positionUTF8Bytes   = 1

	occRange          = 1
	occSymbol         = 2
	occRoles          = 3
	occEnclosingRange = 7
	roleDefinition    = 1
	roleWrite         = 4
	roleRead          = 8

	infoSymbol    = 1
	infoRelations = 4
	infoKind      = 5
	infoDisplay   = 6
	infoEnclosing = 8

	relSymbol         = 1
	relImplementation = 3
)

var scipKind = map[core.SymbolKind]uint64{
	core.KindClass: 7, core.KindConst: 8, core.KindConstructor: 9, core.KindEnum: 11,
	core.KindField: 15, core.KindFunction: 17, core.KindInterface: 21, core.KindMacro: 25,
	core.KindMethod: 26, core.KindModule: 29, core.KindNamespace: 30, core.KindStruct: 49,
	core.KindTrait: 53, core.KindType: 55, core.KindVariable: 61,
}

var scipLanguage = map[string]string{
	"go": "Go", "java": "Java", "kotlin": "Kotlin", "scala": "Scala", "typescript": "TypeScript",
	"tsx": "TSX", "javascript": "JavaScript", "python": "Python", "rust": "Rust", "csharp": "CSharp",
	"c": "C", "cpp": "CPP", "ruby": "Ruby", "php": "PHP", "swift": "Swift", "objc": "Objective_C",
}

// Options name the tool and the project in the index metadata.
type Options struct {
	Root        string // absolute project root; source is read from here
	ToolName    string // "prism" or "grove"
	ToolVersion string
}

type occurrence struct {
	line, col, endCol int // 0-based; one-line ranges
	symbol            string
	roles             uint64
	// enclosing is the declaration around a definition, 0-based
	// [startLine, startCol, endLine, endCol]; startLine < 0 when absent.
	enclosing [4]int
}

type document struct {
	path, language string
	occs           []occurrence
	infos          []symbolInfo
}

type symbolInfo struct {
	symbol, display, enclosing string
	kind                       uint64
	implements                 []string
}

// Export encodes symbols and edges as a SCIP Index.
func Export(symbols []core.SymbolRecord, edges []core.Edge, opt Options) ([]byte, error) {
	byID := make(map[string]*core.SymbolRecord, len(symbols))
	for i := range symbols {
		byID[symbols[i].ID] = &symbols[i]
	}
	names := assignNames(symbols) // grove ID -> SCIP symbol
	src := newSources(opt.Root)
	docs := map[string]*document{}
	doc := func(s *core.SymbolRecord) *document {
		d := docs[s.FilePath]
		if d == nil {
			d = &document{path: s.FilePath, language: scipLanguage[s.Language]}
			docs[s.FilePath] = d
		}
		return d
	}
	implements := map[string][]string{}
	for _, e := range edges {
		switch e.Type {
		case core.EdgeExtends, core.EdgeImplements, core.EdgeOverrides:
			if to := names[e.To]; to != "" && names[e.From] != "" {
				implements[e.From] = append(implements[e.From], to)
			}
		}
	}
	infoDone := map[string]bool{}
	for i := range symbols {
		s := &symbols[i]
		name := names[s.ID]
		if name == "" || infoDone[name] {
			continue
		}
		infoDone[name] = true
		d := doc(s)
		info := symbolInfo{symbol: name, display: s.Name, kind: scipKind[s.Kind], implements: dedupe(implements[s.ID])}
		if s.ParentSymbol != "" {
			info.enclosing = parentName(s)
		}
		d.infos = append(d.infos, info)
		if line, col := src.namePosition(s.FilePath, s.Span.Start, s.Name); line >= 0 {
			d.occs = append(d.occs, occurrence{line: line, col: col, endCol: col + len(s.Name), symbol: name,
				roles: roleDefinition, enclosing: src.enclosingRange(s.FilePath, s.Span, line, col, col+len(s.Name))})
		}
	}
	for _, e := range edges {
		from, to := byID[e.From], byID[e.To]
		target := names[e.To]
		if from == nil || to == nil || target == "" || from.Kind == core.KindFile {
			continue
		}
		roles := uint64(0)
		switch e.Type {
		case core.EdgeCalls:
		case core.EdgeReads:
			roles = roleRead
		case core.EdgeWrites:
			roles = roleWrite
		case core.EdgeUsesType, core.EdgeExtends, core.EdgeImplements:
			for _, p := range src.wordPositions(from.FilePath, from.Span.Start, from.Span.End, to.Name) {
				if from.ID != to.ID {
					doc(from).occs = append(doc(from).occs, occurrence{line: p[0], col: p[1], endCol: p[1] + len(to.Name), symbol: target, enclosing: noEnclosing})
				}
			}
			continue
		default:
			continue
		}
		for _, p := range edgePositions(e, from, to, src) {
			doc(from).occs = append(doc(from).occs, occurrence{line: p[0], col: p[1], endCol: p[1] + len(to.Name), symbol: target, roles: roles, enclosing: noEnclosing})
		}
	}
	paths := make([]string, 0, len(docs))
	for p := range docs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []byte
	out = protowire.AppendTag(out, indexMetadata, protowire.BytesType)
	out = protowire.AppendBytes(out, metadata(opt))
	for _, p := range paths {
		out = protowire.AppendTag(out, indexDocuments, protowire.BytesType)
		out = protowire.AppendBytes(out, encodeDocument(docs[p]))
	}
	return out, nil
}

// assignNames gives each exported symbol its SCIP symbol. Same-named
// callables in one file (Java overloads, a TypeScript getter and setter) get
// SCIP's method disambiguator in source order: name()., name(+1)., ...; a
// second same-named non-callable shares the first one's symbol. Without
// this every overload shared one symbol and the index held duplicate
// SymbolInformation (ObjectMapper.readValue, 2026-11-28).
func assignNames(symbols []core.SymbolRecord) map[string]string {
	order := make([]int, 0, len(symbols))
	for i := range symbols {
		if exported(&symbols[i]) {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := &symbols[order[a]], &symbols[order[b]]
		if x.FilePath != y.FilePath {
			return x.FilePath < y.FilePath
		}
		if x.Span.Start != y.Span.Start {
			return x.Span.Start < y.Span.Start
		}
		return x.ID < y.ID
	})
	names := make(map[string]string, len(order))
	seen := map[string]int{}
	for _, i := range order {
		s := &symbols[i]
		base := symbolName(s)
		n := seen[base]
		seen[base] = n + 1
		if n > 0 && strings.HasSuffix(base, "().") {
			base = strings.TrimSuffix(base, "().") + "(+" + strconv.Itoa(n) + ")."
		}
		names[s.ID] = base
	}
	return names
}

// exported reports whether a symbol gets a SCIP symbol: declarations, not
// Grove's synthetic file, module-body and document nodes.
func exported(s *core.SymbolRecord) bool {
	if s.FilePath == "" || s.Name == "" || s.Name == "_" || strings.HasPrefix(s.Name, "<") {
		return false
	}
	switch s.Kind {
	case core.KindFile, core.KindDocument, core.KindDecorator, core.KindAnnotation:
		return false
	}
	return true
}

// symbolName is the SCIP symbol: scheme "prism", an empty package, the file
// as a namespace, then the qualified name. A member's owners are types (#);
// a callable ends in "()."; anything else is a term (".").
func symbolName(s *core.SymbolRecord) string {
	var b strings.Builder
	b.WriteString("prism . . . ")
	for _, seg := range strings.Split(s.FilePath, "/") {
		b.WriteString(escape(seg))
		b.WriteByte('/')
	}
	parts := strings.Split(s.QualifiedName, ".")
	if s.QualifiedName == "" {
		parts = []string{s.Name}
	}
	for _, p := range parts[:len(parts)-1] {
		b.WriteString(escape(p))
		b.WriteByte('#')
	}
	b.WriteString(escape(parts[len(parts)-1]))
	switch s.Kind {
	case core.KindFunction, core.KindMethod, core.KindConstructor, core.KindMacro:
		b.WriteString("().")
	case core.KindClass, core.KindInterface, core.KindStruct, core.KindTrait, core.KindEnum, core.KindType:
		b.WriteByte('#')
	case core.KindNamespace, core.KindModule:
		b.WriteByte('/')
	default:
		b.WriteByte('.')
	}
	return b.String()
}

// parentName is the SCIP symbol of a member's owner type.
func parentName(s *core.SymbolRecord) string {
	parts := strings.Split(s.QualifiedName, ".")
	if len(parts) < 2 {
		return ""
	}
	owner := core.SymbolRecord{FilePath: s.FilePath, QualifiedName: strings.Join(parts[:len(parts)-1], "."), Kind: core.KindClass}
	return symbolName(&owner)
}

// escape backquotes a descriptor that is not a simple identifier.
func escape(id string) string {
	simple := id != ""
	for _, r := range id {
		if !(r == '_' || r == '+' || r == '-' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			simple = false
			break
		}
	}
	if simple {
		return id
	}
	return "`" + strings.ReplaceAll(id, "`", "``") + "`"
}

func dedupe(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// edgePositions are the 0-based positions of the callee's name for one edge:
// the compiler-resolved "line:col" list when the edge has one, else the
// caller's call sites naming the callee.
func edgePositions(e core.Edge, from, to *core.SymbolRecord, src *sources) [][2]int {
	var out [][2]int
	if e.Lines != "" {
		for _, p := range strings.Split(e.Lines, ",") {
			l, c, _ := strings.Cut(strings.TrimSpace(p), ":")
			line, err1 := strconv.Atoi(l)
			col, err2 := strconv.Atoi(c)
			if err1 == nil && err2 == nil && line > 0 && col > 0 {
				out = append(out, [2]int{line - 1, src.byteCol(from.FilePath, line, col)})
			}
		}
		return out
	}
	for _, cs := range from.CallSites {
		callee := cs.Callee
		if i := strings.LastIndexAny(callee, ".:>"); i >= 0 {
			callee = callee[i+1:]
		}
		if callee != to.Name || cs.Line <= 0 {
			continue
		}
		if cs.Col > 0 {
			out = append(out, [2]int{cs.Line - 1, src.byteCol(from.FilePath, cs.Line, cs.Col)})
		} else if ps := src.wordPositions(from.FilePath, cs.Line, cs.Line, to.Name); len(ps) > 0 {
			out = append(out, ps[0])
		}
	}
	return out
}

// sources caches file lines for position lookups.
type sources struct {
	root  string
	files map[string][]string
}

func newSources(root string) *sources { return &sources{root: root, files: map[string][]string{}} }

func (s *sources) lines(path string) []string {
	if l, ok := s.files[path]; ok {
		return l
	}
	var l []string
	if data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(path))); err == nil && len(data) <= 8<<20 {
		l = strings.Split(string(data), "\n")
	}
	s.files[path] = l
	return l
}

var noEnclosing = [4]int{-1, -1, -1, -1}

// enclosingRange is a definition's enclosing range: from the first non-blank
// column of the span's first line to the end of its last line, widened to
// contain the name occurrence (scip.proto: the enclosing range must contain
// the occurrence). It used to be written as [start, 0, end, 0], an empty
// range on a one-line function that excluded the name (Codex, v0.64.7).
func (s *sources) enclosingRange(path string, span core.LineRange, nameLine, nameCol, nameEnd int) [4]int {
	src := s.lines(path)
	start, end := span.Start-1, span.End-1
	if start < 0 || start >= len(src) {
		start = nameLine
	}
	if end < start || end >= len(src) {
		end = max(start, nameLine)
	}
	if nameLine < start {
		start = nameLine
	}
	if nameLine > end {
		end = nameLine
	}
	startCol := len(src[start]) - len(strings.TrimLeft(src[start], " \t"))
	endCol := len(strings.TrimRight(src[end], "\r"))
	if start == nameLine && nameCol < startCol {
		startCol = nameCol
	}
	if end == nameLine && nameEnd > endCol {
		endCol = nameEnd
	}
	return [4]int{start, startCol, end, endCol}
}

// namePosition finds a declaration's name at or just after its span start.
func (s *sources) namePosition(path string, start int, name string) (int, int) {
	src := s.lines(path)
	for l := max(start, 1); l <= len(src) && l < start+8; l++ {
		if cols := wordCols(src[l-1], name); len(cols) > 0 {
			return l - 1, cols[0]
		}
	}
	return -1, -1
}

// wordPositions lists 0-based positions of name as a whole word within
// 1-based lines [from, to].
func (s *sources) wordPositions(path string, from, to int, name string) [][2]int {
	src := s.lines(path)
	var out [][2]int
	for l := max(from, 1); l <= len(src) && (to <= 0 || l <= to); l++ {
		for _, c := range wordCols(src[l-1], name) {
			out = append(out, [2]int{l - 1, c})
		}
	}
	return out
}

// byteCol converts a 1-based column (UTF-16, as the TypeScript checker and
// javac report it; equal to bytes for ASCII lines) to a 0-based byte offset.
func (s *sources) byteCol(path string, line, col int) int {
	src := s.lines(path)
	if line < 1 || line > len(src) {
		return col - 1
	}
	text, units := src[line-1], 0
	for i, r := range text {
		if units >= col-1 {
			return i
		}
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return len(text)
}

func wordCols(text, name string) []int {
	isIdent := func(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	var out []int
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], name)
		if i < 0 {
			break
		}
		at, end := from+i, from+i+len(name)
		before, _ := utf8.DecodeLastRuneInString(text[:at])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if (at == 0 || !isIdent(before)) && (end == len(text) || !isIdent(after)) {
			out = append(out, at)
		}
		from = end
	}
	return out
}

func metadata(opt Options) []byte {
	var tool []byte
	tool = protowire.AppendTag(tool, toolName, protowire.BytesType)
	tool = protowire.AppendString(tool, opt.ToolName)
	tool = protowire.AppendTag(tool, toolVersion, protowire.BytesType)
	tool = protowire.AppendString(tool, opt.ToolVersion)
	var m []byte
	m = protowire.AppendTag(m, metaVersion, protowire.VarintType)
	m = protowire.AppendVarint(m, 0)
	m = protowire.AppendTag(m, metaToolInfo, protowire.BytesType)
	m = protowire.AppendBytes(m, tool)
	m = protowire.AppendTag(m, metaProjectRoot, protowire.BytesType)
	m = protowire.AppendString(m, "file://"+filepath.ToSlash(opt.Root))
	m = protowire.AppendTag(m, metaTextEncoding, protowire.VarintType)
	m = protowire.AppendVarint(m, textEncodingUTF8)
	return m
}

func encodeDocument(d *document) []byte {
	sort.SliceStable(d.occs, func(i, j int) bool {
		a, b := d.occs[i], d.occs[j]
		if a.line != b.line {
			return a.line < b.line
		}
		if a.col != b.col {
			return a.col < b.col
		}
		return a.symbol < b.symbol
	})
	var out []byte
	out = protowire.AppendTag(out, docPath, protowire.BytesType)
	out = protowire.AppendString(out, d.path)
	seen := map[string]bool{}
	for _, o := range d.occs {
		key := fmt.Sprintf("%d:%d:%s:%d", o.line, o.col, o.symbol, o.roles)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = protowire.AppendTag(out, docOccurrences, protowire.BytesType)
		out = protowire.AppendBytes(out, encodeOccurrence(o))
	}
	for _, info := range d.infos {
		out = protowire.AppendTag(out, docSymbols, protowire.BytesType)
		out = protowire.AppendBytes(out, encodeInfo(info))
	}
	if d.language != "" {
		out = protowire.AppendTag(out, docLanguage, protowire.BytesType)
		out = protowire.AppendString(out, d.language)
	}
	out = protowire.AppendTag(out, docPositionEncoding, protowire.VarintType)
	out = protowire.AppendVarint(out, positionUTF8Bytes)
	return out
}

func packed(vals ...int) []byte {
	var b []byte
	for _, v := range vals {
		b = protowire.AppendVarint(b, uint64(int64(int32(v))))
	}
	return b
}

func encodeOccurrence(o occurrence) []byte {
	var out []byte
	out = protowire.AppendTag(out, occRange, protowire.BytesType)
	out = protowire.AppendBytes(out, packed(o.line, o.col, o.endCol))
	out = protowire.AppendTag(out, occSymbol, protowire.BytesType)
	out = protowire.AppendString(out, o.symbol)
	if o.roles != 0 {
		out = protowire.AppendTag(out, occRoles, protowire.VarintType)
		out = protowire.AppendVarint(out, o.roles)
	}
	if e := o.enclosing; e[0] >= 0 {
		out = protowire.AppendTag(out, occEnclosingRange, protowire.BytesType)
		if e[0] == e[2] {
			out = protowire.AppendBytes(out, packed(e[0], e[1], e[3]))
		} else {
			out = protowire.AppendBytes(out, packed(e[0], e[1], e[2], e[3]))
		}
	}
	return out
}

func encodeInfo(info symbolInfo) []byte {
	var out []byte
	out = protowire.AppendTag(out, infoSymbol, protowire.BytesType)
	out = protowire.AppendString(out, info.symbol)
	for _, impl := range info.implements {
		var rel []byte
		rel = protowire.AppendTag(rel, relSymbol, protowire.BytesType)
		rel = protowire.AppendString(rel, impl)
		rel = protowire.AppendTag(rel, relImplementation, protowire.VarintType)
		rel = protowire.AppendVarint(rel, 1)
		out = protowire.AppendTag(out, infoRelations, protowire.BytesType)
		out = protowire.AppendBytes(out, rel)
	}
	if info.kind != 0 {
		out = protowire.AppendTag(out, infoKind, protowire.VarintType)
		out = protowire.AppendVarint(out, info.kind)
	}
	if info.display != "" {
		out = protowire.AppendTag(out, infoDisplay, protowire.BytesType)
		out = protowire.AppendString(out, info.display)
	}
	if info.enclosing != "" {
		out = protowire.AppendTag(out, infoEnclosing, protowire.BytesType)
		out = protowire.AppendString(out, info.enclosing)
	}
	return out
}
