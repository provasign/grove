package graph

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Python function values (flask oracle review 2026-10-17). A function
// reaches a call through a value: a constructor keyword stored on self
// (ConfigAttribute(get_converter=_make_timedelta) ... self.get_converter(rv)),
// a registry on self that a loop iterates and calls
// ({None: [_default_template_ctx_processor]}), a parameter called inside
// the function it is passed to (record_once(func) ... func(state)), a
// function's result (self.ensure_sync(func)()), or a library call it is
// handed to (kwargs.setdefault("default", _default) for json.dumps).
//
// Each symbol records only what its own text shows, as markers joined at
// graph install (funcFieldEdges), so a full and an incremental index agree:
//   - core.EdgeFuncFieldStore: a function stored into a slot;
//   - core.EdgeFuncSlotFlow: a slot that receives another slot's values
//     (self.get_converter = get_converter);
//   - core.EdgeFuncFieldCall: a call through a slot.
// Slots are keyed by declaration, never by blob: a parameter
// ("file:Qual#p:name"), a result ("file:Qual#ret"), an attribute of a class
// ("file:Class#a:attr"). A function handed to a call outside the project
// gets a direct edge: the library may call it, and nothing else will show.

type pyFlowVal struct {
	funcs []*core.SymbolRecord
	slots []string
}

func (v *pyFlowVal) add(o pyFlowVal) {
	v.funcs = append(v.funcs, o.funcs...)
	v.slots = append(v.slots, o.slots...)
}

func (v pyFlowVal) empty() bool { return len(v.funcs) == 0 && len(v.slots) == 0 }

func pySymKey(s *core.SymbolRecord) string { return s.FilePath + ":" + s.QualifiedName }

var (
	pyFlowAssignRe  = regexp.MustCompile(`^(\s*)([A-Za-z_][\w.]*(?:\[[^\]]*\])?)\s*(?::[^=]+)?=\s*(.+)$`)
	pyFlowAppendRe  = regexp.MustCompile(`^\s*(self\.[A-Za-z_]\w*)(?:\[[^\]]*\])*\.(?:append|add|insert|extend|setdefault)\((.*)\)\s*$`)
	pyFlowForRe     = regexp.MustCompile(`^\s*(?:async\s+)?for\s+([A-Za-z_]\w*)\s+in\s+(.+?):\s*$`)
	pyFlowReturnRe  = regexp.MustCompile(`^\s*return\s+(.+)$`)
	pyFlowDefRe     = regexp.MustCompile(`^(\s*)(?:async\s+)?(?:def|class)\s`)
	pyFlowIdentRe   = regexp.MustCompile(`[A-Za-z_][\w]*(?:\.[A-Za-z_]\w*)?`)
	pyFlowWrapperRe = regexp.MustCompile(`^(?:functools\.|t\.|typing\.)?(?:staticmethod|classmethod|update_wrapper|partial|wraps|property|cast)\s*\(`)
)

// pyValueFlowMarkers emits symbol's value-flow markers and its direct
// handoff edges.
func pyValueFlowMarkers(idx *edgeIndex, symbol *core.SymbolRecord) []core.Edge {
	if symbol.Language != "python" || symbol.RawText == "" {
		return nil
	}
	isClass := symbol.Kind == core.KindClass
	if !isClass && symbol.Kind != core.KindFunction && symbol.Kind != core.KindMethod && symbol.Kind != core.KindConstructor {
		return nil
	}
	f := &pyFlow{idx: idx, sym: symbol, locals: map[string]pyFlowVal{}, params: map[string]bool{},
		test: pyIsTestModule(symbol.FilePath) || strings.Contains("/"+symbol.FilePath, "/tests/")}
	if !isClass {
		for _, p := range pyFlowParamNames(symbol) {
			f.params[p] = true
		}
		f.ann = pyFlowParamAnnotations(symbol)
		f.class = pyFlowOwnerClass(idx, symbol)
		if enc := pyEnclosingCallable(idx, symbol); enc != nil {
			// A nested def sees its encloser's parameters and, inside a
			// method, its self's class.
			f.enc = enc
			f.encPar = map[string]bool{}
			for _, p := range pyFlowParamNames(enc) {
				if !f.params[p] && p != "self" && p != "cls" {
					f.encPar[p] = true
				}
			}
			if f.class == nil {
				for e := enc; e != nil && f.class == nil; e = pyEnclosingCallable(idx, e) {
					f.class = pyFlowOwnerClass(idx, e)
				}
			}
		}
	} else {
		f.class = symbol
	}
	f.shadow = pyFlowAssignedNames(symbol.RawText)
	f.scanBody(isClass)
	f.scanCalls(isClass)
	return f.emit()
}

