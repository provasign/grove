package native

import (
	"context"
	"os/exec"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type jsTSAnalyzer struct{}

func (jsTSAnalyzer) Name() string { return "js-ts" }

func (jsTSAnalyzer) Languages() []string {
	return []string{"javascript", "typescript", "tsx"}
}

func (jsTSAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "package.json", "tsconfig.json", "jsconfig.json") {
		return Availability{Reason: "no package.json, tsconfig.json, or jsconfig.json"}
	}
	if !commandExists("node") {
		return Availability{Reason: "node executable not found"}
	}
	cmd := exec.Command("node", "-e", "require.resolve('typescript')")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		return Availability{Reason: "project typescript package not resolvable"}
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
const root = process.cwd();
const inputFiles = JSON.parse(process.env.GROVE_FILES || '[]');
const cfg = ts.findConfigFile(process.cwd(), ts.sys.fileExists, 'tsconfig.json')
  || ts.findConfigFile(process.cwd(), ts.sys.fileExists, 'jsconfig.json');
if (!cfg) {
  console.log(JSON.stringify({files: 0, config: '', edges: [], calls: [], types: []}));
  process.exit(0);
}
const read = ts.readConfigFile(cfg, ts.sys.readFile);
const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, path.dirname(cfg));
const host = ts.createCompilerHost(parsed.options, true);
const program = ts.createProgram(parsed.fileNames, parsed.options, host);
const checker = program.getTypeChecker();
const input = new Set(inputFiles.map(f => path.resolve(root, f)));
const edges = [];
const calls = [];
const types = [];
function rel(abs) { return path.relative(root, path.resolve(abs)).split(path.sep).join('/'); }
function declInfo(sym) {
  if (!sym || !sym.declarations || !sym.declarations.length) return undefined;
  const decl = sym.declarations[0];
  const sf = decl.getSourceFile();
  const name = sym.getName && sym.getName();
  if (!sf || !name || name === '__function') return undefined;
  const r = rel(sf.fileName);
  if (r.startsWith('..') || path.isAbsolute(r)) return undefined;
  return {file: r, name};
}
function currentName(stack) {
  for (let i = stack.length - 1; i >= 0; i--) {
    const n = stack[i];
    if ((ts.isFunctionDeclaration(n) || ts.isClassDeclaration(n) || ts.isInterfaceDeclaration(n) || ts.isTypeAliasDeclaration(n)) && n.name) return n.name.text;
    if (ts.isMethodDeclaration(n) && n.name && ts.isIdentifier(n.name)) return n.name.text;
    if (ts.isVariableDeclaration(n) && n.name && ts.isIdentifier(n.name)) return n.name.text;
  }
  return undefined;
}
function visit(sf, node, stack) {
  const fromName = currentName(stack);
  if (fromName && ts.isCallExpression(node)) {
    const expr = ts.isPropertyAccessExpression(node.expression) ? node.expression.name : node.expression;
    const target = declInfo(checker.getSymbolAtLocation(expr));
    if (target) calls.push({from: rel(sf.fileName), fromName, to: target.file, toName: target.name});
  }
  if (fromName && (ts.isTypeReferenceNode(node) || ts.isExpressionWithTypeArguments(node))) {
    const typeNode = ts.isTypeReferenceNode(node) ? node.typeName : node.expression;
    const nameNode = ts.isQualifiedName(typeNode) ? typeNode.right : typeNode;
    const target = declInfo(checker.getSymbolAtLocation(nameNode));
    if (target) types.push({from: rel(sf.fileName), fromName, to: target.file, toName: target.name});
  }
  const next = stack.concat(node);
  ts.forEachChild(node, child => visit(sf, child, next));
}
for (const relPath of inputFiles) {
  const abs = path.resolve(root, relPath);
  const source = ts.sys.readFile(abs);
  if (!source) continue;
  const sf = ts.createSourceFile(abs, source, parsed.options.target || ts.ScriptTarget.Latest, false);
  for (const stmt of sf.statements) {
    let spec = undefined;
    if (ts.isImportDeclaration(stmt) || ts.isExportDeclaration(stmt)) {
      spec = stmt.moduleSpecifier && stmt.moduleSpecifier.text;
    } else if (ts.isImportEqualsDeclaration(stmt) && ts.isExternalModuleReference(stmt.moduleReference)) {
      spec = stmt.moduleReference.expression && stmt.moduleReference.expression.text;
    }
    if (!spec) continue;
    const resolved = ts.resolveModuleName(spec, abs, parsed.options, host).resolvedModule;
    if (!resolved || !resolved.resolvedFileName) continue;
    const targetRel = rel(resolved.resolvedFileName);
    if (targetRel.startsWith('..') || path.isAbsolute(targetRel)) continue;
    edges.push({from: relPath, to: targetRel});
  }
}
for (const sf of program.getSourceFiles()) {
  if (!sf.isDeclarationFile && input.has(path.resolve(sf.fileName))) visit(sf, sf, []);
}
console.log(JSON.stringify({files: parsed.fileNames.length, config: cfg, edges, calls, types}));
`)
	cmd.Dir = req.Root
	cmd.Env = appendEnv("GROVE_FILES=" + string(filesJSON))
	out, err := cmd.Output()
	if err != nil {
		return Result{Diagnostics: []string{"typescript language service bootstrap failed: " + err.Error()}}
	}
	var payload struct {
		Files int `json:"files"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
		Calls []struct {
			From     string `json:"from"`
			FromName string `json:"fromName"`
			To       string `json:"to"`
			ToName   string `json:"toName"`
		} `json:"calls"`
		Types []struct {
			From     string `json:"from"`
			FromName string `json:"fromName"`
			To       string `json:"to"`
			ToName   string `json:"toName"`
		} `json:"types"`
	}
	if err := unmarshalJSON(out, &payload); err != nil {
		return Result{Diagnostics: []string{"typescript resolver JSON decode failed: " + err.Error()}}
	}
	fileScope := fileSet(req.Files)
	symbols := symbolByFileAndName(req.Symbols, map[string]bool{"javascript": true, "typescript": true, "tsx": true})
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
		from, okFrom := symbols[edge.From+"\x00"+edge.FromName]
		to, okTo := symbols[edge.To+"\x00"+edge.ToName]
		if okFrom && okTo && from.ID != to.ID {
			edges = append(edges, symbolEdge(from, to, core.EdgeCalls, 0.98))
		}
	}
	for _, edge := range payload.Types {
		from, okFrom := symbols[edge.From+"\x00"+edge.FromName]
		to, okTo := symbols[edge.To+"\x00"+edge.ToName]
		if okFrom && okTo && from.ID != to.ID {
			edges = append(edges, symbolEdge(from, to, core.EdgeUsesType, 0.96))
		}
	}
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"typescript project loaded " + itoa(payload.Files) + " file(s)",
			"resolved " + itoa(len(payload.Edges)) + " native import candidate(s)",
			"resolved " + itoa(len(payload.Calls)) + " native call candidate(s)",
			"resolved " + itoa(len(payload.Types)) + " native type-use candidate(s)",
		},
	}
}
