package graph

import (
	"regexp"
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Python local type inference from the annotations the language already
// carries: parameter annotations, annotated assignments, class attribute
// annotations, and self.x = Type(...) constructor assignments. Same
// philosophy as the Go version: shallow, declaration-driven, and every
// guess bounded by the harness numbers.
//
// A type recorded as "class:X" means the variable holds the CLASS X itself
// (null_session_class = NullSession), so calling it constructs an X.

var (
	// x: Type = ... (annotated assignment in a body)
	pyAnnAssignRe = regexp.MustCompile(`(?m)^\s*(\w+)\s*:\s*([^=\n]+?)\s*(?:=|$)`)
	// x = Type(...) (constructor call; verified against indexed types)
	pyCtorAssignRe = regexp.MustCompile(`(?m)\b(\w+)\s*=\s*(?:\w+\.)?(_*[A-Z]\w*)\(`)
	// x = self.attr — the local aliases a typed attribute (cls = self.test_client_class)
	pySelfAliasRe = regexp.MustCompile(`(?m)^\s*(\w+)\s*=\s*self\.(\w+)\s*$`)
	// self.x = Type(...) inside __init__
	pySelfCtorRe = regexp.MustCompile(`self\.(\w+)\s*=\s*(?:\w+\.)?([A-Z]\w*)\(`)
	// self.x: Type = ... inside __init__
	pySelfAnnRe = regexp.MustCompile(`(?m)self\.(\w+)\s*:\s*([^=\n]+?)\s*(?:=|$)`)
	// class-body attribute annotation: "name: Type" / "name: Type = default"
	pyClassAnnRe = regexp.MustCompile(`(?m)^\s+(\w+)\s*:\s*([^=\n]+?)\s*(?:=|$)`)
	// class-body attribute holding a class reference: "name = SomeClass"
	pyClassRefRe = regexp.MustCompile(`(?m)^\s+(\w+)\s*=\s*(?:\w+\.)?([A-Z]\w*)\s*$`)
	// x = [await] [self.|mod.]func(...) — typed through func's return
	// annotation (ctx = self.app_context() → AppContext)
	pyCallAssignRe = regexp.MustCompile(`(?m)^\s*(\w+)\s*=\s*(?:await\s+)?(?:((?:\w+\.)*\w+)\.)?([a-z_]\w*)\(`)
	// with [async] item[, item]: — parenthesized items may span lines, and
	// the suite may begin on the same line after the colon.
	pyWithRe = regexp.MustCompile(`(?m)^[ \t]*(async\s+)?with\s+((?:\([^)]*\)|[^:\n]+)):[^\n]*$`)
	// Read subscripts only; an assignment target invokes __setitem__, not
	// __getitem__. The optional third group lets the consumer reject it.
	pySubscriptRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*\[[^]\n]*\](\s*=)?`)
)

// pyBareType reduces a Python annotation to one indexable class name.
// Conservative: containers and unions other than Optional return "".
func pyBareType(ann string) string {
	ann = strings.TrimSpace(strings.Trim(strings.TrimSpace(ann), `"'`))
	// X | None / None | X
	if parts := strings.Split(ann, "|"); len(parts) == 2 {
		a, b := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if a == "None" {
			ann = b
		} else if b == "None" {
			ann = a
		}
	}
	// Optional[X] / t.Optional[X]
	for _, prefix := range []string{"Optional[", "t.Optional[", "typing.Optional["} {
		if strings.HasPrefix(ann, prefix) && strings.HasSuffix(ann, "]") {
			return pyBareType(ann[len(prefix) : len(ann)-1])
		}
	}
	// type[X] / Type[X] — holds the class itself
	for _, prefix := range []string{"type[", "Type[", "t.Type[", "typing.Type["} {
		if strings.HasPrefix(ann, prefix) && strings.HasSuffix(ann, "]") {
			if inner := pyBareType(ann[len(prefix) : len(ann)-1]); inner != "" {
				return "class:" + inner
			}
			return ""
		}
	}
	// Keep the nominal outer class of a generic receiver. Strip type
	// arguments before qualification: ParamType[t.Any] is ParamType.
	if i := strings.IndexByte(ann, '['); i >= 0 {
		if !strings.HasSuffix(ann, "]") || len(splitTopLevel(ann, '|')) != 1 {
			return ""
		}
		outer := strings.TrimSpace(ann[:i])
		switch leaf := outer[strings.LastIndexByte(outer, '.')+1:]; leaf {
		case "Union", "Literal", "Annotated", "Callable", "List", "Dict", "Tuple", "Set", "Sequence", "Mapping", "Iterable", "Iterator":
			return ""
		}
		ann = outer
	}
	if i := strings.LastIndexByte(ann, '.'); i >= 0 {
		ann = ann[i+1:]
	}
	if ann == "" || strings.ContainsAny(ann, "[]() ,|") {
		return ""
	}
	// Uppercase-first by convention: "int", "str", "bool" carry no
	// narrowing value for our graph.
	if ann[0] < 'A' || ann[0] > 'Z' {
		return ""
	}
	return ann
}

func pyAnnotationType(idx *edgeIndex, symbol *core.SymbolRecord, ann string) string {
	typ := pyBareType(ann)
	prefix := ""
	if strings.HasPrefix(typ, "class:") {
		prefix, typ = "class:", strings.TrimPrefix(typ, "class:")
	}
	if _, member, ok, _ := idx.pyImportBinding(symbol, typ); ok && member != "" {
		typ = member
	}
	if typ == "" {
		return ""
	}
	return prefix + typ
}