type pyFlow struct {
	ann    map[string]string  // own parameter annotations
	enc    *core.SymbolRecord // enclosing function of a nested def
	encPar map[string]bool    // its parameters (closure variables)
	types  map[string]string  // pyLocalTypes, lazily
	test   bool               // a test module: its stores are left out
	shadow map[string]bool    // names the body binds: never a module function
	idx    *edgeIndex
	sym    *core.SymbolRecord
	class  *core.SymbolRecord // the class whose self this is (or the class itself)
	params map[string]bool
	locals map[string]pyFlowVal
	stores []core.Edge
	links  []string
	calls  []string
	direct []core.Edge
}

// pyFlowOwnerClass is the class a method's self belongs to, nil for a
// module function.
func pyFlowOwnerClass(idx *edgeIndex, s *core.SymbolRecord) *core.SymbolRecord {
	if s.Kind != core.KindMethod && s.Kind != core.KindConstructor || s.ParentSymbol == "" {
		return nil
	}
	for _, c := range idx.byFile[s.FilePath] {
		if c.Kind == core.KindClass && c.Name == s.ParentSymbol && c.Span.Start <= s.Span.Start && c.Span.End >= s.Span.End {
			return c
		}
	}
	return pyResolveClass(idx, s.ParentSymbol, dirOf(s.FilePath))
}

// pyFlowParamNames lists a def's parameter names in order (self/cls
// included; callers drop the receiver).
func pyFlowParamNames(s *core.SymbolRecord) []string {
	params := pyDefParams(s.RawText)
	var out []string
	for _, g := range splitTopLevel(params, ',') {
		g = strings.TrimSpace(g)
		if g == "" || g == "/" || g == "*" {
			continue
		}
		name := g
		if i := strings.IndexAny(name, ":="); i >= 0 {
			name = name[:i]
		}
		out = append(out, strings.TrimSpace(name))
	}
	return out
}

// attrKeys are the attribute slots self.attr can name: the class's and its
// ancestors' (a subclass calls through what a base stored).
func (f *pyFlow) attrKeys(attr string) []string {
	if f.class == nil {
		return nil
	}
	var out []string
	for _, cn := range pyMRO(f.idx, f.class.Name, dirOf(f.class.FilePath)) {
		cls := f.class
		if cn != f.class.Name {
			cls = pyResolveClass(f.idx, cn, dirOf(f.class.FilePath))
		}
		if cls != nil {
			out = append(out, pySymKey(cls)+"#a:"+attr)
		}
	}
	return out
}

// ownKey is the slot an assignment to self.attr writes: the declaring class.
func (f *pyFlow) ownKey(attr string) string {
	if f.class == nil {
		return ""
	}
	return pySymKey(f.class) + "#a:" + attr
}

// method finds a method name on the class's MRO: the class itself by its
// own file, each base by the file pyResolveClass binds it to (two classes
// may share a name: django's model Field and gdal's Field).
func (f *pyFlow) method(name string) *core.SymbolRecord {
	if f.class == nil {
		return nil
	}
	for _, cn := range pyMRO(f.idx, f.class.Name, dirOf(f.class.FilePath)) {
		cls := f.class
		if cn != f.class.Name {
			cls = pyResolveClass(f.idx, cn, dirOf(f.class.FilePath))
		}
		if cls == nil {
			continue
		}
		for _, m := range f.idx.byName[strings.ToLower(name)] {
			if m.Name == name && m.ParentSymbol == cn && m.FilePath == cls.FilePath && m.Language == "python" &&
				(m.Kind == core.KindMethod || m.Kind == core.KindConstructor) {
				return m
			}
		}
	}
	return nil
}

