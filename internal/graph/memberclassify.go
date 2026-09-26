package graph

// Per-occurrence classification for data-member change impact: decides
// whether each source occurrence of a member's name binds to the queried
// declaration (confirmed), cannot be decided (ambiguous, with the reason),
// or binds elsewhere (excluded). See memberimpact.go for the overview.

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// ── classification ──────────────────────────────────────────────────────

type verdict int

const (
	vSkip verdict = iota // not a reference to a data member at all
	vExclude
	vAmbiguous
	vConfirm
)

type memberClassifier struct {
	g        *CodeGraph
	a        *memberAnchor
	lang     string
	idx      *edgeIndex
	ltCache  map[string]map[string]string
	fileSyms map[string][]*core.SymbolRecord
	// otherDeclarers: types (by name) that declare their own member of this
	// name — a receiver typed as one of them is a confident exclusion.
	otherDeclarers map[string]bool
	// shadowFrom: enclosing-symbol ID → first line declaring a local or
	// parameter of the name; unqualified uses from there on are the local.
	shadowFrom map[string]int
	// imports: files that import the ownerless member by name → whether the
	// import names the declaring module.
	imports map[string]bool
	goPkg   string

	confirmed, ambiguous []MemberAccess
	excluded             int
}

func newMemberClassifier(g *CodeGraph, a *memberAnchor) *memberClassifier {
	c := &memberClassifier{g: g, a: a, lang: a.decls[0].Language,
		ltCache: map[string]map[string]string{}, fileSyms: map[string][]*core.SymbolRecord{},
		otherDeclarers: map[string]bool{}, shadowFrom: map[string]int{}, imports: map[string]bool{}}
	for _, id := range g.idsNamed(a.name) {
		s := g.symbols[id]
		if s.ParentSymbol != "" && !a.owners[s.ParentSymbol] {
			c.otherDeclarers[s.ParentSymbol] = true
		}
	}
	return c
}

func (c *memberClassifier) edgeIdx() *edgeIndex {
	if c.idx == nil {
		syms := make([]core.SymbolRecord, 0, len(c.g.symbols))
		for _, s := range c.g.symbols {
			syms = append(syms, s)
		}
		c.idx = newEdgeIndex(syms)
	}
	return c.idx
}

func (c *memberClassifier) localTypes(s *core.SymbolRecord) map[string]string {
	if lt, ok := c.ltCache[s.ID]; ok {
		return lt
	}
	lt := localTypesForSymbol(c.edgeIdx(), s)
	c.ltCache[s.ID] = lt
	return lt
}

// enclosing returns the tightest indexed symbol whose span holds line,
// preferring executable symbols over the declarations they sit in.
func (c *memberClassifier) enclosing(file string, line int) *core.SymbolRecord {
	syms, ok := c.fileSyms[file]
	if !ok {
		syms = c.edgeIdx().byFile[file]
		c.fileSyms[file] = syms
	}
	var best *core.SymbolRecord
	bestSpan := 1 << 30
	for _, s := range syms {
		if s.Kind == core.KindFile || s.Kind == core.KindModule || s.Kind == core.KindNamespace {
			continue
		}
		if s.Span.Start <= line && line <= s.Span.End {
			if span := s.Span.End - s.Span.Start; span < bestSpan {
				best, bestSpan = s, span
			}
		}
	}
	return best
}

func (c *memberClassifier) isDeclSite(o core.MemberOccurrence) bool {
	for _, d := range c.a.decls {
		if d.FilePath == o.File && d.Span.Start <= o.Line && o.Line <= d.Span.End {
			return true
		}
	}
	return false
}

func (c *memberClassifier) classify(occs []core.MemberOccurrence) {
	// Pass 1: scopes that shadow the name, imports, and the Go package name.
	for _, o := range occs {
		switch o.Form {
		case "localdecl":
			if e := c.enclosing(o.File, o.Line); e != nil {
				if from, ok := c.shadowFrom[e.ID]; !ok || o.Line < from {
					c.shadowFrom[e.ID] = o.Line
				}
			}
		case "import":
			if c.importNamesDecl(o) {
				c.imports[o.File] = true
			}
		}
		if o.Package != "" && c.isDeclSite(o) {
			c.goPkg = o.Package
		}
	}
	type lineKey struct {
		file string
		line int
	}
	type lineState struct {
		v        verdict
		access   string
		evidence string
		occ      core.MemberOccurrence
		encl     *core.SymbolRecord
		cols     []int
	}
	states := map[lineKey]*lineState{}
	var order []lineKey
	for _, o := range occs {
		var e *core.SymbolRecord
		v, evidence := vSkip, ""
		if (o.Form == "decl" || o.Form == "bare" || o.Form == "key") && c.isDeclSite(o) {
			v, evidence = vConfirm, "declaration"
		} else {
			e = c.enclosing(o.File, o.Line)
			v, evidence = c.classifyOne(o, e)
		}
		if v == vSkip {
			continue
		}
		k := lineKey{o.File, o.Line}
		st, ok := states[k]
		if !ok {
			st = &lineState{}
			states[k] = st
			order = append(order, k)
		}
		acc := accessKind(o, evidence)
		if v > st.v {
			st.v, st.evidence, st.occ, st.encl, st.access = v, evidence, o, e, acc
			st.cols = []int{o.Col}
		} else if v == st.v {
			st.cols = append(st.cols, o.Col)
			if v == vConfirm && acc == "write" {
				st.access = "write"
			}
		}
	}
	for _, k := range order {
		st := states[k]
		acc := MemberAccess{FilePath: k.file, Line: k.line, Access: st.access, Evidence: st.evidence, Text: st.occ.Text, Cols: st.cols}
		if st.encl != nil {
			acc.Enclosing = *st.encl
		} else if e := c.enclosing(k.file, k.line); e != nil {
			acc.Enclosing = *e
		}
		switch st.v {
		case vConfirm:
			c.confirmed = append(c.confirmed, acc)
		case vAmbiguous:
			c.ambiguous = append(c.ambiguous, acc)
		case vExclude:
			c.excluded++
		}
	}
}

