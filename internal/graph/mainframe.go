package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Mainframe call resolution is kept apart from the modern-language path:
// resolveCallEdges dispatches here
// for cobol/jcl symbols and the modern machinery never sees them.
//
// Semantics differ from modern languages in two ways:
//   - PERFORM targets are paragraphs/sections in the SAME compilation unit;
//     CALL and EXEC PGM= targets are programs resolved ESTATE-WIDE by
//     declared program name (flat namespace, no imports involved).
//   - A dynamic CALL through a variable is constant-propagated within the
//     unit: if the variable's declaration carries a VALUE 'LITERAL' clause,
//     the literal is the candidate program name, at reduced confidence
//     (spec R-5.2, intra-unit propagation).

var reValueLiteral = regexp.MustCompile(`(?i)\bVALUE\s+(?:IS\s+)?['"]([^'"]+)['"]`)

func mainframeCallerKind(kind core.SymbolKind) bool {
	switch string(kind) {
	case "paragraph", "section", "program", "step", "job", "jcl-procedure":
		return true
	}
	return false
}

func resolveMainframeCallEdges(idx *edgeIndex, symbol core.SymbolRecord) []core.Edge {
	if !mainframeCallerKind(symbol.Kind) {
		return nil
	}
	var edges []core.Edge
	seen := make(map[string]bool)
	add := func(toID string, confidence float64, source core.EvidenceSource, reason core.EdgeReason) {
		key := symbol.ID + "::calls::" + toID
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{
			From: symbol.ID, To: toID,
			Type: core.EdgeCalls, Confidence: confidence, Source: source, Reason: reason,
		})
	}

	for _, cs := range symbol.CallSites {
		name := strings.ToLower(cs.Callee)
		dynamic := len(cs.Args) == 1 && cs.Args[0] == "dynamic"

		if dynamic {
			// Trace the variable's VALUE clause within this unit.
			for _, cand := range idx.byName[name] {
				if cand.FilePath != symbol.FilePath || string(cand.Kind) != "data-item" {
					continue
				}
				if m := reValueLiteral.FindStringSubmatch(cand.Signature); m != nil {
					target := strings.ToLower(strings.TrimSpace(m[1]))
					for _, prog := range idx.byName[target] {
						if string(prog.Kind) == "program" {
							add(prog.ID, 0.6, core.EvidenceSourceHeuristic, core.ReasonDispatch)
						}
					}
				}
			}
			continue
		}

		// Same-unit PERFORM targets first (narrowest scope wins).
		resolved := false
		for _, cand := range idx.byName[name] {
			k := string(cand.Kind)
			if cand.FilePath == symbol.FilePath && (k == "paragraph" || k == "section") {
				add(cand.ID, 1.0, core.EvidenceSourceASTKit, core.ReasonASTNarrowed)
				resolved = true
			}
		}
		if resolved {
			continue
		}
		// Estate-wide program / JCL-procedure resolution by declared name.
		for _, cand := range idx.byName[name] {
			k := string(cand.Kind)
			if k == "program" || k == "jcl-procedure" {
				add(cand.ID, 0.9, core.EvidenceSourceASTKit, core.ReasonASTNarrowed)
			}
		}
	}
	return edges
}

// mainframeDataKind: kinds that anchor lineage queries (declared storage).
func mainframeDataKind(kind core.SymbolKind) bool {
	switch string(kind) {
	case "data-item", "condition-name", "logical-file", "dataset":
		return true
	}
	return false
}

// mainframeAnchorKind: every mainframe kind change-impact accepts as anchor.
func mainframeAnchorKind(kind core.SymbolKind) bool {
	return mainframeCallerKind(kind) || mainframeDataKind(kind)
}

