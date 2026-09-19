package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// C preprocessor call-through. A function-like macro is not a function —
// it has no symbol in a compiler's call graph — but the calls its body
// makes execute in every function that invokes it: `json_object_foreach(o,
// k, v)` calls json_object_iter/json_object_iter_next, `RUN_TEST(f)` calls
// UnityDefaultTestRun(f, ..) and, through its argument, f. clang's AST
// records those at the invocation site, and so did a third of cJSON's
// truth edges (its Unity test suite is macros end to end; R 0.65). astkit
// emits every in-repo #define as a KindMacro symbol whose CallSites are
// the body's lexical calls and whose Signature lists its parameters; here
// an invocation is expanded into those calls with parameters substituted
// by the invocation's arguments, transitively through nested macros.

var cMacroParamsRe = regexp.MustCompile(`^#define\s+[A-Za-z_]\w*\(([^)]*)\)`)

const macroExpansionDepth = 4

// macroSymbols returns every in-repo C-family macro definition named name.
// A macro is often defined several times under preprocessor conditions
// (Unity's RUN_TEST has three); without the configuration the invocation
// expands through each, and duplicate edges collapse downstream.
func macroSymbols(idx *edgeIndex, name string) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.Name == name && cand.Kind == core.KindMacro && cFamilyLang(cand.Language) {
			out = append(out, cand)
		}
	}
	return out
}

func cFamilyLang(lang string) bool {
	return lang == "c" || lang == "cpp" || lang == "objc"
}

// macroParams parses "#define NAME(a, b, ...)" into ["a", "b", "..."];
// an object-like macro has none.
func macroParams(sig string) []string {
	m := cMacroParamsRe.FindStringSubmatch(sig)
	if m == nil {
		return nil
	}
	var out []string
	for _, p := range strings.Split(m[1], ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// expandMacroCallSites returns the caller's call sites plus, for every
// site that invokes an in-repo macro, the macro body's calls with the
// invocation's arguments substituted for the macro's parameters (a body
// call whose callee IS a parameter — `#define CALL(f) f(x)` — calls the
// argument). Expansion recurses through macros a body invokes, bounded in
// depth and guarded against cycles. Non-C-family callers are returned as
// they are.
func expandMacroCallSites(idx *edgeIndex, symbol *core.SymbolRecord) []core.CallSite {
	if !cFamilyLang(symbol.Language) || len(symbol.CallSites) == 0 {
		return symbol.CallSites
	}
	out := append([]core.CallSite(nil), symbol.CallSites...)
	seen := map[string]bool{}
	var expand func(cs core.CallSite, depth int)
	var expandOne func(mac *core.SymbolRecord, cs core.CallSite, depth int)
	expand = func(cs core.CallSite, depth int) {
		if depth > macroExpansionDepth || strings.Contains(cs.Callee, ".") || cs.ReferenceOnly {
			return
		}
		for _, mac := range macroSymbols(idx, cs.Callee) {
			expandOne(mac, cs, depth)
		}
	}
	expandOne = func(mac *core.SymbolRecord, cs core.CallSite, depth int) {
		key := mac.ID + "\x00" + strings.Join(cs.Args, ",")
		if seen[key] {
			return
		}
		seen[key] = true
		params := macroParams(mac.Signature)
		bind := map[string]string{}
		for i, p := range params {
			if p == "..." {
				if i < len(cs.Args) {
					bind["__VA_ARGS__"] = cs.Args[i]
				}
				break
			}
			if i < len(cs.Args) {
				bind[p] = cs.Args[i]
			} else {
				bind[p] = ""
			}
		}
		for _, mc := range mac.CallSites {
			callee := mc.Callee
			if v, bound := bind[callee]; bound {
				if v == "" || !isCIdent(v) {
					continue // an expression or unknown argument: no callable name
				}
				callee = v
			}
			if callee == mac.Name {
				continue
			}
			args := make([]string, len(mc.Args))
			for i, a := range mc.Args {
				if v, bound := bind[a]; bound {
					args[i] = v
				} else {
					args[i] = a
				}
			}
			site := core.CallSite{Callee: callee, Line: cs.Line, Argc: mc.Argc, Args: args}
			out = append(out, site)
			expand(site, depth+1)
		}
	}
	for _, cs := range symbol.CallSites {
		expand(cs, 0)
	}
	return out
}

func isCIdent(s string) bool {
	if s == "" || s[0] == '#' || s[0] == '%' || strings.Contains(s, ":") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}