// function resolves a bare name to a project function the caller can see:
// a nested def of its own, else a module-level function.
func (f *pyFlow) function(name string) *core.SymbolRecord {
	if f.sym.Kind != core.KindClass {
		if kids := pythonLexicalChild(f.idx, f.sym, name); len(kids) == 1 {
			return kids[0]
		}
	}
	var found *core.SymbolRecord
	scope := f.idx.importedFiles(f.sym.FilePath)
	for _, c := range f.idx.byName[strings.ToLower(name)] {
		if c.Name != name || c.Language != "python" || c.Kind != core.KindFunction || c.ParentSymbol != "" || strings.Contains(c.QualifiedName, ".") {
			continue
		}
		if _, ok := scope[c.FilePath]; !ok && c.FilePath != f.sym.FilePath {
			continue
		}
		if c.FilePath == f.sym.FilePath {
			return c
		}
		if found != nil {
			return nil // ambiguous
		}
		found = c
	}
	return found
}

// valueOf reads the function values an expression evaluates to.
func (f *pyFlow) valueOf(expr string) pyFlowVal {
	expr = strings.TrimSpace(expr)
	expr = strings.TrimPrefix(expr, "await ")
	var v pyFlowVal
	switch {
	case expr == "":
		return v
	case strings.HasPrefix(expr, "lambda ") || strings.HasPrefix(expr, "lambda:"):
		// A lambda's body is its enclosing function's code.
		if f.sym.Kind != core.KindClass {
			v.funcs = append(v.funcs, f.sym)
		}
		return v
	case pyFlowWrapperRe.MatchString(expr):
		open := strings.IndexByte(expr, '(')
		if args := pyFlowArgs(expr[open:]); len(args) > 0 {
			return f.valueOf(args[0])
		}
		return v
	}
	if isPlainIdent(expr) {
		if expr == "self" || expr == "cls" {
			return v
		}
		if f.params[expr] {
			if !pyFlowDataAnnotation(f.ann[expr]) {
				v.slots = append(v.slots, pySymKey(f.sym)+"#p:"+expr)
			}
			return v
		}
		if l, ok := f.locals[expr]; ok {
			return l
		}
		if f.encPar[expr] {
			v.slots = append(v.slots, pySymKey(f.enc)+"#p:"+expr)
			return v
		}
		if f.shadow[expr] {
			return v // a local (prefix = "-"), whatever module function shares the name
		}
		if fn := f.function(expr); fn != nil {
			v.funcs = append(v.funcs, fn)
		}
		return v
	}
	if strings.HasPrefix(expr, "self.") || strings.HasPrefix(expr, "cls.") {
		attr := expr[strings.IndexByte(expr, '.')+1:]
		if isPlainIdent(attr) {
			if m := f.method(attr); m != nil {
				if propertyAnnotationMatches(m, false) || hasAnnotation(m, "cached_property") {
					return v // self.prop is the property's value, not a function
				}
				// A bound method runs the instance's override.
				v.funcs = append(v.funcs, m)
				for _, o := range subclassOverrides(f.idx, "python", f.class.Name, attr, dirOf(f.class.FilePath)) {
					if pyIsSubclass(f.idx, o.ParentSymbol, f.class.Name, dirOf(o.FilePath)) && !propertyAnnotationMatches(o, false) {
						v.funcs = append(v.funcs, o)
					}
				}
			} else {
				v.slots = append(v.slots, f.attrKeys(attr)...)
			}
		}
		return v
	}
	// A registry display ({None: [f]}, defaultdict(list, {...}), (f, g)):
	// the functions named in it, never called there.
	if strings.ContainsAny(expr[:1], "[{(") || strings.HasPrefix(expr, "defaultdict(") || strings.HasPrefix(expr, "dict(") || strings.HasPrefix(expr, "list(") {
		for _, loc := range pyFlowIdentRe.FindAllStringIndex(expr, -1) {
			name := expr[loc[0]:loc[1]]
			if loc[0] > 0 && (expr[loc[0]-1] == '.' || pyIdentByte(expr[loc[0]-1])) {
				continue
			}
			rest := strings.TrimLeft(expr[loc[1]:], " ")
			if strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "==") {
				continue
			}
			if strings.HasPrefix(name, "self.") {
				v.add(f.valueOf(name))
			} else if !strings.Contains(name, ".") && !f.shadow[name] {
				if fn := f.function(name); fn != nil {
					v.funcs = append(v.funcs, fn)
				} else if f.params[name] {
					v.slots = append(v.slots, pySymKey(f.sym)+"#p:"+name)
				}
			}
		}
	}
	return v
}

func pyIdentByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// store records v flowing into slot key.
func (f *pyFlow) store(key string, v pyFlowVal) {
	if key == "" || f.test {
		// A test passing its own function (ScriptInfo(create_app=...))
		// makes the library call it only under that test.
		return
	}
	for _, fn := range v.funcs {
		f.stores = append(f.stores, core.Edge{From: f.sym.ID, To: fn.ID, Type: core.EdgeFuncFieldStore,
			Confidence: 0.75, Source: core.EvidenceSourceHeuristic, Lines: key})
	}
	for _, s := range v.slots {
		s, _, _ = strings.Cut(s, "^")
		if s != key {
			f.links = append(f.links, key+"<-"+s)
		}
	}
}

// scanBody reads assignments, registry appends, loops and returns of the
// symbol's own body (nested defs and classes are their own symbols).
func (f *pyFlow) scanBody(isClass bool) {
	code := pyLogicalLines(strings.Split(maskCode("python", f.sym.RawText), "\n"))
	skipIndent := -1
	bodyIndent := -1
	for i, line := range code {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if skipIndent >= 0 {
			if indent > skipIndent {
				continue
			}
			skipIndent = -1
		}
		if bodyIndent < 0 {
			bodyIndent = indent
		}
		if m := pyFlowDefRe.FindStringSubmatch(line); m != nil {
			skipIndent = indent
			continue
		}
		if isClass {
			// Class body: `default = staticmethod(_default)` stores into the
			// class attribute.
			if indent != bodyIndent {
				continue
			}
			if m := pyFlowAssignRe.FindStringSubmatch(line); m != nil && isPlainIdent(m[2]) {
				f.store(pySymKey(f.sym)+"#a:"+m[2], f.valueOf(m[3]))
				f.bodyCall(m[3], i, "")
			}
			continue
		}
		if m := pyFlowForRe.FindStringSubmatch(line); m != nil {
			// Iterating a registry yields what it holds.
			src := strings.TrimSpace(m[2])
			for _, suffix := range []string{".values()", ".items()", "()"} {
				src = strings.TrimSuffix(src, suffix)
			}
			if j := strings.IndexByte(src, '['); j > 0 {
				src = src[:j]
			}
			if j := strings.Index(src, ".get("); j > 0 {
				src = src[:j]
			}
			if v := f.valueOf(src); !v.empty() {
				f.locals[m[1]] = v
			}
			continue
		}
		if m := pyFlowAppendRe.FindStringSubmatch(line); m != nil {
			f.store(f.ownKey(strings.TrimPrefix(m[1], "self.")), f.valueOf(pyFlowLastArg(m[2])))
			continue
		}
		if m := pyFlowReturnRe.FindStringSubmatch(line); m != nil {
			f.store(pySymKey(f.sym)+"#ret", f.valueOf(m[1]))
			continue
		}
		if m := pyFlowAssignRe.FindStringSubmatch(line); m != nil {
			target, value := m[2], m[3]
			if strings.Contains(value, "==") && !strings.ContainsAny(value[:1], "[{(") {
				continue
			}
			val := f.valueOf(value)
			if strings.HasSuffix(strings.TrimSpace(value), ")") && val.empty() {
				// x = H(...): x holds H's result, bound at this site.
				if h := f.callTarget(strings.TrimSpace(value)); h != nil {
					site := pySymKey(f.sym) + ":" + strconv.Itoa(f.sym.Span.Start+i)
					val.slots = append(val.slots, pySymKey(h)+"#ret^"+site)
					f.bodyCall(value, i, site)
				}
			}
			switch {
			case strings.HasPrefix(target, "self."):
				attr := strings.TrimPrefix(target, "self.")
				if j := strings.IndexByte(attr, '['); j > 0 {
					attr = attr[:j]
				}
				if isPlainIdent(attr) {
					f.store(f.ownKey(attr), val)
				}
			case isPlainIdent(target) && !val.empty():
				f.locals[target] = val
			}
		}
	}
}