// pyDefParams extracts the parameter list of a def from its raw text,
// scanning balanced parens (signatures are routinely multi-line, so the
// stored first-line Signature is unusable). The scan is QUOTE-AWARE:
// brackets inside string defaults (`prompt="(y/n"`) must not count, or the
// list never closes and every downstream param-derived guard silently
// disables itself.
func pyDefParams(rawText string) string {
	i := strings.Index(rawText, "def ")
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(rawText[i:], '(')
	if j < 0 {
		return ""
	}
	start := i + j
	depth := 0
	var quote byte
	for k := start; k < len(rawText); k++ {
		c := rawText[k]
		if quote != 0 {
			if c == '\\' {
				k++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return rawText[start+1 : k]
			}
		}
	}
	return ""
}

// pyParamNames extracts every parameter name from a def's signature,
// annotated or not — including callables like "loads: t.Callable = json.loads"
// that pyLocalTypes' annotation-only pass drops (Callable isn't a bare
// indexable type). A bare call through one of these names invokes whatever
// was actually passed in, not any particular symbol in scope: resolving it
// by matching the parameter's name against an unrelated same-named function
// elsewhere in the file produces a confident-looking but fabricated edge
// (observed: Config.from_prefixed_env's local `loads(value)` call resolving
// to TaggedJSONSerializer.loads by bare name collision). Callers use this to
// suppress resolution for qualifier-less calls to a shadowed parameter name.
func pyParamNames(rawText string) map[string]bool {
	out := map[string]bool{}
	params := pyDefParams(rawText)
	if params == "" {
		return out
	}
	for _, g := range pySplitParams(params) {
		g = strings.TrimSpace(g)
		g = strings.TrimLeft(g, "*")
		if g == "" {
			continue
		}
		name := g
		if i := strings.IndexAny(g, ":="); i >= 0 {
			name = g[:i]
		}
		name = strings.TrimSpace(name)
		// Only accept fragments whose leading token is a plain identifier —
		// a garbled fragment (unparseable default) must not mint a phantom
		// param that would suppress a legitimate call edge.
		if name != "" && name != "self" && name != "cls" && pyIdentRe.MatchString(name) {
			out[name] = true
		}
	}
	return out
}

var pyIdentRe = regexp.MustCompile(`^[A-Za-z_]\w*$`)

// pyUnnestedMatches returns regexp matches that begin outside parentheses,
// brackets, and braces. Python permits an annotation statement at any
// indentation, but a visually identical "name: Type" inside a dict literal is
// a key/value pair, not a declaration. Input has already had strings and
// comments blanked, so bracket depth is sufficient to distinguish the two.
func pyUnnestedMatches(re *regexp.Regexp, src string) [][]string {
	indices := re.FindAllStringSubmatchIndex(src, -1)
	out := make([][]string, 0, len(indices))
	depth, cursor := 0, 0
	for _, loc := range indices {
		for cursor < loc[0] {
			switch src[cursor] {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				if depth > 0 {
					depth--
				}
			}
			cursor++
		}
		if depth != 0 {
			continue
		}
		match := make([]string, len(loc)/2)
		for i := 0; i < len(loc); i += 2 {
			if loc[i] >= 0 {
				match[i/2] = src[loc[i]:loc[i+1]]
			}
		}
		out = append(out, match)
	}
	return out
}

