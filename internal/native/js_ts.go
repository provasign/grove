package native

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type jsTSAnalyzer struct{}

// neutralNodeCWD is a directory OUTSIDE any indexed repository. Node resolves
// `require('typescript')` by walking node_modules UP from the process working
// directory, so running node here (instead of in the repo) guarantees the
// repo's own node_modules — where a hostile clone plants a malicious
// `typescript` whose entry point runs on require() — is never on the
// resolution path. Only a typescript installed outside the repo (a trusted
// global/toolchain install) can be loaded; if none exists the analyzer
// degrades to the tree-sitter path. The repo root is passed to the script as
// GROVE_ROOT for file access, never as the module-resolution cwd.
func neutralNodeCWD() string { return os.TempDir() }

func (jsTSAnalyzer) Name() string { return "js-ts" }

func (jsTSAnalyzer) Languages() []string {
	return []string{"javascript", "typescript", "tsx"}
}

// untrustedMode reports whether the operator has asked grove to treat the
// indexed tree as UNTRUSTED (reviewing a hostile PR, an unvetted dependency).
// Default is trusted: grove is normally pointed at your own project — the same
// code your editor, eslint, and tsc already load — so it loads the project's
// typescript like any dev tool. Untrusted mode refuses repo-resident
// toolchains (a hostile node_modules/typescript executes on require()) and
// cuts network access. Set GROVE_UNTRUSTED=1 (or prism's --untrusted).
// Independent of this, secrets are ALWAYS scrubbed from analyzer subprocesses.
func untrustedMode() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GROVE_UNTRUSTED")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// tsUntrustedSkipReason explains why type-analysis is skipped in untrusted
// mode, so an operator does not mistake the tree-sitter degrade for a bug.
const tsUntrustedSkipReason = "TypeScript type-analysis skipped: untrusted mode will not load this repo's own typescript (it could run code on load). Tree-sitter symbol/structure analysis IS active. For full type-resolved edges, index this repo without GROVE_UNTRUSTED, or install typescript outside the repo."

func (jsTSAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "package.json", "tsconfig.json", "jsconfig.json") {
		return Availability{Reason: "no package.json, tsconfig.json, or jsconfig.json"}
	}
	if !commandExists("node") {
		return Availability{Reason: "node executable not found"}
	}
	// Trusted (default): resolve typescript from the repo like a normal dev
	// tool. Untrusted: resolve from a neutral cwd so a repo-resident typescript
	// is never on the module path.
	cwd := root
	if untrustedMode() {
		cwd = neutralNodeCWD()
	}
	cmd := exec.Command("node", "-e", "require.resolve('typescript')")
	cmd.Dir = cwd
	cmd.Env = scrubbedEnv()
	if err := cmd.Run(); err != nil {
		if untrustedMode() {
			return Availability{Reason: tsUntrustedSkipReason}
		}
		return Availability{Reason: "typescript not resolvable in the project"}
	}
	return Availability{Available: true}
}

