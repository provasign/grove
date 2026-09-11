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
file_set = set(files)
def local_module_file(rel, module, level):
    if not module and not level:
        return None
    module_path = (module or "").replace(".", "/")
    candidates = []
    if level:
        base = os.path.dirname(rel)
        for _ in range(level - 1):
            base = os.path.dirname(base)
        stem = os.path.normpath(os.path.join(base, module_path)).replace(os.sep, "/")
        candidates.extend((stem + ".py", stem + "/__init__.py"))
    elif module_path:
        candidates.extend((module_path + ".py", module_path + "/__init__.py"))
        suffixes = ("/" + module_path + ".py", "/" + module_path + "/__init__.py")
        candidates.extend(path for path in files if path.endswith(suffixes))
    matches = list(dict.fromkeys(path for path in candidates if path in file_set))
    return matches[0] if len(matches) == 1 else None
def type_names(node, out=None):
    # Every type an annotation references, not just its outermost name.
    # A PEP 604 union ("X | None") is an ast.BinOp and a parameterized
    # generic ("Optional[X]", "dict[str, X]") carries its argument in the
    # slice; the old outermost-name-only walk returned None for the first
    # and "Optional"/"dict" for the second, so both dropped the real type.
    # Measured on urllib3 (2026-09-07): every one of the 8 sites annotated
    # "ProxyConfig | None" was lost, leaving change-impact on ProxyConfig
    # reporting 1 site (its own declaration) for a type used in 7 files.
    if out is None:
        out = []
    if node is None:
        return out
    if isinstance(node, ast.Name):
        out.append(node.id)
    elif isinstance(node, ast.Attribute):
        out.append(node.attr)
    elif isinstance(node, ast.Subscript):
        type_names(node.value, out)
        type_names(node.slice, out)
    elif isinstance(node, ast.BinOp):
        type_names(node.left, out)
        type_names(node.right, out)
    elif isinstance(node, (ast.Tuple, ast.List)):
        for elt in node.elts:
            type_names(elt, out)
    elif isinstance(node, ast.Constant) and isinstance(node.value, str):
        # Forward reference: "ProxyConfig", or "ProxyConfig | None" under a
        # quoted annotation. ast.parse only builds a tree; nothing executes.
        try:
            type_names(ast.parse(node.value, mode="eval").body, out)
        except Exception:
            pass
    elif hasattr(ast, "Index") and isinstance(node, ast.Index):
        type_names(node.value, out)
    return out
def type_qualifiers(node, out=None):
    if out is None:
        out = {}
    if node is None:
        return out
    if isinstance(node, ast.Attribute):
        parts = []
        root = node
        while isinstance(root, ast.Attribute):
            parts.append(root.attr)
            root = root.value
        if isinstance(root, ast.Name):
            parts.append(root.id)
            parts.reverse()
            out[node.attr] = ".".join(parts[:-1])
    elif isinstance(node, ast.Constant) and isinstance(node.value, str):
        # A forward annotation stores its expression as a string constant.
        # Parse that expression just as type_names does so module qualifiers
        # survive a value: "models.Widget" annotation and pin homonyms.
        try:
            type_qualifiers(ast.parse(node.value, mode="eval").body, out)
        except Exception:
            pass
    for child in ast.iter_child_nodes(node):
        type_qualifiers(child, out)
    return out