// pyFlowLastArg is the value appended in append(x) / insert(i, x) /
// setdefault(k, x).
func pyFlowLastArg(args string) string {
	parts := splitTopLevel(args, ',')
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// callTarget resolves the callable of a call expression `H(...)` /
// `self.m(...)` to one project function.
func (f *pyFlow) callTarget(call string) *core.SymbolRecord {
	open := strings.IndexByte(call, '(')
	if open <= 0 {
		return nil
	}
	name := strings.TrimSpace(call[:open])
	if strings.HasPrefix(name, "self.") || strings.HasPrefix(name, "cls.") {
		return f.method(name[strings.IndexByte(name, '.')+1:])
	}
	if isPlainIdent(name) {
		return f.function(name)
	}
	return nil
}

// scanCalls reads the symbol's call sites: values passed as arguments
// (stored into the callee's parameters, or handed to a library), and calls
// through parameters, locals, self attributes and call results.
func (f *pyFlow) scanCalls(isClass bool) {
	lines := strings.Split(maskCode("python", f.sym.RawText), "\n")
	raw := strings.Split(f.sym.RawText, "\n")
	for _, cs := range f.sym.CallSites {
		if cs.Line <= 0 {
			continue
		}
		li := cs.Line - f.sym.Span.Start
		if li < 0 || li >= len(lines) {
			continue
		}
		pos := strconv.Itoa(cs.Line)
		if cs.Col > 0 {
			pos += ":" + strconv.Itoa(cs.Col)
		}
		callee := cs.Callee
		qual, name := "", callee
		if i := strings.LastIndexByte(callee, '.'); i >= 0 {
			qual, name = callee[:i], callee[i+1:]
		}
		// Calls through a slot.
		switch {
		case isClass:
		case qual == "" && f.params[name]:
			f.calls = append(f.calls, pySymKey(f.sym)+"#p:"+name+"@"+pos)
		case qual == "" && !f.locals[name].empty():
			f.callThrough(f.locals[name], pos)
		case qual == "" && f.encPar[name]:
			f.calls = append(f.calls, pySymKey(f.enc)+"#p:"+name+"@"+pos)
		case (qual == "self" || qual == "cls") && f.class != nil && f.method(name) == nil:
			for _, k := range f.attrKeys(name) {
				f.calls = append(f.calls, k+"@"+pos)
			}
		}
		// Arguments, read from the source: astkit keeps no keywords.
		args, after, ok := pyFlowCallArgs(lines, raw, li, cs.Col, name)
		if !ok {
			continue
		}
		if qual == "" && (f.params[name] || f.encPar[name] || !f.locals[name].empty()) {
			continue
		}
		callResult := strings.HasPrefix(strings.TrimLeft(after, " "), "(")
		if !callResult && !f.anyValue(args) {
			continue // nothing function-valued flows here
		}
		target, external := f.resolveCallee(qual, name)
		site := ""
		if target != nil && callResult {
			// A call through the result: self.ensure_sync(func)(). The
			// arguments are bound at this site too, so a function that
			// returns its argument hands back this call's, not every
			// caller's.
			site = pySymKey(f.sym) + ":" + pos
			f.calls = append(f.calls, pySymKey(target)+"#ret@"+pos+"^"+site)
		}
		f.storeArgs(target, external, name, args, pos, site)
	}
}

// callThrough records a call through a value's slots, and direct edges to
// its known functions.
func (f *pyFlow) callThrough(v pyFlowVal, pos string) {
	for _, s := range v.slots {
		key, site, _ := strings.Cut(s, "^")
		if site != "" {
			f.calls = append(f.calls, key+"@"+pos+"^"+site)
		} else {
			f.calls = append(f.calls, key+"@"+pos)
		}
	}
	for _, fn := range v.funcs {
		if fn.ID != f.sym.ID {
			f.direct = append(f.direct, core.Edge{From: f.sym.ID, To: fn.ID, Type: core.EdgeCalls, Confidence: 0.75,
				Source: core.EvidenceSourceHeuristic, Reason: core.ReasonFunctionRef, Lines: pos})
		}
	}
}

// resolveCallee resolves a call's target to one project callable (a
// constructor call to its __init__), or reports the callee external: no
// project symbol has the name.
func (f *pyFlow) resolveCallee(qual, name string) (*core.SymbolRecord, bool) {
	switch qual {
	case "":
		if t := f.function(name); t != nil {
			return t, false
		}
		if cls := pyResolveClass(f.idx, name, dirOf(f.sym.FilePath)); cls != nil {
			if inits := pyDunderTargets(f.idx, cls.Name, "__init__", dirOf(cls.FilePath)); len(inits) == 1 {
				return inits[0], false
			}
			return nil, false
		}
		return nil, len(namedSymbols(f.idx, name)) == 0
	case "self", "cls":
		return f.method(name), false
	}
	if f.types == nil {
		f.types = pyLocalTypes(f.idx, f.sym)
	}
	if t := f.types[qual]; t != "" {
		if strings.HasPrefix(t, "extern:") {
			return nil, true // kwargs.setdefault(...): dict's
		}
		t = strings.TrimPrefix(t, "class:")
		for _, cn := range pyMRO(f.idx, t, dirOf(f.sym.FilePath)) {
			for _, m := range f.idx.byName[strings.ToLower(name)] {
				if m.Name == name && m.ParentSymbol == cn && m.Language == "python" && (m.Kind == core.KindMethod || m.Kind == core.KindConstructor) {
					return m, false
				}
			}
		}
		return nil, false
	}
	// recv.m(...) on something the project does not declare m for.
	return nil, len(namedSymbols(f.idx, name)) == 0
}

// storeArgs stores each function-valued argument into the target's
// parameter (and its site-bound variant), or, for a library call, records
// that the library may call it.
func (f *pyFlow) storeArgs(target *core.SymbolRecord, external bool, callExprName string, args []string, pos, site string) {
	var tparams []string
	var tann map[string]string
	if target != nil {
		tparams = pyFlowParamNames(target)
		if len(tparams) > 0 && (tparams[0] == "self" || tparams[0] == "cls") && target.ParentSymbol != "" {
			tparams = tparams[1:]
		}
		tann = pyFlowParamAnnotations(target)
	}
	for i, a := range args {
		a = strings.TrimSpace(a)
		if strings.HasPrefix(a, "*") {
			continue
		}
		kw := ""
		if eq := strings.IndexByte(a, '='); eq > 0 && isPlainIdent(strings.TrimSpace(a[:eq])) && (eq+1 >= len(a) || a[eq+1] != '=') {
			kw, a = strings.TrimSpace(a[:eq]), a[eq+1:]
		}
		v := f.valueOf(a)
		if v.empty() {
			continue
		}
		if target != nil {
			param := kw
			if param == "" {
				if i < len(tparams) && !strings.HasPrefix(tparams[i], "*") {
					param = tparams[i]
				} else {
					for _, p := range tparams {
						if strings.HasPrefix(p, "*") && !strings.HasPrefix(p, "**") {
							param = p
						}
					}
				}
			}
			param = strings.TrimLeft(param, "*")
			if param != "" && !pyFlowDataAnnotation(tann[param]) {
				key := pySymKey(target) + "#p:" + param
				f.store(key, v)
				if site != "" {
					f.store(key+"@@"+site, v)
				}
			}
			continue
		}
		if external && !pyFlowWrapperRe.MatchString(callExprName+"(") && pyFlowDirectRef(a) {
			// Handed to the library, which may call it.
			for _, sl := range v.slots {
				// Only an attribute slot (a stored callable, a registry) is
				// handed over as such; a parameter or a call's result is
				// data as often as not.
				if key, _, _ := strings.Cut(sl, "^"); strings.Contains(key, "#a:") {
					f.calls = append(f.calls, key+"@"+pos)
				}
			}
			for _, fn := range v.funcs {
				if fn.ID != f.sym.ID {
					f.direct = append(f.direct, core.Edge{From: f.sym.ID, To: fn.ID, Type: core.EdgeCalls, Confidence: 0.7,
						Source: core.EvidenceSourceHeuristic, Reason: core.ReasonFunctionRef, Lines: pos})
				}
			}
		}
	}
}

// pyFlowCallArgs reads the argument list of the call whose name starts at
// (line li, column col): the arguments and the text right after the closing
// parenthesis. Strings are read unmasked so a keyword's name survives.
func pyFlowCallArgs(masked, raw []string, li, col int, name string) ([]string, string, bool) {
	if col <= 0 {
		return nil, "", false
	}
	line := masked[li]
	start := col - 1 + len(name)
	if start > len(line) {
		return nil, "", false
	}
	var b strings.Builder
	depth := 0
	sub := 0
	began := false
	for l := li; l < len(masked) && l < li+40; l++ {
		s := masked[l]
		from := 0
		if l == li {
			from = start
		}
		for k := from; k < len(s); k++ {
			c := s[k]
			if !began {
				if c == ' ' {
					continue
				}
				if c == '[' && sub == 0 {
					// ConfigAttribute[timedelta](...): skip the subscript.
					sub = 1
					for k++; k < len(s) && sub > 0; k++ {
						if s[k] == '[' {
							sub++
						} else if s[k] == ']' {
							sub--
						}
					}
					k--
					continue
				}
				if c != '(' {
					return nil, "", false
				}
				began = true
				depth = 1
				continue
			}
			switch c {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				depth--
				if depth == 0 {
					return splitTopLevel(b.String(), ','), s[k+1:], true
				}
			}
			b.WriteByte(c)
		}
		b.WriteByte(' ')
	}
	return nil, "", false
}

// pyFlowArgs splits a parenthesized argument text "(a, b)" into arguments.
func pyFlowArgs(s string) []string {
	if !strings.HasPrefix(s, "(") {
		return nil
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return splitTopLevel(s[1:i], ',')
			}
		}
	}
	return nil
}