func (jsTSAnalyzer) Analyze(ctx context.Context, req Request) Result {
	filesJSON, err := jsonMarshal(req.Files)
	if err != nil {
		return Result{Diagnostics: []string{"failed to encode file list: " + err.Error()}}
	}
	cmd := exec.CommandContext(ctx, "node", "-e", `
const ts = require('typescript');
const path = require('path');
const root = process.env.GROVE_ROOT;
const inputFiles = JSON.parse(process.env.GROVE_FILES || '[]');
const input = new Set(inputFiles.map(f => path.resolve(root, f)));
const edges = [];
const calls = [];
const types = [];
const edgeKeys = new Set();
const callKeys = new Set();
const typeKeys = new Set();
const configErrors = [];
const configByPath = new Map();
const configs = [];
const rootAbs = path.resolve(root);
function rel(abs) { return path.relative(root, path.resolve(abs)).split(path.sep).join('/'); }
function insideRoot(abs) {
  const r = path.relative(rootAbs, path.resolve(abs));
  return r === '' || (r !== '..' && !r.startsWith('..' + path.sep) && !path.isAbsolute(r));
}
function exactConfig(dir) {
  for (const name of ['tsconfig.json', 'jsconfig.json']) {
    const candidate = path.join(dir, name);
    if (ts.sys.fileExists(candidate)) return candidate;
  }
  return undefined;
}
function nearestConfig(file) {
  let dir = path.dirname(path.resolve(file));
  while (insideRoot(dir)) {
    const found = exactConfig(dir);
    if (found) return found;
    if (dir === rootAbs) break;
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  return undefined;
}
function referenceConfig(ref, baseDir) {
  let target = typeof ts.resolveProjectReferencePath === 'function'
    ? ts.resolveProjectReferencePath(ref)
    : path.resolve(baseDir, ref.path);
  target = path.resolve(target);
  if (ts.sys.fileExists(target)) return target;
  const nested = path.join(target, 'tsconfig.json');
  return ts.sys.fileExists(nested) ? nested : target;
}
function addConfig(cfg) {
  if (!cfg) return undefined;
  cfg = path.resolve(cfg);
  if (!insideRoot(cfg) || configByPath.has(cfg)) return configByPath.get(cfg);
  // Mark before parsing so circular project-reference graphs terminate.
  configByPath.set(cfg, undefined);
  const read = ts.readConfigFile(cfg, ts.sys.readFile);
  if (read.error) {
    configErrors.push(path.relative(rootAbs, cfg) + ': unreadable or invalid config');
    return undefined;
  }
  const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, path.dirname(cfg), undefined, cfg);
  const project = {cfg, parsed};
  configByPath.set(cfg, project);
  configs.push(project);
  for (const ref of parsed.projectReferences || []) addConfig(referenceConfig(ref, path.dirname(cfg)));
  return project;
}
addConfig(exactConfig(rootAbs));
for (const relPath of inputFiles) addConfig(nearestConfig(path.resolve(rootAbs, relPath)));
if (configs.length === 0) {
  console.log(JSON.stringify({files: 0, configs: 0, solutionConfigs: 0, configErrors,
    edges: [], calls: [], types: []}));
  process.exit(0);
}
const projectByInput = new Map();
for (const project of configs) {
  for (const file of project.parsed.fileNames) {
    const abs = path.resolve(file);
    if (!input.has(abs)) continue;
    const current = projectByInput.get(abs);
    if (!current || path.dirname(project.cfg).length > path.dirname(current.cfg).length) {
      projectByInput.set(abs, project);
    }
  }
}
function projectForFile(abs) {
  abs = path.resolve(abs);
  const owning = projectByInput.get(abs);
  if (owning) return owning;
  const cfg = nearestConfig(abs);
  return cfg && configByPath.get(path.resolve(cfg));
}
function projectHost(project) {
  if (!project.host) project.host = ts.createCompilerHost(project.parsed.options, true);
  return project.host;
}
function addModuleEdge(spec, abs, from, options, host) {
  if (!spec) return;
  const resolved = ts.resolveModuleName(spec, abs, options, host).resolvedModule;
  if (!resolved || !resolved.resolvedFileName) return;
  const targetRel = rel(resolved.resolvedFileName);
  if (targetRel.startsWith('..') || path.isAbsolute(targetRel)) return;
  const key = from + '\0' + targetRel;
  if (!edgeKeys.has(key)) {
    edgeKeys.add(key);
    edges.push({from, to: targetRel});
  }
}
function isCallableNode(node) {
  return ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node) ||
    ts.isConstructorDeclaration(node) || ts.isGetAccessor(node) ||
    ts.isSetAccessor(node) || ts.isFunctionExpression(node) || ts.isArrowFunction(node);
}
function hasCallableAncestor(node) {
  for (let parent = node && node.parent; parent; parent = parent.parent) {
    if (isCallableNode(parent)) return true;
  }
  return false;
}
function declInfo(checker, sym) {
  if (sym && sym.flags & ts.SymbolFlags.Alias) sym = checker.getAliasedSymbol(sym);
  if (!sym || !sym.declarations || !sym.declarations.length) return undefined;
	const decl = sym.declarations.find(d => d.body) || sym.valueDeclaration || sym.declarations[0];
  if (ts.isVariableDeclaration(decl) && decl.initializer &&
      (ts.isArrowFunction(decl.initializer) || ts.isFunctionExpression(decl.initializer)) &&
      hasCallableAncestor(decl)) return undefined;
  const sf = decl.getSourceFile();
  const name = sym.getName && sym.getName();
  if (!sf || !name || name === '__function') return undefined;
  const r = rel(sf.fileName);
  if (r.startsWith('..') || path.isAbsolute(r)) return undefined;
  const line = sf.getLineAndCharacterOfPosition(decl.getStart(sf)).line + 1;
  return {file: r, name, line};
}
function currentName(sf, stack) {
  for (let i = stack.length - 1; i >= 0; i--) {
    const n = stack[i];
    let name = undefined;
    if ((ts.isFunctionDeclaration(n) || ts.isClassDeclaration(n) || ts.isInterfaceDeclaration(n) || ts.isTypeAliasDeclaration(n)) && n.name) name = n.name.text;
    else if (ts.isMethodDeclaration(n) && n.name && ts.isIdentifier(n.name)) name = n.name.text;
	else if (ts.isConstructorDeclaration(n)) name = 'constructor';
	else if ((ts.isGetAccessor(n) || ts.isSetAccessor(n)) && n.name && ts.isIdentifier(n.name)) name = n.name.text;
    else if (ts.isPropertyDeclaration(n) && n.name && ts.isIdentifier(n.name) && n.initializer &&
             (ts.isArrowFunction(n.initializer) || ts.isFunctionExpression(n.initializer))) name = n.name.text;
    else if (ts.isVariableDeclaration(n) && n.name && ts.isIdentifier(n.name) && n.initializer &&
	         (ts.isArrowFunction(n.initializer) || ts.isFunctionExpression(n.initializer))) {
	  // A named function value at module scope is a Grove symbol. Inside a
	  // method/function it is a closure; attribute its calls to the enclosing
	  // declaration. Falling back by its local name can otherwise bind to an
	  // unrelated method with the same name (for example a local onClose arrow
	  // becoming Socket.onClose).
	  let nested = false;
	  for (let j = i - 1; j >= 0; j--) {
	    if (isCallableNode(stack[j])) {
	      nested = true;
	      break;
	    }
	  }
	  if (!nested) name = n.name.text;
	}
    if (name) return {name, line: sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1};
  }
  return undefined;
}
function visit(checker, options, host, sf, node, stack) {
	if (ts.isCallExpression(node) && node.arguments.length > 0 && ts.isStringLiteralLike(node.arguments[0])) {
	  const expr = node.expression;
	  if ((ts.isIdentifier(expr) && expr.text === 'require') || expr.kind === ts.SyntaxKind.ImportKeyword) {
	    addModuleEdge(node.arguments[0].text, sf.fileName, rel(sf.fileName), options, host);
	  }
	}
  const enclosing = currentName(sf, stack);
  const fromName = enclosing && enclosing.name;
  const fromLine = enclosing && enclosing.line;
  if (fromName && ts.isCallExpression(node)) {
    const expr = ts.isPropertyAccessExpression(node.expression) ? node.expression.name : node.expression;
    const target = declInfo(checker, checker.getSymbolAtLocation(expr));
    if (target) {
      const call = {from: rel(sf.fileName), fromName, fromLine, to: target.file, toName: target.name, toLine: target.line};
      const key = JSON.stringify(call);
      if (!callKeys.has(key)) { callKeys.add(key); calls.push(call); }
    }
  }
  if (fromName && (ts.isTypeReferenceNode(node) || ts.isExpressionWithTypeArguments(node))) {
    const typeNode = ts.isTypeReferenceNode(node) ? node.typeName : node.expression;
    const nameNode = ts.isQualifiedName(typeNode) ? typeNode.right : typeNode;
    const target = declInfo(checker, checker.getSymbolAtLocation(nameNode));
    if (target) {
      const typeUse = {from: rel(sf.fileName), fromName, fromLine, to: target.file, toName: target.name, toLine: target.line};
      const key = JSON.stringify(typeUse);
      if (!typeKeys.has(key)) { typeKeys.add(key); types.push(typeUse); }
    }
  }
  const next = stack.concat(node);
  ts.forEachChild(node, child => visit(checker, options, host, sf, child, next));
}
// Import resolution does not require a Program. Resolve it for every indexed
// input using that file's nearest project options, even if a broken config
// leaves the file outside the Program; call/type resolution below still makes
// the distinction visible in diagnostics.
for (const relPath of inputFiles) {
  const abs = path.resolve(root, relPath);
  const source = ts.sys.readFile(abs);
  if (!source) continue;
  const project = projectForFile(abs);
  if (!project) continue;
  const options = project.parsed.options;
  const host = projectHost(project);
  const sf = ts.createSourceFile(abs, source, options.target || ts.ScriptTarget.Latest, false);
  for (const stmt of sf.statements) {
    let spec = undefined;
    if (ts.isImportDeclaration(stmt) || ts.isExportDeclaration(stmt)) {
      spec = stmt.moduleSpecifier && stmt.moduleSpecifier.text;
    } else if (ts.isImportEqualsDeclaration(stmt) && ts.isExternalModuleReference(stmt.moduleReference)) {
      spec = stmt.moduleReference.expression && stmt.moduleReference.expression.text;
    }
	if (spec) addModuleEdge(spec, abs, relPath, options, host);
  }
}
const loadedFiles = new Set();
let solutionConfigs = 0;
for (const project of configs) {
  const parsed = project.parsed;
  if (parsed.fileNames.length === 0) {
    if ((parsed.projectReferences || []).length > 0) solutionConfigs++;
    continue;
  }
  const host = projectHost(project);
  const program = ts.createProgram({rootNames: parsed.fileNames, options: parsed.options,
    projectReferences: parsed.projectReferences, host});
  const checker = program.getTypeChecker();
  for (const sf of program.getSourceFiles()) {
    if (sf.isDeclarationFile || !input.has(path.resolve(sf.fileName))) continue;
    loadedFiles.add(path.resolve(sf.fileName));
    visit(checker, parsed.options, host, sf, sf, []);
  }
}
console.log(JSON.stringify({files: loadedFiles.size, configs: configs.length, solutionConfigs,
  configErrors, edges, calls, types}));
`)
	// Trusted (default): run node in the repo so its own typescript loads,
	// like any dev tool. Untrusted: run from a neutral cwd so the repo's
	// node_modules is never on the module-resolution path; the repo root still
	// reaches the script via GROVE_ROOT. appendEnv scrubs grove's secrets from
	// the subprocess environment in BOTH modes.
	cmd.Dir = req.Root
	if untrustedMode() {
		cmd.Dir = neutralNodeCWD()
	}
	cmd.Env = appendEnv("GROVE_FILES="+string(filesJSON), "GROVE_ROOT="+req.Root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return Result{Diagnostics: []string{"typescript language service bootstrap failed: " + err.Error() + ": " + detail}}
		}
		return Result{Diagnostics: []string{"typescript language service bootstrap failed: " + err.Error()}}
	}
	var payload struct {
		Files           int      `json:"files"`
		Configs         int      `json:"configs"`
		SolutionConfigs int      `json:"solutionConfigs"`
		ConfigErrors    []string `json:"configErrors"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
		Calls []struct {
			From     string `json:"from"`
			FromName string `json:"fromName"`
			FromLine int    `json:"fromLine"`
			To       string `json:"to"`
			ToName   string `json:"toName"`
			ToLine   int    `json:"toLine"`
		} `json:"calls"`
		Types []struct {
			From     string `json:"from"`
			FromName string `json:"fromName"`
			FromLine int    `json:"fromLine"`
			To       string `json:"to"`
			ToName   string `json:"toName"`
			ToLine   int    `json:"toLine"`
		} `json:"types"`
	}
	if err := unmarshalJSON(out, &payload); err != nil {
		return Result{Diagnostics: []string{"typescript resolver JSON decode failed: " + err.Error()}}
	}
	fileScope := fileSet(req.Files)
	symbols := newSymbolLocator(req.Symbols, map[string]bool{"javascript": true, "typescript": true, "tsx": true})
	edges := make([]core.Edge, 0, len(payload.Edges)+len(payload.Calls)+len(payload.Types))
	for _, edge := range payload.Edges {
		from := strings.TrimSpace(edge.From)
		to := strings.TrimSpace(edge.To)
		if from == "" || to == "" || !fileScope[from] {
			continue
		}
		edges = append(edges, nativeImportEdge(from, to, 0.97))
	}
	for _, edge := range payload.Calls {
		from, okFrom := symbols.at(edge.From, edge.FromName, edge.FromLine)
		to, okTo := symbols.at(edge.To, edge.ToName, edge.ToLine)
		if okFrom && okTo && from.ID != to.ID {
			edges = append(edges, symbolEdge(from, to, core.EdgeCalls, 0.98))
		}
	}
	for _, edge := range payload.Types {
		from, okFrom := symbols.at(edge.From, edge.FromName, edge.FromLine)
		to, okTo := symbols.at(edge.To, edge.ToName, edge.ToLine)
		if okFrom && okTo && from.ID != to.ID {
			edges = append(edges, symbolEdge(from, to, core.EdgeUsesType, 0.96))
		}
	}
	return Result{
		Edges: edges,
		Diagnostics: append([]string{
			"typescript projects loaded " + itoa(payload.Configs) + " config(s), including " + itoa(payload.SolutionConfigs) + " solution config(s), and " + itoa(payload.Files) + " indexed file(s)",
			"resolved " + itoa(len(payload.Edges)) + " native import candidate(s)",
			"resolved " + itoa(len(payload.Calls)) + " native call candidate(s)",
			"resolved " + itoa(len(payload.Types)) + " native type-use candidate(s)",
		}, payload.ConfigErrors...),
	}
}