// hasMainframeSymbols reports whether any symbol is cobol/jcl — used by the
// delta path to fall back to a full edge build until include-closure-aware
// invalidation (plan phase 0.4) lands. Estates are few-files corpora; a full
// rebuild is cheap there, and silence-losing data edges is not acceptable.
func hasMainframeSymbols(symbols []core.SymbolRecord) bool {
	for i := range symbols {
		if symbols[i].Language == "cobol" || symbols[i].Language == "jcl" {
			return true
		}
	}
	return false
}

var (
	reFieldToken     = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9-]+`)
	reQuoted         = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	reAssignTo       = regexp.MustCompile(`(?i)ASSIGN\s+TO\s+([A-Za-z0-9-]+)`)
	reFieldQualifier = regexp.MustCompile(`(?i)^\s+(?:OF|IN)\s+([A-Za-z0-9-]+)`)
)

// mainframeAccessAt classifies one field occurrence by its position in a
// COBOL statement. COBOL verbs frequently read and write different operands,
// so treating an entire captured tail as a write target loses lineage (for
// example WRITE dst FROM src and ADD a TO b GIVING c).
func mainframeAccessAt(statement string, tokenStart int) (read, write bool) {
	upper := strings.ToUpper(statement)
	fields := strings.Fields(upper)
	if len(fields) == 0 {
		return true, false
	}
	verb := strings.Trim(fields[0], ".")
	position := func(keyword string) int { return strings.Index(upper, keyword) }
	after := func(keyword string) bool {
		pos := position(keyword)
		return pos >= 0 && tokenStart > pos
	}

	switch verb {
	case "MOVE":
		return !after(" TO "), after(" TO ")
	case "ADD":
		if position(" GIVING ") >= 0 {
			return !after(" GIVING "), after(" GIVING ")
		}
		if after(" TO ") {
			return true, true
		}
		return true, false
	case "SUBTRACT":
		if position(" GIVING ") >= 0 {
			return !after(" GIVING "), after(" GIVING ")
		}
		if after(" FROM ") {
			return true, true
		}
		return true, false
	case "COMPUTE":
		if eq := strings.IndexByte(upper, '='); eq >= 0 {
			return tokenStart > eq, tokenStart < eq
		}
		return false, true
	case "INITIALIZE", "ACCEPT":
		return false, true
	case "SET":
		to := position(" TO ")
		if to < 0 {
			return false, true
		}
		return tokenStart > to, tokenStart < to
	case "STRING", "UNSTRING", "READ":
		return !after(" INTO "), after(" INTO ")
	case "WRITE", "REWRITE":
		return after(" FROM "), !after(" FROM ")
	case "INSPECT":
		tallying := position(" TALLYING ")
		replacing := position(" REPLACING ")
		firstClause := len(upper)
		if tallying >= 0 && tallying < firstClause {
			firstClause = tallying
		}
		if replacing >= 0 && replacing < firstClause {
			firstClause = replacing
		}
		if tokenStart < firstClause {
			return true, replacing >= 0
		}
		if tallying >= 0 && tokenStart > tallying {
			forPos := position(" FOR ")
			if forPos < 0 || tokenStart < forPos {
				return false, true
			}
		}
		return true, false
	default:
		return true, false
	}
}

func mainframeQualifiedField(fields []*core.SymbolRecord, statement string, tokenEnd int) []*core.SymbolRecord {
	match := reFieldQualifier.FindStringSubmatch(statement[tokenEnd:])
	if len(match) == 0 {
		return fields
	}
	qualifier := strings.ToUpper(match[1])
	var matched []*core.SymbolRecord
	for _, field := range fields {
		parent := strings.ToUpper(field.ParentSymbol)
		qualified := strings.ToUpper(field.QualifiedName)
		if parent == qualifier || strings.HasSuffix(parent, "."+qualifier) ||
			strings.Contains(qualified, "."+qualifier+".") {
			matched = append(matched, field)
		}
	}
	if len(matched) > 0 {
		return matched
	}
	return fields
}

// buildMainframeDataEdges emits the lineage layer:
//
//  1. Resolved include edges: file:A -> file:B when A's COPY member name
//     matches B's basename (the member/file join the bare "import:MEMBER"
//     record cannot make). The unresolved import edge is kept — it is the
//     honest record that the reference exists even when the member is absent.
//  2. Field-reference edges (uses-type, 0.7, regex): paragraph/section body
//     tokens matched against data items VISIBLE to that file — declared in
//     it, or in the transitive closure of its resolved copybooks. Operand
//     position supplies read/write direction and OF/IN qualifiers disambiguate
//     duplicate leaf names.
func buildMainframeDataEdges(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	// A COBOL program enters the first paragraph/section of PROCEDURE
	// DIVISION even when there is no explicit PERFORM. Model that control-flow
	// edge so dead-code and impact do not report every program entry paragraph
	// as unreachable.
	firstProcedure := map[string]*core.SymbolRecord{}
	programs := map[string]*core.SymbolRecord{}
	for i := range symbols {
		symbol := &symbols[i]
		if symbol.Language != "cobol" {
			continue
		}
		if string(symbol.Kind) == "program" {
			programs[symbol.FilePath+"\x00"+strings.ToUpper(symbol.Name)] = symbol
			continue
		}
		if string(symbol.Kind) != "paragraph" && string(symbol.Kind) != "section" {
			continue
		}
		key := symbol.FilePath + "\x00" + strings.ToUpper(symbol.ParentSymbol)
		if current := firstProcedure[key]; current == nil || symbol.Span.Start < current.Span.Start {
			firstProcedure[key] = symbol
		}
	}

	// Member name -> file path, for cobol files only.
	memberFile := map[string]string{}
	cobolFiles := map[string][]string{} // filePath -> imports
	for i := range symbols {
		s := &symbols[i]
		if s.Language != "cobol" {
			continue
		}
		base := s.FilePath
		if j := strings.LastIndexByte(base, '/'); j >= 0 {
			base = base[j+1:]
		}
		if j := strings.IndexByte(base, '.'); j >= 0 {
			base = base[:j]
		}
		memberFile[strings.ToUpper(base)] = s.FilePath
		if _, ok := cobolFiles[s.FilePath]; !ok {
			cobolFiles[s.FilePath] = s.Imports
		}
	}
	if len(cobolFiles) == 0 {
		return nil
	}

	var edges []core.Edge
	for key, first := range firstProcedure {
		if program := programs[key]; program != nil {
			edges = append(edges, core.Edge{
				From: program.ID, To: first.ID,
				Type: core.EdgeCalls, Confidence: 1.0,
				Source: core.EvidenceSourceASTKit, Reason: core.ReasonStructural,
			})
		}
	}
	// 1. Resolved include edges + per-file include closure.
	closure := map[string]map[string]bool{} // filePath -> reachable member files
	var expand func(file string, seen map[string]bool)
	expand = func(file string, seen map[string]bool) {
		for _, imp := range cobolFiles[file] {
			target, ok := memberFile[strings.ToUpper(imp)]
			if !ok || seen[target] || target == file {
				continue
			}
			seen[target] = true
			expand(target, seen)
		}
	}
	seenInc := map[string]bool{}
	for file := range cobolFiles {
		seen := map[string]bool{}
		expand(file, seen)
		closure[file] = seen
		for _, imp := range cobolFiles[file] {
			if target, ok := memberFile[strings.ToUpper(imp)]; ok && target != file {
				key := file + "->" + target
				if seenInc[key] {
					continue
				}
				seenInc[key] = true
				edges = append(edges, core.Edge{
					From: "file:" + file, To: "file:" + target,
					Type: core.EdgeImports, Confidence: 1.0,
					Source: core.EvidenceSourceASTKit, Reason: core.ReasonASTNarrowed,
				})
			}
		}
	}

	// 2. Directional field references from paragraph/section bodies.
	//
	// Statement verbs classify direction: MOVE/COMPUTE/ADD/SUBTRACT/SET/
	// INITIALIZE/GIVING/INTO/VARYING targets are WRITES; every other
	// referenced field in the statement is a READ. Quoted literals are
	// stripped first so 'ACCT-NOT-FOUND' never matches a field.
	//
	// References remain owned by their paragraph/section. Rolling same-file
	// fields up to the program destroys the caller identity used by impact,
	// rename, and lineage queries. Deduplication below still bounds repeated
	// references within one owner.
	fieldsByFile := map[string]map[string][]*core.SymbolRecord{}
	fieldMap := func(file string) map[string][]*core.SymbolRecord {
		if m, ok := fieldsByFile[file]; ok {
			return m
		}
		m := map[string][]*core.SymbolRecord{}
		addFrom := func(f string) {
			for _, s := range idx.byFile[f] {
				if mainframeDataKind(s.Kind) {
					m[strings.ToUpper(s.Name)] = append(m[strings.ToUpper(s.Name)], s)
				}
			}
		}
		addFrom(file)
		for inc := range closure[file] {
			addFrom(inc)
		}
		fieldsByFile[file] = m
		return m
	}
	programOfFile := map[string]*core.SymbolRecord{}
	for i := range symbols {
		s := &symbols[i]
		if string(s.Kind) == "program" {
			programOfFile[s.FilePath] = s
		}
	}

	seenRef := map[string]bool{}
	emitRef := func(from *core.SymbolRecord, field *core.SymbolRecord, write bool) {
		et := core.EdgeReads
		if write {
			et = core.EdgeWrites
		}
		key := from.ID + string(et) + field.ID
		if seenRef[key] {
			return
		}
		seenRef[key] = true
		edges = append(edges, core.Edge{
			From: from.ID, To: field.ID,
			Type: et, Confidence: 0.7,
			Source: core.EvidenceSourceRegex, Reason: core.ReasonRegexFallbck,
		})
	}

	for i := range symbols {
		s := &symbols[i]
		if s.Language != "cobol" || !mainframeCallerKind(s.Kind) || s.RawText == "" {
			continue
		}
		visible := fieldMap(s.FilePath)
		for _, stmt := range strings.Split(s.RawText, "\n") {
			stmt = reQuoted.ReplaceAllString(stmt, " ")
			for _, loc := range reFieldToken.FindAllStringIndex(stmt, -1) {
				tok := stmt[loc[0]:loc[1]]
				u := strings.ToUpper(tok)
				fields := visible[u]
				if len(fields) == 0 {
					continue
				}
				fields = mainframeQualifiedField(fields, stmt, loc[1])
				read, write := mainframeAccessAt(stmt, loc[0])
				for _, field := range fields {
					if read {
						emitRef(s, field, false)
					}
					if write {
						emitRef(s, field, true)
					}
				}
			}
		}
	}

	// 3. REDEFINES edges: the alternate-view relation, resolved within the
	// declaring file (a redefinition legally targets a preceding sibling).
	for i := range symbols {
		s := &symbols[i]
		if s.Language != "cobol" || !mainframeDataKind(s.Kind) {
			continue
		}
		for _, mod := range s.Modifiers {
			if !strings.HasPrefix(mod, "redefines:") {
				continue
			}
			target := strings.ToUpper(strings.TrimPrefix(mod, "redefines:"))
			for _, cand := range idx.byFile[s.FilePath] {
				if mainframeDataKind(cand.Kind) && strings.ToUpper(cand.Name) == target && cand.ID != s.ID {
					edges = append(edges, core.Edge{
						From: s.ID, To: cand.ID,
						Type: core.EdgeRedefines, Confidence: 1.0,
						Source: core.EvidenceSourceASTKit, Reason: core.ReasonStructural,
					})
				}
			}
		}
	}

	// 4. Cross-artifact dataset binding (spec R-5.3): program declares
	// logical file ASSIGN TO <dd>; a JCL step executes the program and its
	// <dd> DD names a dataset. The derived edge joins the two artifacts at
	// reduced confidence and cites the join in its reason.
	datasetsByStepDD := map[string][]*core.SymbolRecord{}
	for i := range symbols {
		s := &symbols[i]
		if string(s.Kind) != "dataset" {
			continue
		}
		for _, mod := range s.Modifiers {
			if strings.HasPrefix(mod, "dd:") {
				key := strings.ToUpper(s.ParentSymbol) + "/" + strings.TrimPrefix(mod, "dd:")
				datasetsByStepDD[key] = append(datasetsByStepDD[key], s)
			}
		}
	}
	if len(datasetsByStepDD) > 0 {
		// Steps that execute each program, from the call edges built above
		// plus estate-wide name resolution (same rule as call resolution).
		stepsByProgram := map[string][]*core.SymbolRecord{}
		for i := range symbols {
			s := &symbols[i]
			if string(s.Kind) != "step" {
				continue
			}
			for _, cs := range s.CallSites {
				stepsByProgram[strings.ToUpper(cs.Callee)] = append(stepsByProgram[strings.ToUpper(cs.Callee)], s)
			}
		}
		seenBind := map[string]bool{}
		for i := range symbols {
			lf := &symbols[i]
			if string(lf.Kind) != "logical-file" {
				continue
			}
			dd := ""
			if m := reAssignTo.FindStringSubmatch(lf.Signature); m != nil {
				dd = strings.ToUpper(m[1])
			}
			if dd == "" {
				continue
			}
			prog := programOfFile[lf.FilePath]
			if prog == nil {
				continue
			}
			for _, step := range stepsByProgram[strings.ToUpper(prog.Name)] {
				for _, ds := range datasetsByStepDD[strings.ToUpper(step.Name)+"/"+dd] {
					key := lf.ID + "->" + ds.ID
					if seenBind[key] {
						continue
					}
					seenBind[key] = true
					edges = append(edges, core.Edge{
						From: lf.ID, To: ds.ID,
						Type: core.EdgeBinds, Confidence: 0.8,
						Source: core.EvidenceSourceASTKit, Reason: core.ReasonCrossArtifact,
					})
				}
			}
		}
	}

	return edges
}

// mainframeImpactLocked answers change-impact for mainframe anchors. The
// modern Type.member machinery cannot represent them: a data item's dotted
// qualified name is a HIERARCHY path (group.field), not a type family, and
// COBOL names are case-insensitive. Declarations match by name or qualified
// name; impact sites are inbound call edges plus — for data anchors —
// inbound field-reference (uses-type) edges. Completeness is "callers-only":
// reachability, not a closed change-set.
func (g *CodeGraph) mainframeImpactLocked(query string) *ChangeImpactResult {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var decls []core.SymbolRecord
	declIDs := map[string]bool{}
	dataAnchor := false
	for id := range g.symbols {
		s := g.symbols[id]
		if !mainframeAnchorKind(s.Kind) {
			continue
		}
		if strings.ToLower(s.QualifiedName) != q && strings.ToLower(s.Name) != q {
			continue
		}
		decls = append(decls, s)
		declIDs[s.ID] = true
		if mainframeDataKind(s.Kind) {
			dataAnchor = true
		}
	}
	if len(decls) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var callers []core.SymbolRecord
	for id := range declIDs {
		for _, ei := range g.inbound[id] {
			edge := g.edges[ei]
			ok := edge.Type == core.EdgeCalls ||
				(dataAnchor && (edge.Type == core.EdgeReads || edge.Type == core.EdgeWrites ||
					edge.Type == core.EdgeRedefines || edge.Type == core.EdgeBinds))
			if !ok || declIDs[edge.From] || seen[edge.From] {
				continue
			}
			seen[edge.From] = true
			if s, found := g.symbols[edge.From]; found {
				callers = append(callers, s)
			}
		}
	}
	sortSymbols(decls)
	sortSymbols(callers)
	return &ChangeImpactResult{
		Query:        query,
		Declarations: decls,
		Callers:      callers,
		Completeness: "callers-only",
	}
}
