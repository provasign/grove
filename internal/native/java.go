package native

import (
	"context"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
	"sort"
)

type javaAnalyzer struct{}

func (javaAnalyzer) Name() string { return "java" }

func (javaAnalyzer) Languages() []string { return []string{"java"} }

// Available: javac needs only a JDK, not a build system (a plain-javac
// project has no pom.xml); jdtls/Maven/Gradle alone still run the
// tool-free inheritance and type-use pass. Neither means no compiler facts,
// and the reason says so in terms readiness can alert on.
func (javaAnalyzer) Available(_ context.Context, root string) Availability {
	if findJDK() != nil {
		return Availability{Available: true}
	}
	if !anyFile(root, "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts") {
		return Availability{Reason: "no JDK 11+ found and no Maven or Gradle project config"}
	}
	if firstExistingExecutable("jdtls", "mvn", "gradle") == "" {
		return Availability{Reason: "no JDK 11+ found (JAVA_HOME, PATH, java_home, Homebrew openjdk) and no jdtls, mvn, or gradle"}
	}
	return Availability{Available: true}
}

func (javaAnalyzer) Analyze(ctx context.Context, req Request) Result {
	diags := []string{"project tooling detected"}
	// Compiler-resolved calls and field references: javac attributes the
	// project's sources (with its Maven classpath when resolvable) and names
	// the exact declaration of every call, constructor call, method
	// reference and field access -- overloads included. An incremental run
	// attributes only the affected packages; the text pass below follows the
	// same scope so every other package's stored edges carry forward whole.
	javacEdges, javacDiags, scopedDirs := javacResolveScoped(ctx, req)
	var inScope func(file string) bool
	if scopedDirs != nil {
		dirs := map[string]bool{}
		for _, d := range scopedDirs {
			dirs[d] = true
		}
		inScope = func(file string) bool { return dirs[packageDir(file)] }
	}
	edges := javaSemanticEdgesIn(req.Symbols, inScope)
	edges = append(edges, javacEdges...)
	diags = append(diags, javacDiags...)
	diags = append(diags,
		"resolved "+itoa(countNativeEdges(edges, core.EdgeCalls))+" native call edge(s)",
		"resolved "+itoa(countNativeEdges(edges, core.EdgeReads)+countNativeEdges(edges, core.EdgeWrites))+" native field-reference edge(s)",
		"resolved "+itoa(countNativeEdges(edges, core.EdgeUsesType))+" native type-use edge(s)",
		"resolved "+itoa(countNativeEdges(edges, core.EdgeExtends))+" native extends edge(s)",
		"resolved "+itoa(countNativeEdges(edges, core.EdgeImplements))+" native implements edge(s)",
	)
	res := Result{Edges: edges, Diagnostics: diags}
	if scopedDirs != nil {
		res.Partial = map[string][]string{"java": scopedDirs}
	}
	return res
}

type javaIndex struct {
	typesByName  map[string][]core.SymbolRecord
	methods      map[string][]core.SymbolRecord
	ctors        map[string][]core.SymbolRecord
	methodsByCls map[string][]core.SymbolRecord
}

func newJavaIndex(symbols []core.SymbolRecord) javaIndex {
	idx := javaIndex{
		typesByName:  map[string][]core.SymbolRecord{},
		methods:      map[string][]core.SymbolRecord{},
		ctors:        map[string][]core.SymbolRecord{},
		methodsByCls: map[string][]core.SymbolRecord{},
	}
	for _, symbol := range symbols {
		if symbol.Language != "java" {
			continue
		}
		if typeKind(symbol.Kind) {
			idx.typesByName[symbol.Name] = append(idx.typesByName[symbol.Name], symbol)
		}
		switch symbol.Kind {
		case core.KindMethod:
			key := symbol.ParentSymbol + "." + symbol.Name
			idx.methods[key] = append(idx.methods[key], symbol)
			idx.methodsByCls[symbol.ParentSymbol] = append(idx.methodsByCls[symbol.ParentSymbol], symbol)
		case core.KindConstructor:
			idx.ctors[symbol.ParentSymbol] = append(idx.ctors[symbol.ParentSymbol], symbol)
		}
	}
	return idx
}

func javaSemanticEdges(symbols []core.SymbolRecord) []core.Edge {
	return javaSemanticEdgesIn(symbols, nil)
}

