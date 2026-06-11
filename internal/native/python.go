package native

import (
	"context"
	"os/exec"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type pythonAnalyzer struct{}

func (pythonAnalyzer) Name() string { return "python" }

func (pythonAnalyzer) Languages() []string { return []string{"python"} }

func (pythonAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "pyproject.toml", "setup.py", "setup.cfg", "requirements.txt") {
		return Availability{Reason: "no Python project config"}
	}
	if firstExistingExecutable("python3", "python") == "" {
		return Availability{Reason: "python executable not found"}
	}
	return Availability{Available: true}
}

func (pythonAnalyzer) Analyze(ctx context.Context, req Request) Result {
	filesJSON, err := jsonMarshal(req.Files)
	if err != nil {
		return Result{Diagnostics: []string{"failed to encode file list: " + err.Error()}}
	}
	name := firstExistingExecutable("python3", "python")
	cmd := exec.CommandContext(ctx, name, "-c", `
import ast, json, os, sys
from importlib.machinery import PathFinder
root = os.getcwd()
files = json.loads(os.environ.get("GROVE_FILES", "[]"))
sys.path.insert(0, root)
edges = []
calls = []
types = []
def type_name(node):
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute):
        return node.attr
    if isinstance(node, ast.Subscript):
        return type_name(node.value)
    return None
def find_spec_no_import(mod):
    # importlib.util.find_spec imports parent packages for dotted names,
    # which executes the repository's __init__.py at index time. Walk the
    # dotted path with PathFinder instead: pure filesystem resolution, no
    # code from the indexed repo ever runs.
    path = None
    spec = None
    for part in mod.split("."):
        try:
            spec = PathFinder.find_spec(part, path)
        except Exception:
            return None
        if spec is None:
            return None
        path = spec.submodule_search_locations
    return spec
for rel in files:
    path = os.path.join(root, rel)
    try:
        source = open(path, "r", encoding="utf-8").read()
        tree = ast.parse(source, filename=path)
    except Exception:
        continue
    mods = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            mods.extend(alias.name for alias in node.names)
        elif isinstance(node, ast.ImportFrom) and node.module:
            mods.append(node.module)
    for mod in mods:
        spec = find_spec_no_import(mod)
        origin = getattr(spec, "origin", None) if spec else None
        if not origin or origin in ("built-in", "frozen"):
            continue
        origin = os.path.realpath(origin)
        try:
            target = os.path.relpath(origin, root).replace(os.sep, "/")
        except ValueError:
            continue
        if target.startswith("../") or os.path.isabs(target):
            continue
        edges.append({"from": rel, "to": target})
    stack = []
    class Visitor(ast.NodeVisitor):
        def visit_FunctionDef(self, node):
            if stack and node.args.args:
                self_name = node.args.args[0].arg
            stack.append(node.name)
            for arg in node.args.args + node.args.kwonlyargs:
                name = type_name(arg.annotation)
                if name:
                    types.append({"from": rel, "fromName": node.name, "to": rel, "toName": name})
            name = type_name(node.returns)
            if name:
                types.append({"from": rel, "fromName": node.name, "to": rel, "toName": name})
            self.generic_visit(node)
            stack.pop()
        def visit_AsyncFunctionDef(self, node):
            self.visit_FunctionDef(node)
        def visit_ClassDef(self, node):
            for base in node.bases:
                name = type_name(base)
                if name:
                    types.append({"from": rel, "fromName": node.name, "to": rel, "toName": name})
            stack.append(node.name)
            self.generic_visit(node)
            stack.pop()
        def visit_Call(self, node):
            if stack:
                name = None
                if isinstance(node.func, ast.Name):
                    name = node.func.id
                elif isinstance(node.func, ast.Attribute):
                    name = node.func.attr
                if name:
                    calls.append({"from": rel, "fromName": stack[-1], "to": rel, "toName": name})
            self.generic_visit(node)
    Visitor().visit(tree)
print(json.dumps({"edges": edges, "calls": calls, "types": types}))
`)
	cmd.Dir = req.Root
	cmd.Env = appendEnv("GROVE_FILES=" + string(filesJSON))
	out, err := cmd.Output()
	if err != nil {
		return Result{Diagnostics: []string{name + " failed: " + err.Error()}}
	}
	var payload struct {
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
		return Result{Diagnostics: []string{"python resolver JSON decode failed: " + err.Error()}}
	}
	fileScope := fileSet(req.Files)
	symbols := symbolByFileAndName(req.Symbols, map[string]bool{"python": true})
	edges := make([]core.Edge, 0, len(payload.Edges)+len(payload.Calls)+len(payload.Types))
	for _, edge := range payload.Edges {
		from := strings.TrimSpace(edge.From)
		to := strings.TrimSpace(edge.To)
		if from == "" || to == "" || !fileScope[from] {
			continue
		}
		edges = append(edges, nativeImportEdge(from, to, 0.94))
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
			name + " resolved " + itoa(len(payload.Edges)) + " native import candidate(s)",
			name + " resolved " + itoa(len(payload.Calls)) + " native call candidate(s)",
			name + " resolved " + itoa(len(payload.Types)) + " native type-use candidate(s)",
		},
	}
}