func (f *pyFlow) emit() []core.Edge {
	// The graph keeps one edge per (from, to, type): a function stored into
	// several slots is one record listing them.
	keys := map[string][]string{}
	var tos []string
	for _, e := range f.stores {
		if _, ok := keys[e.To]; !ok {
			tos = append(tos, e.To)
		}
		keys[e.To] = append(keys[e.To], e.Lines)
	}
	var out []core.Edge
	for _, to := range tos {
		out = append(out, core.Edge{From: f.sym.ID, To: to, Type: core.EdgeFuncFieldStore, Confidence: 0.75,
			Source: core.EvidenceSourceHeuristic, Lines: strings.Join(uniqueSortedStrings(sortedCopy(keys[to])), ",")})
	}
	out = append(out, f.direct...)
	if len(f.links) > 0 {
		out = append(out, core.Edge{From: f.sym.ID, To: f.sym.ID, Type: core.EdgeFuncSlotFlow, Confidence: 0.75,
			Source: core.EvidenceSourceHeuristic, Lines: strings.Join(uniqueSortedStrings(sortedCopy(f.links)), ",")})
	}
	if len(f.calls) > 0 {
		out = append(out, core.Edge{From: f.sym.ID, To: f.sym.ID, Type: core.EdgeFuncFieldCall, Confidence: 0.75,
			Source: core.EvidenceSourceHeuristic, Lines: strings.Join(uniqueSortedStrings(sortedCopy(f.calls)), ",")})
	}
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// pyLogicalLines joins a statement continued across lines (open brackets)
// onto its first line; the continuation lines become blank so line indexes
// stay aligned.
func pyLogicalLines(lines []string) []string {
	out := make([]string, len(lines))
	for i := 0; i < len(lines); i++ {
		stmt := lines[i]
		depth := pyBracketDepth(stmt)
		j := i
		for depth > 0 && j+1 < len(lines) && j-i < 60 {
			j++
			stmt += " " + strings.TrimSpace(lines[j])
			depth += pyBracketDepth(lines[j])
		}
		out[i] = stmt
		i = j
	}
	return out
}

func pyBracketDepth(s string) int {
	d := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			d++
		case ')', ']', '}':
			d--
		}
	}
	return d
}