// shadowedAt reports whether an unqualified occurrence names a same-named
// local or parameter of its enclosing function: declared on or before the
// line (block-scoped languages), or anywhere in the function (Python, where
// any assignment makes the name local for the whole body).
func (c *memberClassifier) shadowedAt(e *core.SymbolRecord, o core.MemberOccurrence) bool {
	if e == nil {
		return false
	}
	from, ok := c.shadowFrom[e.ID]
	if !ok {
		return false
	}
	return o.Language == "python" || o.Line >= from
}

func accessKind(o core.MemberOccurrence, evidence string) string {
	switch {
	case evidence == "declaration":
		return "decl"
	case o.Form == "key":
		return "init"
	case o.Write:
		return "write"
	case o.Call:
		return "call"
	}
	return "read"
}

// importNamesDecl reports whether an import occurrence imports the anchor
// from its declaring module (module path's last segment matches the
// declaring file's stem or directory).
func (c *memberClassifier) importNamesDecl(o core.MemberOccurrence) bool {
	mod := strings.Trim(o.Receiver, `"'`)
	mod = strings.TrimSuffix(mod, path.Ext(mod))
	last := mod
	if i := strings.LastIndexAny(last, "./"); i >= 0 {
		last = last[i+1:]
	}
	for _, d := range c.a.decls {
		stem := strings.TrimSuffix(path.Base(d.FilePath), path.Ext(d.FilePath))
		if last == stem || (stem == "__init__" && last == path.Base(dirOf(d.FilePath))) || (stem == "index" && last == path.Base(dirOf(d.FilePath))) {
			return true
		}
	}
	return false
}

var implicitThisLanguages = map[string]bool{
	"java": true, "csharp": true, "cpp": true, "kotlin": true, "swift": true, "objc": true,
}

var staticTypedLanguages = map[string]bool{
	"go": true, "java": true, "csharp": true, "c": true, "cpp": true, "kotlin": true,
	"swift": true, "rust": true, "typescript": true, "tsx": true, "objc": true,
}

// ownerOf is the type an enclosing symbol belongs to ("" at top level).
func ownerOf(e *core.SymbolRecord) string {
	if e == nil {
		return ""
	}
	if isTypeKind(e.Kind) {
		return e.Name
	}
	return e.ParentSymbol
}

func (c *memberClassifier) selfWords(e *core.SymbolRecord) map[string]bool {
	w := map[string]bool{"this": true, "self": true, "$this": true, "static": true, "cls": true, "Self": true}
	if e != nil && e.Language == "go" && e.Kind == core.KindMethod {
		if v := goReceiverVar(e.Signature); v != "" {
			w[v] = true
		}
	}
	if e != nil && e.Language == "python" && isCallableKind(e.Kind) && e.ParentSymbol != "" {
		// first positional parameter of a method, whatever it is called
		if v := pyFirstParam(e.Signature); v != "" {
			w[v] = true
		}
	}
	return w
}