def add_types(rel, from_name, from_line, node, seen, imported_names, imported_modules):
    qualifiers = type_qualifiers(node)
    for name in type_names(node):
        key = (from_name, name)
        if name and key not in seen:
            seen.add(key)
            imported = imported_names.get(name)
            if not imported:
                target_file = imported_modules.get(qualifiers.get(name))
                if target_file:
                    imported = (name, target_file)
            target_name, target_file = imported if imported else (name, rel)
            types.append({"from": rel, "fromName": from_name, "fromLine": from_line,
                          "to": target_file, "toName": target_name,
                          "localImport": imported is not None})
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
    imported_names = {}
    imported_modules = {}
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                target_file = local_module_file(rel, alias.name, 0)
                if target_file:
                    imported_modules[alias.asname or alias.name] = target_file
                    edges.append({"from": rel, "to": target_file})
                else:
                    mods.append(alias.name)
        elif isinstance(node, ast.ImportFrom):
            if node.module:
                target_file = local_module_file(rel, node.module, node.level)
                if target_file:
                    edges.append({"from": rel, "to": target_file})
                elif not node.level:
                    mods.append(node.module)
                for alias in node.names:
                    if alias.name != "*" and target_file:
                        imported_names[alias.asname or alias.name] = (alias.name, target_file)
            else:
                package_file = local_module_file(rel, None, node.level)
                for alias in node.names:
                    if alias.name == "*":
                        continue
                    target_file = local_module_file(rel, alias.name, node.level)
                    if target_file:
                        imported_modules[alias.asname or alias.name] = target_file
                        edges.append({"from": rel, "to": target_file})
                    elif package_file:
                        imported_names[alias.asname or alias.name] = (alias.name, package_file)
                        edges.append({"from": rel, "to": package_file})
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
            stack.append(node.name)
            seen = set()
            for arg in node.args.args + node.args.kwonlyargs:
                add_types(rel, node.name, node.lineno, arg.annotation, seen, imported_names, imported_modules)
            add_types(rel, node.name, node.lineno, node.returns, seen, imported_names, imported_modules)
            self.generic_visit(node)
            stack.pop()
        def visit_AsyncFunctionDef(self, node):
            self.visit_FunctionDef(node)
        def visit_ClassDef(self, node):
            seen = set()
            for base in node.bases:
                add_types(rel, node.name, node.lineno, base, seen, imported_names, imported_modules)
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
                # Calls are resolved by the graph's receiver-aware resolver.
                # A name-only match here is not native binding evidence.
            self.generic_visit(node)
    Visitor().visit(tree)
print(json.dumps({"edges": edges, "calls": calls, "types": types}))
`)
	cmd.Dir = req.Root
	// PYTHONSAFEPATH=1 disables Python's implicit prepend of the `-c` working
	// directory ('') to sys.path. Without it, cmd.Dir=req.Root means a repo
	// that ships its own ast.py / json.py at the root SHADOWS this script's
	// own stdlib imports (line "import ast, json, os, sys") and executes
	// attacker code at index time. The script's later explicit
	// sys.path.insert(root) for PathFinder resolution is unaffected — and
	// PathFinder only resolves paths, it never imports/executes repo modules.
	// appendEnv also scrubs grove's secrets from the subprocess environment.
	cmd.Env = appendEnv("GROVE_FILES="+string(filesJSON), "PYTHONSAFEPATH=1")
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
			FromLine int    `json:"fromLine"`
			To       string `json:"to"`
			ToName   string `json:"toName"`
		} `json:"calls"`
		Types []struct {
			From        string `json:"from"`
			FromName    string `json:"fromName"`
			FromLine    int    `json:"fromLine"`
			To          string `json:"to"`
			ToName      string `json:"toName"`
			LocalImport bool   `json:"localImport"`
		} `json:"types"`
	}
	if err := unmarshalJSON(out, &payload); err != nil {
		return Result{Diagnostics: []string{"python resolver JSON decode failed: " + err.Error()}}
	}
	fileScope := fileSet(req.Files)
	// Span-aware resolution: a (file, name) map collapses every same-named
	// method in a file to the earliest span, so werkzeug's six __init__ in
	// routing/rules.py all resolved to Subdomain.__init__ — RuleTemplate's
	// genuine "t.Iterable[Rule]" parameter was reported 82 lines away, on a
	// method that does not mention Rule. js_ts has used the locator since it
	// hit the same collision.
	symbols := newSymbolLocator(req.Symbols, map[string]bool{"python": true})
	// A local re-export can point first at a module that contains only an import
	// alias, not the original declaration (urllib3.connection.ProxyConfig is
	// one). If direct file resolution misses, follow only a proven-local import
	// whose original name has one unambiguous Python type declaration. External
	// imports never take this fallback, even if the repo happens to declare the
	// same name.
	importedTypes := make(map[string][]core.SymbolRecord)
	for _, symbol := range req.Symbols {
		if symbol.Language == "python" && typeKind(symbol.Kind) {
			importedTypes[symbol.Name] = append(importedTypes[symbol.Name], symbol)
		}
	}
	edges := make([]core.Edge, 0, len(payload.Edges)+len(payload.Calls)+len(payload.Types))
	for _, edge := range payload.Edges {
		from := strings.TrimSpace(edge.From)
		to := strings.TrimSpace(edge.To)
		if from == "" || to == "" || !fileScope[from] {
			continue
		}
		edges = append(edges, nativeImportEdge(from, to, 0.94))
	}
	for _, edge := range payload.Types {
		from, okFrom := symbols.at(edge.From, edge.FromName, edge.FromLine)
		to, okTo := symbols.at(edge.To, edge.ToName, 0)
		if !okTo && edge.LocalImport {
			if candidates := importedTypes[edge.ToName]; len(candidates) == 1 {
				to, okTo = candidates[0], true
			}
		}
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