// pyFlowParamAnnotations maps a def's parameter names to their annotations.
func pyFlowParamAnnotations(s *core.SymbolRecord) map[string]string {
	out := map[string]string{}
	for _, g := range splitTopLevel(pyDefParams(s.RawText), ',') {
		name, ann, ok := strings.Cut(strings.TrimSpace(g), ":")
		if !ok {
			continue
		}
		if eq := strings.IndexByte(ann, '='); eq >= 0 {
			ann = ann[:eq]
		}
		out[strings.TrimLeft(strings.TrimSpace(name), "*")] = strings.TrimSpace(ann)
	}
	return out
}

// pyFlowDataAnnotation reports an annotation that names plain data, which
// never holds a function: str, int, a container of data, Path, ... A
// Callable, a type alias, a union with a callable, or none at all may.
func pyFlowDataAnnotation(ann string) bool {
	if ann == "" || strings.Contains(ann, "Callable") || strings.Contains(ann, "Func") || strings.Contains(ann, "Callback") ||
		strings.Contains(ann, "Handler") || strings.Contains(ann, "Processor") || strings.Contains(ann, "Function") {
		return false
	}
	base := strings.TrimSpace(ann)
	for _, p := range []string{"t.", "typing.", "te."} {
		base = strings.TrimPrefix(base, p)
	}
	if i := strings.IndexAny(base, "[| "); i > 0 {
		base = base[:i]
	}
	switch base {
	case "str", "int", "float", "bool", "bytes", "dict", "Dict", "list", "List", "set", "Set", "tuple", "Tuple",
		"Mapping", "Sequence", "Iterable", "PathLike", "Path", "datetime", "timedelta", "None", "Any", "object",
		"Optional", "Union", "Literal", "IO", "TextIO", "BinaryIO":
		return base != "Any" && base != "object" && base != "Optional" && base != "Union"
	}
	return false
}