func pyFirstParam(sig string) string {
	open := strings.IndexByte(sig, '(')
	if open < 0 {
		return ""
	}
	rest := sig[open+1:]
	end := strings.IndexAny(rest, ",):=")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// normalizeReceiver maps grammar-specific receiver text onto the dotted
// chain the local-type resolvers take: $this->a->b → this.a.b, p->q → p.q,
// a?.b → a.b, Type::X qualifiers keep their last segment.
func normalizeReceiver(recv string) string {
	r := strings.NewReplacer("?->", ".", "->", ".", "?.", ".", "!.", ".", "::", ".", "$", "", "\\", ".", "(*", "(", "&", "").Replace(recv)
	r = strings.TrimLeft(r, "*")
	for len(r) >= 2 && r[0] == '(' && r[len(r)-1] == ')' && balancedParens(r[1:len(r)-1]) {
		r = strings.TrimLeft(r[1:len(r)-1], "*")
	}
	return strings.TrimPrefix(r, ".")
}

func balancedParens(s string) bool {
	depth := 0
	for _, ch := range s {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func lastSegment(s string) string {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func (c *memberClassifier) typeExists(name string) bool {
	for _, id := range c.g.idsNamed(name) {
		if isTypeKind(c.g.symbols[id].Kind) {
			return true
		}
	}
	return false
}

// judgeType turns a resolved receiver/literal type into a verdict.
func (c *memberClassifier) judgeType(t, how string) (verdict, string) {
	t = strings.TrimPrefix(t, "class:")
	if i := strings.IndexByte(t, '<'); i > 0 {
		t = t[:i]
	}
	t = lastSegment(t)
	switch {
	case c.a.owners[t]:
		return vConfirm, how
	case c.otherDeclarers[t]:
		return vExclude, ""
	case !c.typeExists(t):
		if staticTypedLanguages[c.lang] {
			return vExclude, "" // typed as a type outside the project's declarations
		}
		return vAmbiguous, fmt.Sprintf("receiver typed %s (not an indexed type)", t)
	}
	return vAmbiguous, fmt.Sprintf("receiver typed %s, which neither declares nor inherits %s", t, c.a.name)
}

func (c *memberClassifier) classifyOne(o core.MemberOccurrence, e *core.SymbolRecord) (verdict, string) {
	switch o.Form {
	case "localdecl", "import":
		return vSkip, ""
	case "decl":
		return vSkip, "" // another declaration of the same name
	}
	if o.Call && (o.Form == "access" || o.Form == "bare") && !c.declCallable() {
		// x.name(...) is a METHOD call in these languages unless the
		// member itself holds a function: `c.Errors.Errors()` calls
		// errorMsgs.Errors, it does not read Context.Errors.
		switch o.Language {
		case "java", "csharp", "rust", "kotlin", "go":
			return vSkip, ""
		}
	}
	if c.a.ownerless {
		return c.classifyOwnerless(o, e)
	}
	switch o.Form {
	case "access":
		return c.classifyAccess(o, e)
	case "key":
		if o.Receiver == "" {
			if c.lang == "typescript" || c.lang == "tsx" || c.lang == "javascript" || c.lang == "python" || c.lang == "php" || c.lang == "kotlin" {
				return vExclude, "" // untyped object key / named argument: not attributable
			}
			return vAmbiguous, "initializer key whose literal type could not be named"
		}
		t := lastSegment(normalizeReceiver(o.Receiver))
		if c.a.owners[t] {
			return vConfirm, "literal of type " + t
		}
		if c.typeExists(t) || c.lang == "python" || c.lang == "kotlin" || c.lang == "php" {
			return vExclude, "" // another type's literal, or a keyword argument to a function
		}
		return vAmbiguous, "initializer of unresolved type " + t
	case "bare":
		if !implicitThisLanguages[o.Language] {
			return vSkip, ""
		}
		if c.shadowedAt(e, o) {
			return vExclude, "" // the same-named local/parameter, not the member
		}
		p := ownerOf(e)
		if c.a.owners[p] {
			return vConfirm, "implicit this in " + p
		}
		if p != "" && c.otherDeclarers[p] {
			return vExclude, ""
		}
		if p == "" {
			return vSkip, ""
		}
		return vAmbiguous, "unqualified name in " + p + " (static import or inherited member?)"
	}
	return vSkip, ""
}

func (c *memberClassifier) classifyAccess(o core.MemberOccurrence, e *core.SymbolRecord) (verdict, string) {
	raw := o.Receiver
	recv := normalizeReceiver(raw)
	if recv == "" {
		return vAmbiguous, "receiver not named in source"
	}
	self := c.selfWords(e)
	first := recv
	if i := strings.IndexByte(first, '.'); i >= 0 {
		first = first[:i]
	}
	if !strings.Contains(recv, ".") && self[recv] {
		p := ownerOf(e)
		if c.a.owners[p] {
			return vConfirm, "self receiver in " + p
		}
		if p == "" {
			return vAmbiguous, "self receiver outside any indexed type"
		}
		if c.otherDeclarers[p] || staticTypedLanguages[c.lang] {
			return vExclude, ""
		}
		return vAmbiguous, "self receiver in " + p + ", which does not inherit from the owner"
	}
	if recv == "parent" && c.lang == "php" {
		return vAmbiguous, "parent:: qualifier"
	}
	var lt map[string]string
	if e != nil {
		lt = c.localTypes(e)
	}
	// A type qualifier: Owner.CONST, Owner::X, pkg.Owner.X — only where the
	// syntax can qualify by type at all. C `p->f`/`s.f` and Go `x.f` never
	// do, and a variable named like a struct tag (`hashtable->size` with a
	// `struct hashtable`) must not read as one.
	if _, isLocal := lt[first]; !isLocal && !self[first] && typeQualifiable(o) {
		if t := lastSegment(recv); t != "" && c.typeExists(t) && (!strings.Contains(recv, ".") || !isLowerIdentStart(t)) {
			if c.a.owners[t] {
				return vConfirm, "type qualifier " + t
			}
			return vExclude, ""
		}
	}
	if e == nil {
		return vAmbiguous, "receiver " + raw + " outside any indexed symbol"
	}
	chain := recv
	if self[first] && first != "this" && first != "self" && strings.Contains(recv, ".") {
		// Go receiver variable / Python first param: resolve the hop
		// through the owner's own field list.
		chain = "this" + recv[len(first):]
		if e.Language == "go" {
			lt2 := map[string]string{}
			for k, v := range lt {
				lt2[k] = v
			}
			lt2[first] = ownerOf(e)
			lt = lt2
			chain = recv
		}
	}
	var t string
	if open := strings.IndexByte(chain, '('); open > 0 && strings.HasSuffix(chain, ")") && balancedParens(chain[open+1:len(chain)-1]) && !strings.ContainsAny(chain[:open], "()") {
		t = c.callResultType(e, chain)
	} else if strings.Contains(chain, "(") || strings.Contains(chain, "[") {
		t = ""
	} else {
		t = resolveReceiverType(c.edgeIdx(), e, lt, chain)
		if t == "" && (e.Language == "c" || e.Language == "cpp" || e.Language == "objc") {
			t = c.cChainType(e, lt, chain)
		}
		if t == "" && (e.Language == "typescript" || e.Language == "tsx" || e.Language == "javascript") {
			t = c.tsChainType(e, lt, chain)
		}
		if t == "" && e.Language == "go" && !strings.Contains(chain, ".") {
			t = c.goTupleResultType(e, chain)
		}
		if t == "" && e.Language == "python" && !strings.Contains(chain, ".") {
			t = c.pyNameType(e, chain)
		}
	}
	if t == "" {
		if len(c.otherDeclarers) == 0 {
			return vAmbiguous, fmt.Sprintf("receiver %s untyped; %s is the only indexed declarer of %s", raw, ownerNames(c.a), c.a.name)
		}
		return vAmbiguous, fmt.Sprintf("receiver %s untyped; %d other type(s) also declare %s", raw, len(c.otherDeclarers), c.a.name)
	}
	return c.judgeType(t, "receiver "+raw+" typed "+lastSegment(strings.TrimPrefix(t, "class:")))
}

// declCallable reports whether the member may hold a function value (so a
// call through it is still an access of it).
func (c *memberClassifier) declCallable() bool {
	for _, d := range c.a.decls {
		switch d.Language {
		case "go":
			if strings.Contains(d.Signature, "func") {
				return true
			}
		case "java":
		case "csharp":
			if strings.Contains(d.Signature, "Func<") || strings.Contains(d.Signature, "Action") || strings.Contains(d.Signature, "delegate") {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// cDeclaredType finds `T *name` / `T name` / `struct T *name` declarations
// of a C/C++ receiver variable in the enclosing function's text (parameters
// and body) and returns T when it is an indexed type. The shared C-family
// local-type pass misses the common `T *name` spelling (star attached to
// the name), which left every `array->entries` in jansson untyped.
func (c *memberClassifier) cDeclaredType(e *core.SymbolRecord, name string) string {
	if e == nil || name == "" {
		return ""
	}
	text := stripCommentsAndStrings(e.Signature + "\n" + e.RawText)
	re := regexp.MustCompile(`(?:\bstruct\s+)?\b([A-Za-z_]\w*)\s*(?:\*+\s*(?:const\s+)?|&\s*|\s+)(?:\w+\s*(?:\[[^\]]*\])?\s*,\s*\**\s*)*` + regexp.QuoteMeta(name) + `\s*(?:[=;,)\[])`)
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		switch m[1] {
		case "return", "sizeof", "const", "struct", "else", "case", "goto":
			continue
		}
		if c.typeExists(m[1]) {
			return m[1]
		}
	}
	return ""
}

var goTupleAssignRe = regexp.MustCompile(`(?m)\b([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)+)\s*:?=\s*(?:[A-Za-z_]\w*\.)?([A-Za-z_]\w*)\(`)

// goTupleResultType types `a, b := f(...)` bindings by position through the
// callee's result list — the shared local-type pass only types the first
// variable (`c, router := CreateTestContext(w)` left router untyped).
func (c *memberClassifier) goTupleResultType(e *core.SymbolRecord, name string) string {
	for _, m := range goTupleAssignRe.FindAllStringSubmatch(stripCommentsAndStrings(e.RawText), -1) {
		vars := strings.Split(m[1], ",")
		pos := -1
		for i, v := range vars {
			if strings.TrimSpace(v) == name {
				pos = i
			}
		}
		if pos < 0 {
			continue
		}
		found := ""
		for _, id := range c.g.idsNamed(m[2]) {
			f := c.g.symbols[id]
			if f.Language != "go" || !isCallableKind(f.Kind) {
				continue
			}
			t := goResultTypeAt(f.Signature, pos)
			if t == "" || !c.typeExists(t) || (found != "" && found != t) {
				found = ""
				break
			}
			found = t
		}
		if found != "" {
			return found
		}
	}
	return ""
}

// goResultTypeAt is the bare type name of result #pos in a Go signature.
func goResultTypeAt(sig string, pos int) string {
	if rest, ok := strings.CutPrefix(sig, "func ("); ok {
		if end := strings.IndexByte(rest, ')'); end >= 0 {
			sig = "func " + rest[end+1:]
		}
	}
	open := strings.IndexByte(sig, '(')
	if open < 0 {
		return ""
	}
	depth, end := 0, -1
	for i := open; i < len(sig) && end < 0; i++ {
		switch sig[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
	}
	if end < 0 {
		return ""
	}
	res := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sig[end+1:]), "{"))
	if !strings.HasPrefix(res, "(") {
		if pos == 0 {
			return goBareTypeToken(res)
		}
		return ""
	}
	parts := strings.Split(strings.Trim(res, "()"), ",")
	if pos >= len(parts) {
		return ""
	}
	return goBareTypeToken(parts[pos])
}

func goBareTypeToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	t := strings.TrimLeft(fields[len(fields)-1], "*[]")
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	return t
}

var (
	tsReturnTypeRe   = regexp.MustCompile(`\)\s*:\s*(?:Promise<\s*)?([A-Za-z_]\w*)`)
	tsReturnObjectRe = regexp.MustCompile(`\)\s*:\s*(?:Promise<\s*)?\{`)
	tsMemberTypeRe   = regexp.MustCompile(`^[^:(]*\??!?\s*:\s*(?:readonly\s+)?([A-Za-z_]\w*)`)
)

// tsAnonymousType marks a value of an inline object type (`): { a: T }`):
// no class declares its members, so a static receiver of that type is
// never the queried class.
const tsAnonymousType = "{anonymous}"

// supertypeNames is owner plus every indexed supertype name above it.
func (c *memberClassifier) supertypeNames(owner string) map[string]bool {
	out := map[string]bool{owner: true}
	frontier := []string{}
	for _, id := range c.g.idsNamed(owner) {
		if isTypeKind(c.g.symbols[id].Kind) {
			frontier = append(frontier, id)
		}
	}
	seen := map[string]bool{}
	for depth := 0; depth < 8 && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			for _, ei := range c.g.outbound[id] {
				e := c.g.edges[ei]
				if (e.Type == core.EdgeExtends || e.Type == core.EdgeImplements) && !seen[e.To] {
					seen[e.To] = true
					if s, ok := c.g.symbols[e.To]; ok {
						out[s.Name] = true
					}
					next = append(next, e.To)
				}
			}
		}
		frontier = next
	}
	return out
}

func tsDeclaredReturnType(sig string) string {
	if tsReturnObjectRe.MatchString(sig) {
		return tsAnonymousType
	}
	if m := tsReturnTypeRe.FindStringSubmatch(sig); m != nil {
		return m[1]
	}
	return ""
}

// tsChainType types a TS/JS receiver chain hop by hop: the first segment
// (this → the enclosing class; a local via the shared pass or tsNameType),
// then each member through its declared field type or getter return type.
func (c *memberClassifier) tsChainType(e *core.SymbolRecord, lt map[string]string, chain string) string {
	parts := strings.Split(chain, ".")
	var cur string
	switch {
	case parts[0] == "this":
		cur = ownerOf(e)
	case lt[parts[0]] != "":
		cur = lastSegment(strings.TrimPrefix(lt[parts[0]], "class:"))
	default:
		cur = c.tsNameType(e, parts[0])
	}
	for _, seg := range parts[1:] {
		if cur == "" || cur == tsAnonymousType {
			return cur
		}
		cur = c.tsMemberType(cur, seg)
	}
	return cur
}

// tsMemberType is the declared type of member `name` on class `owner`: a
// field annotation (`metadata?: EntityMetadata`) or a getter's return type.
func (c *memberClassifier) tsMemberType(owner, name string) string {
	lineage := c.supertypeNames(owner)
	for _, id := range c.g.idsNamed(name) {
		f := c.g.symbols[id]
		if !lineage[f.ParentSymbol] {
			continue
		}
		var t string
		if isCallableKind(f.Kind) {
			if !strings.Contains(f.Signature, "get ") {
				continue
			}
			t = tsDeclaredReturnType(f.Signature)
		} else if m := tsMemberTypeRe.FindStringSubmatch(f.Signature); m != nil {
			t = m[1]
		}
		if t == tsAnonymousType || (t != "" && c.typeExists(t)) {
			return t
		}
	}
	return ""
}

// tsNameType types a TS/JS local the shared pass leaves untyped, through the
// two shapes that dominate real code: `const m = <call>(...)` (the callee's
// declared return type, unwrapping Promise) and `for (const m of <expr>)`
// (the element type of a typed array field or local).
func (c *memberClassifier) tsNameType(e *core.SymbolRecord, name string) string {
	body := stripCommentsAndStrings(e.RawText)
	q := regexp.QuoteMeta(name)
	callRe := regexp.MustCompile(`(?:const|let|var)\s+` + q + `\s*=\s*(?:await\s+)?[\w.!?]*?([A-Za-z_]\w*)\s*(?:<[^>()]*>)?\(`)
	if m := callRe.FindStringSubmatch(body); m != nil {
		found := ""
		for _, id := range c.g.idsNamed(m[1]) {
			f := c.g.symbols[id]
			if !isCallableKind(f.Kind) || memberLanguageFamily(f.Language) != "typescript" {
				continue
			}
			rt := tsDeclaredReturnType(f.Signature)
			if rt == "" || (rt != tsAnonymousType && !c.typeExists(rt)) || (found != "" && found != rt) {
				found = ""
				break
			}
			found = rt
		}
		if found != "" {
			return found
		}
	}
	ofRe := regexp.MustCompile(`for\s*\(\s*(?:const|let|var)\s+` + q + `\s+of\s+([\w.]+)\s*\)`)
	if m := ofRe.FindStringSubmatch(body); m != nil {
		src := m[1]
		var elem string
		if rest, ok := strings.CutPrefix(src, "this."); ok && !strings.Contains(rest, ".") {
			elem = c.tsFieldElemType(ownerOf(e), rest)
		} else if !strings.Contains(src, ".") {
			elem = tsArrayElem(c.localTypes(e)[src])
		}
		if elem != "" && c.typeExists(elem) {
			return elem
		}
	}
	return ""
}

func tsArrayElem(t string) string {
	t = strings.TrimSpace(strings.TrimPrefix(t, "class:"))
	if strings.HasSuffix(t, "[]") {
		return strings.TrimSuffix(t, "[]")
	}
	if inner, ok := strings.CutPrefix(t, "Array<"); ok {
		return strings.TrimSuffix(inner, ">")
	}
	return ""
}

var tsFieldArrayRe = regexp.MustCompile(`:\s*(?:readonly\s+)?(?:([A-Za-z_]\w*)\s*\[\]|Array<\s*([A-Za-z_]\w*)\s*>)`)

// tsFieldElemType is the element type of an array-typed field on owner.
func (c *memberClassifier) tsFieldElemType(owner, field string) string {
	for _, id := range c.g.idsNamed(field) {
		f := c.g.symbols[id]
		if f.ParentSymbol != owner || f.Kind != core.KindField {
			continue
		}
		if m := tsFieldArrayRe.FindStringSubmatch(f.Signature); m != nil {
			if m[1] != "" {
				return m[1]
			}
			return m[2]
		}
	}
	return ""
}

// typeQualifiable reports whether the occurrence's syntax can name a type
// as its qualifier.
func typeQualifiable(o core.MemberOccurrence) bool {
	switch o.Language {
	case "go", "c", "objc":
		return false
	case "cpp", "rust", "php":
		return strings.Contains(o.Receiver, "::") || !strings.ContainsAny(o.Receiver, "$.->")
	}
	return true
}

// cChainType types a C/C++ receiver chain (`object->hashtable` →
// hashtable_t): the first segment through the enclosing function's
// declarations, each further hop through the indexed field declaration's
// type.
func (c *memberClassifier) cChainType(e *core.SymbolRecord, lt map[string]string, chain string) string {
	parts := strings.Split(chain, ".")
	cur := strings.TrimPrefix(lt[parts[0]], "class:")
	if cur == "" {
		cur = c.cDeclaredType(e, parts[0])
	}
	for _, seg := range parts[1:] {
		if cur == "" {
			return ""
		}
		cur = c.cFieldType(cur, seg)
	}
	return cur
}

var cFieldTypeRe = regexp.MustCompile(`^\s*(?:(?:const|volatile|struct|union|enum|mutable|static)\s+)*([A-Za-z_][\w:]*)`)

// cFieldType is the indexed type of field `field` declared on `typeName`.
func (c *memberClassifier) cFieldType(typeName, field string) string {
	for _, id := range c.g.idsNamed(field) {
		f := c.g.symbols[id]
		if f.ParentSymbol != typeName || f.Kind != core.KindField {
			continue
		}
		if m := cFieldTypeRe.FindStringSubmatch(f.Signature); m != nil {
			t := lastSegment(strings.ReplaceAll(m[1], "::", "."))
			if c.typeExists(t) {
				return t
			}
		}
	}
	return ""
}

// callResultType types a call-result receiver (`f(x)->m`, `create_app().m`)
// through the callee's declared or evident return type, when exactly one
// indexed callable of that name exists in the language.
func (c *memberClassifier) callResultType(e *core.SymbolRecord, chain string) string {
	open := strings.IndexByte(chain, '(')
	if open <= 0 {
		return ""
	}
	callee := lastSegment(chain[:open])
	var cands []core.SymbolRecord
	best := -1
	for _, id := range c.g.idsNamed(callee) {
		f := c.g.symbols[id]
		if !isCallableKind(f.Kind) && f.Kind != core.KindMacro {
			continue
		}
		if memberLanguageFamily(f.Language) != memberLanguageFamily(e.Language) {
			continue
		}
		// Same-named helpers in unrelated subtrees (every example app has
		// its own create_app): keep the candidates nearest the call site.
		shared := sharedDirDepth(f.FilePath, e.FilePath)
		if shared > best {
			best, cands = shared, nil
		}
		if shared == best {
			cands = append(cands, f)
		}
	}
	var found string
	for i := range cands {
		f := cands[i]
		t := c.returnTypeOf(&f)
		if t == "" || (found != "" && found != t) {
			return ""
		}
		found = t
	}
	return found
}

// sharedDirDepth counts the leading directory segments two paths share.
func sharedDirDepth(a, b string) int {
	as, bs := strings.Split(dirOf(a), "/"), strings.Split(dirOf(b), "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] && as[n] != "" && as[n] != "." {
		n++
	}
	return n
}

func memberLanguageFamily(lang string) string {
	return memberLanguages(lang)[0]
}

var (
	cContainerOfRe = regexp.MustCompile(`container_of\s*\(\s*[^,]+,\s*(?:struct\s+)?([A-Za-z_]\w*)\s*,`)
	cCastRe        = regexp.MustCompile(`\(\s*(?:struct\s+)?([A-Za-z_]\w*)\s*\*\s*\)`)
	pyReturnVarRe  = regexp.MustCompile(`(?m)^\s*(?:return|yield)\s+([A-Za-z_]\w*)\s*$`)
	pyReturnCtorRe = regexp.MustCompile(`(?m)^\s*(?:return|yield)\s+(?:[A-Za-z_]\w*\.)?([A-Z]\w*)\(`)
	pyArrowRe      = regexp.MustCompile(`->\s*["']?(?:[A-Za-z_]\w*\.)?([A-Za-z_]\w*)`)
)

// returnTypeOf is the indexed type a callable evidently returns.
func (c *memberClassifier) returnTypeOf(f *core.SymbolRecord) string {
	switch f.Language {
	case "go":
		return goReturnType(c.edgeIdx(), f)
	case "c", "cpp", "objc":
		if f.Kind == core.KindMacro {
			body := f.RawText
			for _, re := range []*regexp.Regexp{cContainerOfRe, cCastRe} {
				if m := re.FindStringSubmatch(body); m != nil && c.typeExists(m[1]) {
					return m[1]
				}
			}
			return ""
		}
		re := regexp.MustCompile(`\b(?:struct\s+)?([A-Za-z_]\w*)\s*\*?\s*` + regexp.QuoteMeta(f.Name) + `\s*\(`)
		if m := re.FindStringSubmatch(f.Signature); m != nil && c.typeExists(m[1]) {
			return m[1]
		}
	case "python":
		if m := pyArrowRe.FindStringSubmatch(f.Signature); m != nil && c.typeExists(m[1]) {
			return m[1]
		}
		body := stripCommentsAndStrings(f.RawText)
		if m := pyReturnCtorRe.FindStringSubmatch(body); m != nil && c.typeExists(m[1]) {
			return m[1]
		}
		if m := pyReturnVarRe.FindStringSubmatch(body); m != nil {
			if t := strings.TrimPrefix(c.localTypes(f)[m[1]], "class:"); t != "" {
				return lastSegment(t)
			}
			return c.pyModuleVarType(m[1])
		}
	}
	return ""
}

// pyNameType types an untyped Python receiver name through the two shapes
// tests reach objects by: a pytest fixture parameter (the fixture's return
// type), or an imported module-level variable (`app = Flask(__name__)`).
func (c *memberClassifier) pyNameType(e *core.SymbolRecord, name string) string {
	if isPyParam(e.Signature, name) {
		var found string
		for _, f := range c.pyFixtures(e, name) {
			t := c.returnTypeOf(&f)
			if t == "" || (found != "" && found != t) {
				return ""
			}
			found = t
		}
		return found
	}
	return c.pyModuleVarType(name)
}

func isPyParam(sig, name string) bool {
	open, close := strings.IndexByte(sig, '('), strings.LastIndexByte(sig, ')')
	if open < 0 || close < open {
		return false
	}
	for _, p := range strings.Split(sig[open+1:close], ",") {
		p = strings.TrimSpace(p)
		if i := strings.IndexAny(p, ":="); i >= 0 {
			p = strings.TrimSpace(p[:i])
		}
		if p == name {
			return true
		}
	}
	return false
}

// pyFixtures returns the pytest fixtures named name visible from e: same
// file, or conftest.py in e's directory or any ancestor.
func (c *memberClassifier) pyFixtures(e *core.SymbolRecord, name string) []core.SymbolRecord {
	visible := func(f core.SymbolRecord) bool {
		if f.FilePath == e.FilePath {
			return true
		}
		if path.Base(f.FilePath) != "conftest.py" {
			return false
		}
		fd := dirOf(f.FilePath)
		return fd == "" || fd == "." || dirOf(e.FilePath) == fd || strings.HasPrefix(e.FilePath, fd+"/")
	}
	var out []core.SymbolRecord
	for _, f := range c.g.symbols {
		if f.Language != "python" || f.Kind != core.KindFunction || !visible(f) {
			continue
		}
		for _, a := range f.Annotations {
			if !strings.Contains(a, "fixture") {
				continue
			}
			named := strings.Contains(a, `name="`+name+`"`) || strings.Contains(a, `name='`+name+`'`)
			if named || (f.Name == name && !strings.Contains(a, "name=")) {
				out = append(out, f)
			}
		}
	}
	sortSymbols(out)
	return out
}

// pyModuleVarType types a module-level Python variable assigned a
// constructor call, when exactly one such variable of that name exists.
func (c *memberClassifier) pyModuleVarType(name string) string {
	var found string
	for _, id := range c.g.idsNamed(name) {
		v := c.g.symbols[id]
		if v.Language != "python" || v.ParentSymbol != "" || !dataMemberKinds[v.Kind] {
			continue
		}
		m := pyModuleCtorRe.FindStringSubmatch(v.Signature + "\n" + v.RawText)
		if m == nil || !c.typeExists(m[1]) {
			continue
		}
		if found != "" && found != m[1] {
			return ""
		}
		found = m[1]
	}
	return found
}

var pyModuleCtorRe = regexp.MustCompile(`=\s*(?:[A-Za-z_]\w*\.)?([A-Z]\w*)\(`)

func isLowerIdentStart(s string) bool {
	return s != "" && s[0] >= 'a' && s[0] <= 'z'
}

func ownerNames(a *memberAnchor) string {
	var names []string
	for _, d := range a.decls {
		if d.ParentSymbol != "" {
			names = append(names, d.ParentSymbol)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "the declaration"
	}
	return strings.Join(names, ", ")
}

// classifyOwnerless handles module/package variables and constants.
func (c *memberClassifier) classifyOwnerless(o core.MemberOccurrence, e *core.SymbolRecord) (verdict, string) {
	d := c.a.decls[0]
	sameFile := o.File == d.FilePath
	samePkg := d.Language == "go" && dirOf(o.File) == dirOf(d.FilePath)
	switch o.Form {
	case "bare":
		if c.shadowedAt(e, o) {
			return vExclude, ""
		}
		switch {
		case sameFile:
			return vConfirm, "same scope"
		case samePkg:
			return vConfirm, "same package"
		case c.imports[o.File]:
			return vConfirm, "imported from " + d.FilePath
		}
		switch d.Language {
		case "go", "python", "typescript", "tsx", "javascript", "rust", "php":
			return vExclude, "" // not visible unqualified outside its module
		case "c", "cpp", "objc":
			if hasStorageModifier(d, "static") {
				return vExclude, "" // internal linkage
			}
			return vAmbiguous, "global referenced from another translation unit"
		}
		return vAmbiguous, "unqualified name outside the declaring scope"
	case "access":
		recv := normalizeReceiver(o.Receiver)
		if e != nil {
			if _, local := c.localTypes(e)[recv]; local {
				return vExclude, "" // a member of some local value, not the module variable
			}
		}
		pkg := c.goPkg
		if d.Language != "go" {
			pkg = strings.TrimSuffix(path.Base(d.FilePath), path.Ext(d.FilePath))
		}
		if recv == pkg && !samePkg {
			return vConfirm, "qualified by " + recv
		}
		if c.typeExists(lastSegment(recv)) {
			return vExclude, ""
		}
		return vAmbiguous, "qualified by " + o.Receiver + ", which is not known to name the declaring module"
	}
	return vExclude, ""
}

func hasStorageModifier(s core.SymbolRecord, m string) bool {
	for _, x := range s.Modifiers {
		if x == m {
			return true
		}
	}
	return strings.HasPrefix(strings.TrimSpace(s.Signature), m+" ") || strings.Contains(s.Signature, " "+m+" ")
}