// javaSemanticEdgesIn emits edges only from symbols whose file inScope
// accepts (nil: every file); targets still resolve against every symbol.
func javaSemanticEdgesIn(symbols []core.SymbolRecord, inScope func(string) bool) []core.Edge {
	idx := newJavaIndex(symbols)
	slowNames := slowTypeNames(idx.typesByName)
	var edges []core.Edge
	seen := map[string]bool{}
	add := func(edge core.Edge) {
		key := edge.From + "\x00" + string(edge.Type) + "\x00" + edge.To
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, edge)
	}

	for _, symbol := range symbols {
		if symbol.Language != "java" || (inScope != nil && !inScope(symbol.FilePath)) {
			continue
		}
		if typeKind(symbol.Kind) {
			// The masked declaration header only: the raw first line also
			// carried a trailing comment (`class A { // extends Base`).
			for _, ref := range javaInheritanceRefs(maskedDeclHeader("java", symbol)) {
				if target, ok := javaBestType(idx, ref.Name, symbol.FilePath); ok && target.ID != symbol.ID {
					add(symbolEdge(symbol, target, ref.EdgeType, 0.97))
				}
			}
		}
		if !callableKind(symbol.Kind) || symbol.RawText == "" {
			continue
		}
		// Call edges intentionally NOT emitted here: the text-matching
		// approach edged every overload of every name it saw (a 6x edge
		// explosion on overload-heavy code). astkit emits qualified,
		// arity-tagged call sites for Java, so the graph layer's narrowed
		// resolution is authoritative. This pass keeps what text matching
		// is still good for: inheritance and type-usage evidence.
		masked := maskCode("java", symbol.RawText)
		for _, className := range javaConstructedTypes(masked) {
			if target, ok := javaBestType(idx, className, symbol.FilePath); ok && target.ID != symbol.ID {
				add(symbolEdge(symbol, target, core.EdgeUsesType, 0.96))
			}
		}
		names := make([]string, 0, 8)
		for t := range typeTokensIn(masked) {
			if _, ok := idx.typesByName[t]; ok {
				names = append(names, t)
			}
		}
		sort.Strings(names)
		for _, name := range slowNames {
			if containsTypeToken(masked, name) {
				names = append(names, name)
			}
		}
		for _, name := range names {
			if target, ok := javaBestType(idx, name, symbol.FilePath); ok && target.ID != symbol.ID {
				add(symbolEdge(symbol, target, core.EdgeUsesType, 0.94))
			}
		}
	}
	return edges
}

type javaInheritanceRef struct {
	Name     string
	EdgeType core.EdgeType
}

func javaInheritanceRefs(text string) []javaInheritanceRef {
	var refs []javaInheritanceRef
	// Full dotted names pass through: javaBestType uses a Capitalized
	// qualifier ("SettableBeanProperty.Delegating") to scope resolution to
	// the right nested type — stripping it here re-created the bare-name
	// fan-out the scoping exists to prevent.
	tail := javaDeclarationTail(text)
	for _, name := range inheritanceClause(tail, "extends", "implements") {
		refs = append(refs, javaInheritanceRef{Name: name, EdgeType: core.EdgeExtends})
	}
	for _, name := range inheritanceClause(tail, "implements") {
		refs = append(refs, javaInheritanceRef{Name: name, EdgeType: core.EdgeImplements})
	}
	return refs
}

var javaDeclarationHead = regexp.MustCompile(`\b(?:class|interface|enum|record)\s+[A-Za-z_][A-Za-z0-9_]*`)

func javaDeclarationTail(text string) string {
	loc := javaDeclarationHead.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	tail := strings.TrimLeft(text[loc[1]:], " \t\r\n")
	if n := balancedSuffixEnd(tail, '<', '>'); n > 0 {
		tail = strings.TrimLeft(tail[n:], " \t\r\n")
	}
	return tail
}

var javaNewPattern = regexp.MustCompile(`\bnew\s+([A-Za-z_][A-Za-z0-9_.]*)(?:\s*<[^;(){}]*>)?\s*\(`)

// javaConstructedTypes returns the `new T(` class names of masked (see
// maskCode).
func javaConstructedTypes(masked string) []string {
	matches := javaNewPattern.FindAllStringSubmatch(masked, -1)
	seen := map[string]bool{}
	var out []string
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		name := lastDottedName(match[1])
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func javaBestType(idx javaIndex, name, fromFile string) (core.SymbolRecord, bool) {
	simple := lastDottedName(name)
	candidates := idx.typesByName[simple]
	if len(candidates) == 0 {
		return core.SymbolRecord{}, false
	}
	// A Capitalized qualifier names a nested type ("ValueInstantiator.Base")
	// and SCOPES resolution to candidates declared inside that parent —
	// without it, `extends ValueInstantiator.Base` resolved to arbitrary
	// same-named types (jackson: every hierarchy owning a Base). Lowercase
	// qualifiers are package segments; resolution proceeds by simple name.
	if q := nestedQualifierOf(name); q != "" {
		scoped := candidates[:0:0]
		for _, c := range candidates {
			if c.ParentSymbol == q || c.QualifiedName == q+"."+simple ||
				strings.HasSuffix(c.QualifiedName, "."+q+"."+simple) {
				scoped = append(scoped, c)
			}
		}
		candidates = scoped
		if len(candidates) == 0 {
			return core.SymbolRecord{}, false // external nested type (Map.Entry)
		}
	}
	for _, candidate := range candidates {
		if candidate.FilePath == fromFile {
			return candidate, true
		}
	}
	fromDir := packageDir(fromFile)
	for _, candidate := range candidates {
		if packageDir(candidate.FilePath) == fromDir {
			return candidate, true
		}
	}
	// Cross-package: only an unambiguous name resolves. Returning an
	// arbitrary candidate emitted confidently-wrong edges (0.97) that
	// polluted every closure walk downstream.
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return core.SymbolRecord{}, false
}

// nestedQualifierOf returns the innermost dotted qualifier when it names a
// nested type (Capitalized, per Java/C# convention), "" otherwise.
func nestedQualifierOf(name string) string {
	name = strings.TrimSpace(name)
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return ""
	}
	q := name[:i]
	if j := strings.LastIndexByte(q, '.'); j >= 0 {
		q = q[j+1:]
	}
	if q == "" || q[0] < 'A' || q[0] > 'Z' {
		return ""
	}
	return q
}

func lastDottedName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}