// bodyCall stores the arguments of a call written as an assignment's value
// (class-body `x = ConfigAttribute[timedelta]("X", get_converter=f)`, or a
// body `fn = H(f)` bound at site). Class-body calls carry no call sites.
func (f *pyFlow) bodyCall(value string, line int, site string) {
	m := pyFlowCallExprRe.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return
	}
	qual, name := "", m[1]
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		qual, name = name[:i], name[i+1:]
	}
	args := pyFlowArgs(strings.TrimSpace(value)[len(m[0])-1:])
	if args == nil {
		return
	}
	if site == "" && f.sym.Kind != core.KindClass {
		return // a body call's arguments are read through its call site
	}
	target, external := f.resolveCallee(qual, name)
	f.storeArgs(target, external, name, args, strconv.Itoa(f.sym.Span.Start+line), site)
}

var pyFlowCallExprRe = regexp.MustCompile(`^([A-Za-z_][\w.]*)\s*(?:\[[^\]]*\])?\s*\(`)

// anyValue reports whether any argument carries a function value.
func (f *pyFlow) anyValue(args []string) bool {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if eq := strings.IndexByte(a, '='); eq > 0 && isPlainIdent(strings.TrimSpace(a[:eq])) && (eq+1 >= len(a) || a[eq+1] != '=') {
			a = a[eq+1:]
		}
		if !strings.HasPrefix(a, "*") && !f.valueOf(a).empty() {
			return true
		}
	}
	return false
}

// pyFlowDirectRef reports an argument that is itself a function reference
// (a name, self.attr, a lambda, a wrapper of one), not a container holding
// some: only the former is handed to a library to call.
func pyFlowDirectRef(a string) bool {
	a = strings.TrimSpace(a)
	return isPlainIdent(a) || strings.HasPrefix(a, "self.") && isPlainIdent(a[5:]) ||
		strings.HasPrefix(a, "lambda") || pyFlowWrapperRe.MatchString(a)
}

var (
	pyFlowBindRe   = regexp.MustCompile(`(?m)^\s*([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)\s*(?::[^=\n]+)?=[^=]`)
	pyFlowForBind  = regexp.MustCompile(`\bfor\s+\(?([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)\)?\s+in\b`)
	pyFlowAsBind   = regexp.MustCompile(`\bas\s+([A-Za-z_]\w*)`)
	pyFlowWalrusRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*:=`)
)

// pyFlowAssignedNames lists the names a def's body binds (assignments, loop
// and comprehension targets, with/except ... as, walrus): such a name is a
// local, never the module function of the same name. Nested defs bind
// their own name, which function() resolves first anyway.
func pyFlowAssignedNames(raw string) map[string]bool {
	out := map[string]bool{}
	code := maskCode("python", raw)
	add := func(list string) {
		for _, n := range strings.Split(list, ",") {
			if n = strings.TrimSpace(n); n != "" {
				out[n] = true
			}
		}
	}
	for _, m := range pyFlowBindRe.FindAllStringSubmatch(code, -1) {
		add(m[1])
	}
	for _, m := range pyFlowForBind.FindAllStringSubmatch(code, -1) {
		add(m[1])
	}
	for _, m := range pyFlowAsBind.FindAllStringSubmatch(code, -1) {
		out[m[1]] = true
	}
	for _, m := range pyFlowWalrusRe.FindAllStringSubmatch(code, -1) {
		out[m[1]] = true
	}
	return out
}