// pySplitParams splits a def's parameter list at top-level commas, aware of
// brackets, quotes, AND lambda defaults. splitTopLevel is quote-blind and
// lambda-blind: `key=lambda a, b: cmp(a, b)` splits at the lambda's own
// comma, and the fragment ` b: cmp(...)` minted a phantom param `b` that
// wrongly suppressed calls to any real function named b (same failure via
// comma-bearing string defaults, `names="a, b"`). Inside a lambda's
// parameter list (from the `lambda` keyword to its top-level `:`), commas do
// not split; inside quotes nothing splits or nests.
func pySplitParams(s string) []string {
	var parts []string
	depth, start := 0, 0
	var quote byte
	inLambdaParams := false
	for k := 0; k < len(s); k++ {
		c := s[k]
		if quote != 0 {
			if c == '\\' {
				k++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ':':
			if depth == 0 && inLambdaParams {
				inLambdaParams = false
			}
		case ',':
			if depth == 0 && !inLambdaParams {
				parts = append(parts, s[start:k])
				start = k + 1
			}
		default:
			if c == 'l' && depth >= 0 && strings.HasPrefix(s[k:], "lambda") {
				before := byte(' ')
				if k > 0 {
					before = s[k-1]
				}
				after := byte(' ')
				if k+6 < len(s) {
					after = s[k+6]
				}
				if !isWordByte(before) && !isWordByte(after) {
					inLambdaParams = true
					k += 5
				}
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// pySetattrAssignRe matches attribute-assignment statements of the forms
// "qualifier.attr = value" and "module.qualifier.attr = value". The trailing
// [^=] excludes comparisons (==); requiring "=" immediately after the
// attribute name excludes augmented assignment (+=, -=, ...) and <=/>=/!=.
// The LEADING [^.\w] (or start) anchors the chain head: with the old \b,
// `resp.headers.foo = x` matched with qualifier `headers`, which was then
// looked up as a bare NAME — any module global named `headers`/`session`
// fabricated an implicit-dunder edge. Now the optional head group captures
// the chain's first segment; the consumer accepts a two-segment chain ONLY
// when the head is an imported module (flask.g.foo = x), never a variable.
var pySetattrAssignRe = regexp.MustCompile(`(?:^|[^.\w])(?:([A-Za-z_]\w*)\.)?([A-Za-z_]\w*)\.[A-Za-z_]\w*\s*=(?:[^=]|$)`)

// pyLocalRebindRe finds plain local rebindings ("name = expr" at statement
// start) — used to detect names that shadow a module global inside a body.
var pyLocalRebindRe = regexp.MustCompile(`(?m)^\s*([A-Za-z_]\w*)\s*=[^=]`)

// pySetattrTargets resolves attribute-assignment statements in symbol's body
// ("g.foo = value") to the __setattr__ of the assigned-to class, for classes
// that declare a CUSTOM __setattr__. Plain attribute assignment has no call
// syntax — there is no "g.__setattr__(...)" site for astkit to extract — so
// without this, the calls graph (and everything built on it: change-impact,
// dead-code) is blind to any class that overrides
// attribute assignment. Flask's _AppCtxGlobals is exactly this shape:
// __setattr__ stores into __dict__, is genuinely covered by 11 real tests,
// and every one of them assigns through a bare "g.attr = value" — grove
// previously flagged it a coverage gap for lack of any resolvable edge.
func pySetattrTargets(idx *edgeIndex, symbol *core.SymbolRecord, localTypes map[string]string, selfVars map[string]struct{}) []*core.SymbolRecord {
	if symbol.RawText == "" {
		return nil
	}
	body := stripCommentsAndStrings(symbol.RawText)
	// Shadowing guard for the module-global fallback: a parameter or a plain
	// local rebinding with the same name as a module global refers to the
	// LOCAL value, not the global — resolving it through the global's type
	// would fabricate an edge (the exact failure class the bare-call
	// param-shadowing fix removed). Positive local typing (selfVars /
	// localTypes) still wins above; this guard only blocks the fallback.
	shadowed := pyParamNames(symbol.RawText)
	for _, m := range pyLocalRebindRe.FindAllStringSubmatch(body, -1) {
		shadowed[m[1]] = true
	}
	imports := idx.fileImports[symbol.FilePath]
	isImportedModule := func(head string) bool {
		if _, ok := imports[head]; ok {
			return true
		}
		for imp := range imports {
			if strings.HasSuffix(imp, "."+head) || strings.HasSuffix(imp, "/"+head) {
				return true
			}
		}
		return false
	}
	seenQual := map[string]bool{}
	seenTarget := map[string]bool{}
	preferDir := dirOf(symbol.FilePath)
	var out []*core.SymbolRecord
	for _, m := range pySetattrAssignRe.FindAllStringSubmatch(body, -1) {
		head, qual := m[1], m[2]
		key := head + "." + qual
		if seenQual[key] {
			continue
		}
		seenQual[key] = true
		var className string
		if head != "" {
			// Two-segment chain: accept only module-qualified global access
			// (`flask.g.foo = x` with `import flask`). A variable head
			// (`resp.headers.foo = x`) stays unresolved — the intermediate
			// attribute's type is unknowable here.
			if t, ok := idx.pyModuleGlobals[qual]; ok && isImportedModule(head) {
				className = t
			}
		} else if _, isSelf := selfVars[qual]; isSelf {
			className = symbol.ParentSymbol
		} else if t, ok := localTypes[qual]; ok {
			className = strings.TrimPrefix(t, "class:")
		} else if t, ok := idx.pyModuleGlobals[qual]; ok && !shadowed[qual] {
			// Assignment through a module-level global (Flask's `g`):
			// resolve the global's declared type, then walk its base
			// classes below — the declared type is often a proxy stub
			// (`_AppCtxGlobalsProxy`) that inherits the class carrying the
			// real __setattr__.
			className = t
		}
		if className == "" {
			continue
		}
		for _, cand := range pyDunderTargets(idx, className, "__setattr__", preferDir) {
			if !seenTarget[cand.ID] {
				seenTarget[cand.ID] = true
				out = append(out, cand)
			}
		}
	}
	return out
}

// pyModuleGlobalType extracts the class name from a module-global variable's
// annotation signature ("g: _AppCtxGlobalsProxy" → "_AppCtxGlobalsProxy").
// Unlike pyBareType it deliberately KEEPS underscore-leading names — a proxy
// stub class like _AppCtxGlobalsProxy is exactly the case that matters — and
// rejects only builtins, containers, and multi-token annotations.
func pyModuleGlobalType(sig string) string {
	_, ann, ok := strings.Cut(sig, ":")
	if !ok {
		return ""
	}
	ann = strings.TrimSpace(ann)
	if parts := strings.Split(ann, "|"); len(parts) == 2 {
		a, b := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if a == "None" {
			ann = b
		} else if b == "None" {
			ann = a
		}
	}
	for _, prefix := range []string{"Optional[", "t.Optional[", "typing.Optional["} {
		if strings.HasPrefix(ann, prefix) && strings.HasSuffix(ann, "]") {
			ann = strings.TrimSpace(ann[len(prefix) : len(ann)-1])
		}
	}
	if i := strings.LastIndexByte(ann, '.'); i >= 0 {
		ann = ann[i+1:]
	}
	if ann == "" || strings.ContainsAny(ann, "[]() ,|\"'") {
		return ""
	}
	switch ann {
	case "int", "str", "bool", "float", "bytes", "complex", "None",
		"Any", "object", "dict", "list", "tuple", "set", "frozenset":
		return ""
	}
	return ann
}

// pyDunderTargets finds the method named dunder ("__setattr__") that a call
// through an instance of className would invoke: className's own declaration
// if it has one, else the nearest ancestor's (Python MRO — the closest
// definition wins). Walks up to five levels of base classes via pyBaseClasses,
// which parses bases from class signatures (so proxy stubs that only exist for
// type-checking still contribute their inheritance link once indexed).
func pyDunderTargets(idx *edgeIndex, className, dunder, preferDir string) []*core.SymbolRecord {
	for _, cn := range pyMRO(idx, className, preferDir) {
		var found []*core.SymbolRecord
		// Pin the class NAME to one class symbol (preferDir-preferred,
		// same choice rule as pyBaseClasses) and accept only the dunder
		// declared in THAT class's file.
		cls := pyResolveClass(idx, cn, preferDir)
		if cls == nil {
			continue
		}
		for _, cand := range idx.byName[strings.ToLower(dunder)] {
			if cand.Name == dunder && cand.ParentSymbol == cn && cand.FilePath == cls.FilePath {
				found = append(found, cand)
			}
		}
		if len(found) > 0 {
			return found // first definition in Python's C3 MRO wins
		}
	}
	return nil
}

func pyMRO(idx *edgeIndex, className, preferDir string) []string {
	return pyLinearize(idx, className, preferDir, map[string]bool{})
}

func pyLinearize(idx *edgeIndex, className, preferDir string, visiting map[string]bool) []string {
	if className == "" || visiting[className] {
		return nil
	}
	visiting[className] = true
	defer delete(visiting, className)
	bases := pyBaseClasses(idx, className, preferDir)
	seqs := make([][]string, 0, len(bases)+1)
	for _, base := range bases {
		seqs = append(seqs, pyLinearize(idx, base, preferDir, visiting))
	}
	seqs = append(seqs, append([]string(nil), bases...))
	out := []string{className}
	seen := map[string]bool{className: true}
	for {
		var nonEmpty bool
		candidate := ""
		for _, seq := range seqs {
			if len(seq) == 0 {
				continue
			}
			nonEmpty = true
			head := seq[0]
			inTail := false
			for _, other := range seqs {
				if len(other) < 2 {
					continue
				}
				for _, tail := range other[1:] {
					if tail == head {
						inTail = true
						break
					}
				}
				if inTail {
					break
				}
			}
			if !inTail {
				candidate = head
				break
			}
		}
		if !nonEmpty {
			break
		}
		if candidate == "" {
			// Invalid/inconsistent hierarchy: retain deterministic coverage
			// rather than looping or dropping all ancestors.
			for _, seq := range seqs {
				if len(seq) > 0 {
					candidate = seq[0]
					break
				}
			}
		}
		if !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
		for i, seq := range seqs {
			if len(seq) > 0 && seq[0] == candidate {
				seqs[i] = seq[1:]
			}
		}
	}
	return out
}

// pyResolveClass picks the single class symbol a bare class name refers to,
// preferring a declaration in preferDir (the caller's package) over
// same-named classes elsewhere — the same choice rule pyBaseClasses applies.
func pyResolveClass(idx *edgeIndex, className, preferDir string) *core.SymbolRecord {
	var chosen *core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(className)] {
		if cand.Name != className || (cand.Kind != core.KindClass && cand.Kind != core.KindInterface) {
			continue
		}
		if dirOf(cand.FilePath) == preferDir {
			return cand
		}
		if chosen == nil {
			chosen = cand
		}
	}
	return chosen
}

// pyLocalTypes infers identifier → type name for one Python callable.
func pyLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Class attribute types, own class first then ancestors (inherited
	// attributes resolve through the hierarchy; ancestors never overwrite
	// closer definitions). Lowest precedence overall.
	if symbol.Kind == core.KindMethod && symbol.ParentSymbol != "" {
		seen := map[string]bool{}
		classes := []string{symbol.ParentSymbol}
		for level := 0; level < 4 && len(classes) > 0; level++ {
			var next []string
			for _, className := range classes {
				if seen[className] {
					continue
				}
				seen[className] = true
				pyClassAttrTypes(idx, symbol, className, out)
				next = append(next, pyBaseClasses(idx, className, dirOf(symbol.FilePath))...)
			}
			classes = next
		}
		// Class attributes are keyed by bare name because call sites keep
		// only the last receiver segment ("self.cli.foo()" arrives as
		// "cli.foo"). A bare name the body never reaches through self/cls
		// is a module, parameter, or local instead (Flask.run's `cli.
		// show_server_banner()` is the imported cli module, not the
		// AppGroup in self.cli) — drop the attribute reading for it.
		if symbol.RawText != "" {
			for name := range out {
				if !strings.Contains(symbol.RawText, "self."+name) && !strings.Contains(symbol.RawText, "cls."+name) {
					delete(out, name)
				}
			}
		}
	}

	// Parameter annotations.
	if params := pyDefParams(symbol.RawText); params != "" {
		for _, g := range splitTopLevel(params, ',') {
			g = strings.TrimSpace(g)
			name, ann, ok := strings.Cut(g, ":")
			if !ok {
				continue
			}
			name = strings.TrimSpace(strings.TrimLeft(name, "*"))
			if eq := strings.Index(ann, "="); eq >= 0 {
				ann = ann[:eq]
			}
			if t := pyAnnotationType(idx, symbol, ann); t != "" && name != "" {
				out[name] = t
			}
		}
	}

	// Body declarations (highest precedence).
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range pyUnnestedMatches(pyAnnAssignRe, body) {
			if t := pyAnnotationType(idx, symbol, m[2]); t != "" {
				out[m[1]] = t
			}
		}
		for _, m := range pySelfAnnRe.FindAllStringSubmatch(body, -1) {
			if t := pyAnnotationType(idx, symbol, m[2]); t != "" {
				out[m[1]] = t
			}
		}
		for _, m := range pyCtorAssignRe.FindAllStringSubmatch(body, -1) {
			if typeSymbolExists(idx, m[2]) {
				out[m[1]] = m[2]
			}
		}
		selfVars := callerSelfQualifiers(symbol)
		for _, m := range pyWithRe.FindAllStringSubmatch(body, -1) {
			items := strings.TrimSpace(m[2])
			if strings.HasPrefix(items, "(") && strings.HasSuffix(items, ")") {
				items = strings.TrimSpace(items[1 : len(items)-1])
			}
			for _, item := range splitTopLevel(items, ',') {
				expr, bound, ok := strings.Cut(strings.TrimSpace(item), " as ")
				bound = strings.TrimSpace(bound)
				if !ok || bound == "" || strings.ContainsAny(bound, " .()[]") {
					continue
				}
				className := pyExprType(idx, strings.TrimPrefix(strings.TrimSpace(expr), "await "), symbol, out, selfVars)
				if className != "" {
					out[bound] = pyContextValueType(idx, className, m[1] != "", symbol)
				}
			}
		}
		aliased := map[string]bool{}
		for _, m := range pySelfAliasRe.FindAllStringSubmatch(body, -1) {
			if t, ok := out[m[2]]; ok && m[1] != m[2] {
				out[m[1]] = t
				aliased[m[1]] = true
			}
		}
		for _, m := range pyCallAssignRe.FindAllStringSubmatch(body, -1) {
			if _, done := out[m[1]]; done {
				continue // annotation / constructor evidence wins
			}
			if q := m[2]; q != "" && q != "self" && q != "cls" && !strings.HasPrefix(q, "self.") && !strings.HasPrefix(q, "cls.") {
				// ctx = _cv_app.get(None): the receiver is a module-level
				// ContextVar, not anything indexed — its `get` is not the
				// repo's `get`. Only an own-object chain, a typed local, or
				// a bare call is typed.
				if _, typed := out[q]; !typed {
					continue
				}
			}
			if t := pyCallResultType(idx, m[3], symbol); t != "" {
				out[m[1]] = t
			}
		}
		// A body-assigned `cls` (cls = self.test_client_class) is a
		// local holding a class, not the classmethod parameter.
		if !aliased["cls"] {
			delete(out, "cls")
		}
	} else {
		delete(out, "cls")
	}
	// Imported module globals with a declared class type (flask's
	// `current_app: FlaskProxy`, imported as `from .globals import
	// current_app`), unless a parameter or local rebinding shadows the
	// name. Lowest precedence: nothing above is overwritten.
	if idx != nil && len(idx.pyModuleGlobals) > 0 {
		shadowed := pyParamNames(symbol.RawText)
		if symbol.RawText != "" {
			for _, m := range pyLocalRebindRe.FindAllStringSubmatch(stripCommentsAndStrings(symbol.RawText), -1) {
				shadowed[m[1]] = true
			}
		}
		for _, imp := range symbol.Imports {
			_, name, ok := strings.Cut(imp, "#")
			if !ok || shadowed[name] {
				continue
			}
			if _, done := out[name]; done {
				continue
			}
			if t, ok := idx.pyModuleGlobals[name]; ok {
				out[name] = t
			}
		}
	}
	delete(out, "self")
	delete(out, "_")
	return out
}

// pyClassAttrTypes collects attribute types declared on one class — body
// annotations, class-attribute class references, and self.x assignments in
// its __init__ — without overwriting names already inferred.
func pyClassAttrTypes(idx *edgeIndex, symbol *core.SymbolRecord, className string, out map[string]string) {
	record := func(name, typ string) {
		if _, exists := out[name]; !exists && typ != "" {
			out[name] = typ
		}
	}
	preferDir := dirOf(symbol.FilePath)
	var class *core.SymbolRecord
	for _, cls := range idx.byName[strings.ToLower(className)] {
		if cls.Name != className || cls.Kind != core.KindClass || cls.RawText == "" {
			continue
		}
		if class == nil || dirOf(cls.FilePath) == preferDir {
			class = cls
		}
		if dirOf(cls.FilePath) == preferDir {
			break
		}
	}
	if class != nil {
		for _, m := range pyClassAnnRe.FindAllStringSubmatch(class.RawText, -1) {
			record(m[1], pyAnnotationType(idx, class, m[2]))
		}
		for _, m := range pyClassRefRe.FindAllStringSubmatch(class.RawText, -1) {
			if typeSymbolExists(idx, m[2]) {
				record(m[1], "class:"+m[2])
			}
		}
	}
	var init *core.SymbolRecord
	for _, cand := range idx.byName["__init__"] {
		if cand.ParentSymbol != className {
			continue
		}
		// ParentSymbol is only a display name, so same-named classes in other
		// modules can also have an __init__ candidate. Attribute inference must
		// stay attached to the concrete class declaration selected above.
		if class != nil && cand.FilePath != class.FilePath {
			continue
		}
		if init == nil {
			init = cand
		}
	}
	if init != nil {
		body := stripCommentsAndStrings(init.RawText)
		for _, m := range pySelfAnnRe.FindAllStringSubmatch(body, -1) {
			record(m[1], pyAnnotationType(idx, init, m[2]))
		}
		for _, m := range pySelfCtorRe.FindAllStringSubmatch(body, -1) {
			if typeSymbolExists(idx, m[2]) {
				record(m[1], m[2])
			}
		}
	}
}

// pyBaseClasses parses the base-class names from a class declaration
// signature: "class Blueprint(Scaffold):" → [Scaffold]. Keyword arguments
// (metaclass=...) and subscripted bases (Generic[T]) are skipped.
func pyBaseClasses(idx *edgeIndex, className, preferDir string) []string {
	var chosen *core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(className)] {
		if cand.Name != className || (cand.Kind != core.KindClass && cand.Kind != core.KindInterface) {
			continue
		}
		if dirOf(cand.FilePath) == preferDir {
			chosen = cand
			break
		}
		if chosen == nil {
			chosen = cand
		}
	}
	if chosen != nil {
		sig := chosen.Signature
		open := strings.IndexByte(sig, '(')
		closeIdx := strings.LastIndexByte(sig, ')')
		if open < 0 || closeIdx <= open {
			return nil
		}
		var bases []string
		for _, b := range splitTopLevel(sig[open+1:closeIdx], ',') {
			b = strings.TrimSpace(b)
			if b == "" || strings.Contains(b, "=") {
				continue
			}
			if i := strings.IndexByte(b, '['); i >= 0 {
				b = b[:i]
			}
			if i := strings.LastIndexByte(b, '.'); i >= 0 {
				b = b[i+1:]
			}
			if _, member, ok, _ := idx.pyImportBinding(chosen, b); ok && member != "" {
				b = member
			}
			if b != "" && b != "object" {
				bases = append(bases, b)
			}
		}
		return bases
	}
	return nil
}

// inheritedTargets resolves self.method() / self.property to members of the
// caller's ancestor classes, regardless of file import scope: inheritance
// reaches through files the subclass module never imports directly
// (app.py's Flask never imports scaffold.py, yet self.has_static_folder
// lives there). propertyOnly restricts to property-annotated methods for
// attribute reads.
func inheritedTargets(idx *edgeIndex, symbol *core.SymbolRecord, calleeName string, propertyOnly bool) []*core.SymbolRecord {
	if symbol.ParentSymbol == "" {
		return nil
	}
	var all []*core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(calleeName)] {
		if cand.Name != calleeName || cand.Kind != core.KindMethod {
			continue
		}
		if propertyOnly && !hasPropertyAnnotation(cand) {
			continue
		}
		all = append(all, cand)
	}
	if len(all) == 0 {
		return nil
	}
	bases := baseClassesFor(idx, symbol.Language, symbol.ParentSymbol, dirOf(symbol.FilePath))
	for level := 0; level < 4 && len(bases) > 0; level++ {
		var matched []*core.SymbolRecord
		for _, base := range bases {
			matched = append(matched, filterByParent(all, base)...)
		}
		if len(matched) > 0 {
			return matched
		}
		var next []string
		for _, base := range bases {
			next = append(next, baseClassesFor(idx, symbol.Language, base, dirOf(symbol.FilePath))...)
		}
		bases = next
	}
	return nil
}

// narrowBySuper resolves super().method() candidates to methods on the
// caller's base classes (walking up to three levels of the hierarchy).
func narrowBySuper(idx *edgeIndex, symbol *core.SymbolRecord, cands []*core.SymbolRecord) []*core.SymbolRecord {
	if symbol.ParentSymbol == "" || len(cands) == 0 {
		return nil
	}
	if symbol.Language == "python" {
		if matched := pyImportedDirectBaseTargets(idx, symbol, cands); len(matched) > 0 {
			return matched
		}
		mro := pyMRO(idx, symbol.ParentSymbol, dirOf(symbol.FilePath))
		for _, base := range mro[1:] {
			if matched := filterByParent(cands, base); len(matched) > 0 {
				return matched
			}
		}
		return nil
	}
	bases := baseClassesFor(idx, symbol.Language, symbol.ParentSymbol, dirOf(symbol.FilePath))
	for level := 0; level < 3 && len(bases) > 0; level++ {
		var matched []*core.SymbolRecord
		for _, base := range bases {
			matched = append(matched, filterByParent(cands, base)...)
		}
		if len(matched) > 0 {
			return matched
		}
		var next []string
		for _, base := range bases {
			next = append(next, baseClassesFor(idx, symbol.Language, base, dirOf(symbol.FilePath))...)
		}
		bases = next
	}
	return nil
}

// pyImportedDirectBaseTargets preserves the file identity behind an aliased
// Python base (`class Blueprint(SansioBlueprint)`). Reducing the binding to its
// leaf name alone is ambiguous when both subclass and base are called
// Blueprint, and makes super() walk back into the subclass.
func pyImportedDirectBaseTargets(idx *edgeIndex, symbol *core.SymbolRecord, cands []*core.SymbolRecord) []*core.SymbolRecord {
	var class *core.SymbolRecord
	for _, candidate := range idx.byFile[symbol.FilePath] {
		if candidate.Name == symbol.ParentSymbol && candidate.Kind == core.KindClass {
			class = candidate
			break
		}
	}
	if class == nil {
		return nil
	}
	open := strings.IndexByte(class.Signature, '(')
	closeIdx := strings.LastIndexByte(class.Signature, ')')
	if open < 0 || closeIdx <= open {
		return nil
	}
	seen := map[string]bool{}
	var out []*core.SymbolRecord
	for _, rawBase := range splitTopLevel(class.Signature[open+1:closeIdx], ',') {
		base := strings.TrimSpace(rawBase)
		if base == "" || strings.Contains(base, "=") {
			continue
		}
		if i := strings.IndexByte(base, '['); i >= 0 {
			base = base[:i]
		}
		module, member, ok, _ := idx.pyImportBinding(class, base)
		if !ok || member == "" {
			continue
		}
		files := map[string]bool{}
		for _, file := range idx.pyModuleFiles(class.FilePath, module) {
			files[file] = true
		}
		for _, candidate := range cands {
			if candidate.ParentSymbol == member && files[candidate.FilePath] && !seen[candidate.ID] {
				seen[candidate.ID] = true
				out = append(out, candidate)
			}
		}
	}
	return out
}

// pyReturnType reads a def's "-> Ann" return annotation from its raw text
// (multi-line signatures make the first-line Signature unusable) and
// reduces it to one indexable class name, or "".
func pyReturnType(s *core.SymbolRecord) string {
	raw := s.RawText
	i := strings.Index(raw, "def ")
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(raw[i:], '(')
	if j < 0 {
		return ""
	}
	depth := 0
	var quote byte
	end := -1
	for k := i + j; k < len(raw); k++ {
		c := raw[k]
		if quote != 0 {
			if c == '\\' {
				k++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				end = k
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return ""
	}
	rest := raw[end+1:]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	head := rest[:colon]
	arrow := strings.Index(head, "->")
	if arrow < 0 {
		return ""
	}
	return pyBareType(head[arrow+2:])
}

// pyCallResultType types the result of calling `name` from symbol's body:
// an indexed class name constructs that class; a function or method with a
// return annotation yields it when every in-scope declaration agrees. A
// method on the caller's own class (or its ancestors) wins over same-named
// functions elsewhere — self.app_context() is the own-class one.
func pyCallResultType(idx *edgeIndex, name string, symbol *core.SymbolRecord) string {
	if name == "" {
		return ""
	}
	if bare := strings.TrimLeft(name, "_"); bare != "" && bare[0] >= 'A' && bare[0] <= 'Z' && typeSymbolExists(idx, name) {
		return name
	}
	scope := idx.importedFiles(symbol.FilePath)
	own := map[string]bool{}
	if symbol.ParentSymbol != "" {
		own[symbol.ParentSymbol] = true
		classes := []string{symbol.ParentSymbol}
		for level := 0; level < 4 && len(classes) > 0; level++ {
			var next []string
			for _, c := range classes {
				for _, b := range pyBaseClasses(idx, c, dirOf(symbol.FilePath)) {
					if !own[b] {
						own[b] = true
						next = append(next, b)
					}
				}
			}
			classes = next
		}
	}
	ret, ownRet := "", ""
	agree, ownAgree := true, true
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.Name != name || (cand.Kind != core.KindFunction && cand.Kind != core.KindMethod) {
			continue
		}
		if _, ok := scope[cand.FilePath]; !ok && !own[cand.ParentSymbol] {
			continue
		}
		r := pyAnnotationType(idx, cand, pyReturnType(cand))
		if own[cand.ParentSymbol] {
			if r == "" {
				ownAgree = false
			} else if ownRet == "" {
				ownRet = r
			} else if ownRet != r {
				ownAgree = false
			}
			continue
		}
		if r == "" {
			agree = false
		} else if ret == "" {
			ret = r
		} else if ret != r {
			agree = false
		}
	}
	if ownRet != "" && ownAgree {
		return ownRet
	}
	if ownRet != "" || !ownAgree {
		return "" // own-class method exists but is untyped/ambiguous
	}
	if agree {
		return ret
	}
	return ""
}

// pyWithTargets resolves `with expr [as x]:` items in symbol's body to the
// __enter__/__exit__ (async: __aenter__/__aexit__) methods of the item's
// class. Like attribute assignment, the protocol calls have no call syntax
// for astkit to extract; the item's type comes from a constructor call
// (with Lock():), a typed local or self attribute (with self._lock:), or a
// call whose return annotation names the class (with self.app_context():).
func pyWithTargets(idx *edgeIndex, symbol *core.SymbolRecord, localTypes map[string]string, selfVars map[string]struct{}) []*core.SymbolRecord {
	if symbol.RawText == "" {
		return nil
	}
	body := stripCommentsAndStrings(symbol.RawText)
	preferDir := dirOf(symbol.FilePath)
	seen := map[string]bool{}
	var out []*core.SymbolRecord
	for _, m := range pyWithRe.FindAllStringSubmatch(body, -1) {
		enter, exit := "__enter__", "__exit__"
		if m[1] != "" {
			enter, exit = "__aenter__", "__aexit__"
		}
		items := strings.TrimSpace(m[2])
		if strings.HasPrefix(items, "(") && strings.HasSuffix(items, ")") {
			items = strings.TrimSpace(items[1 : len(items)-1])
		}
		for _, item := range splitTopLevel(items, ',') {
			item = strings.TrimSpace(item)
			if i := strings.Index(item, " as "); i >= 0 {
				item = strings.TrimSpace(item[:i])
			}
			item = strings.TrimPrefix(item, "await ")
			className := pyExprType(idx, item, symbol, localTypes, selfVars)
			if className == "" {
				continue
			}
			for _, d := range []string{enter, exit} {
				for _, cand := range pyDunderTargets(idx, className, d, preferDir) {
					if !seen[cand.ID] {
						seen[cand.ID] = true
						out = append(out, cand)
					}
				}
			}
		}
	}
	return out
}

func pyContextValueType(idx *edgeIndex, className string, async bool, symbol *core.SymbolRecord) string {
	dunder := "__enter__"
	if async {
		dunder = "__aenter__"
	}
	for _, method := range pyDunderTargets(idx, className, dunder, dirOf(symbol.FilePath)) {
		ret := pyReturnType(method)
		if ret == "Self" || ret == "typing.Self" {
			return className
		}
		if typ := pyAnnotationType(idx, method, ret); typ != "" {
			return strings.TrimPrefix(typ, "class:")
		}
	}
	return className
}

func pySubscriptTargets(idx *edgeIndex, symbol *core.SymbolRecord, localTypes map[string]string) []*core.SymbolRecord {
	if symbol.RawText == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []*core.SymbolRecord
	for _, match := range pySubscriptRe.FindAllStringSubmatch(stripCommentsAndStrings(symbol.RawText), -1) {
		if match[2] != "" {
			continue
		}
		typ := strings.TrimPrefix(localTypes[match[1]], "class:")
		if typ == "" {
			continue
		}
		for _, candidate := range pyDunderTargets(idx, typ, "__getitem__", dirOf(symbol.FilePath)) {
			if !seen[candidate.ID] {
				seen[candidate.ID] = true
				out = append(out, candidate)
			}
		}
	}
	return out
}

func pyTypeHasMember(idx *edgeIndex, className, member, preferDir string) bool {
	for _, owner := range pyMRO(idx, className, preferDir) {
		cls := pyResolveClass(idx, owner, preferDir)
		if cls == nil {
			continue
		}
		for _, candidate := range idx.byFile[cls.FilePath] {
			if candidate.ParentSymbol == owner && candidate.Name == member {
				return true
			}
		}
	}
	return false
}

// pyExprType types a simple receiver expression: Name(...) constructs Name;
// a call result goes through pyCallResultType; self.attr and bare names go
// through the inferred local/attribute types.
func pyExprType(idx *edgeIndex, expr string, symbol *core.SymbolRecord, localTypes map[string]string, selfVars map[string]struct{}) string {
	if i := strings.IndexByte(expr, '('); i >= 0 {
		callee := expr[:i]
		if j := strings.LastIndexByte(callee, '.'); j >= 0 {
			callee = callee[j+1:]
		}
		callee = strings.TrimSpace(callee)
		if callee == "" || strings.ContainsAny(callee, " \t[]") {
			return ""
		}
		if t, ok := localTypes[callee]; ok && strings.HasPrefix(t, "class:") {
			return strings.TrimPrefix(t, "class:") // with self.lock_class():
		}
		return pyCallResultType(idx, callee, symbol)
	}
	if strings.ContainsAny(expr, " \t[]") {
		return ""
	}
	name := expr
	if j := strings.LastIndexByte(expr, '.'); j >= 0 {
		head := expr[:j]
		name = expr[j+1:]
		if _, isSelf := selfVars[head]; !isSelf {
			return ""
		}
	}
	if t, ok := localTypes[name]; ok {
		return strings.TrimPrefix(t, "class:")
	}
	return ""
}

// subclassOverrides returns the methods named calleeName declared on
// subclasses of className (transitively, four levels) in the caller's
// language — the class-hierarchy dispatch targets of a call that binds
// className's method: self.to_json() inside the base, or $stmt->getType()
// with $stmt typed by the abstract Node.
func subclassOverrides(idx *edgeIndex, language, className, calleeName, preferDir string) []*core.SymbolRecord {
	idx.subclassesOnce.Do(func() {
		idx.subclasses = map[string]map[string][]string{}
		seen := map[string]bool{}
		for _, cands := range idx.byName {
			for _, c := range cands {
				if !classLanguage(c.Language) || seen[c.ID] {
					continue
				}
				switch c.Kind {
				case core.KindClass, core.KindStruct, core.KindInterface:
				default:
					continue
				}
				seen[c.ID] = true
				byLang := idx.subclasses[c.Language]
				if byLang == nil {
					byLang = map[string][]string{}
					idx.subclasses[c.Language] = byLang
				}
				className := c.Name
				if c.Language == "cpp" && strings.Contains(c.QualifiedName, "::") {
					className = c.QualifiedName
				}
				for _, base := range baseClassesFor(idx, c.Language, className, dirOf(c.FilePath)) {
					byLang[base] = append(byLang[base], className)
				}
			}
		}
		for _, byLang := range idx.subclasses {
			for base := range byLang {
				sort.Strings(byLang[base])
			}
		}
	})
	subs := idx.subclasses[language]
	if subs == nil {
		return nil
	}
	var methods []*core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(calleeName)] {
		if cand.Name == calleeName && graphCallableSymbol(cand) && cand.Language == language {
			methods = append(methods, cand)
		}
	}
	if len(methods) == 0 {
		return nil
	}
	var out []*core.SymbolRecord
	visited := map[string]bool{className: true}
	frontier := []string{className}
	for level := 0; level < 4 && len(frontier) > 0; level++ {
		var next []string
		for _, cls := range frontier {
			for _, sub := range subs[cls] {
				if visited[sub] {
					continue
				}
				visited[sub] = true
				next = append(next, sub)
				out = append(out, filterByParent(methods, sub)...)
			}
		}
		frontier = next
	}
	return out
}
