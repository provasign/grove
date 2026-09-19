package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Objective-C call-edge ground truth from clang's own semantic analysis:
// each .m file is parsed with `clang -fsyntax-only -Xclang -ast-dump=json`
// and the typed AST read back. clang resolves every message send's
// receiver type (`[self foo]`, `[SBJson5Writer alloc]`, `[super init]`)
// and every C call's function, so the oracle records the same "declared
// receiver type" altitude as the Java/Kotlin bytecode oracles: an
// instance message binds the selector's implementation on the receiver's
// static class or the nearest superclass implementing it; `id`- and
// protocol-typed receivers, whose target only exists at run time, record
// nothing. Only methods and functions implemented in the repo take part.
//
// scip-clang (the C/C++ oracle) refuses .m inputs and sourcekitten only
// indexes Swift, which is why this reads clang's AST directly. The JSON
// dump is location-sparse — a node's file and line are printed only when
// they differ from the previous node's — so the walk carries them along
// in document order.

// ObjCCallTruth parses every non-test .m file and derives caller→callee
// edges between in-repo implementations.
func ObjCCallTruth(repoRoot string) (TruthFile, []TruthEdge, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return TruthFile{}, nil, err
	}
	var sources []string
	includeDirs := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || objcSkipDir(d.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".m"):
			if filepath.Base(path) != "main.m" {
				sources = append(sources, filepath.ToSlash(rel))
			}
		case strings.HasSuffix(path, ".h"):
			includeDirs[filepath.Dir(rel)] = true
		}
		return nil
	})
	if len(sources) == 0 {
		return TruthFile{}, nil, fmt.Errorf("no .m sources under %s", root)
	}
	sort.Strings(sources)
	var incArgs []string
	for dir := range includeDirs {
		incArgs = append(incArgs, "-I"+dir)
	}
	sort.Strings(incArgs)

	sdk := ""
	if out, err := exec.Command("xcrun", "--show-sdk-path").Output(); err == nil {
		sdk = strings.TrimSpace(string(out))
	}

	tus := make([]*objcTU, 0, len(sources))
	for _, src := range sources {
		args := []string{"-fsyntax-only", "-fmodules", "-fobjc-arc", "-Wno-everything", "-Xclang", "-ast-dump=json"}
		if sdk != "" {
			args = append(args, "-isysroot", sdk)
		}
		args = append(args, incArgs...)
		args = append(args, src)
		cmd := exec.Command("clang", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			continue // a file that does not compile in isolation has no truth
		}
		var rootNode objcNode
		if err := json.Unmarshal(out, &rootNode); err != nil {
			continue
		}
		tu := &objcTU{file: src, supers: map[string]string{}}
		tu.walk(&rootNode, &objcLoc{}, "", "", false)
		tus = append(tus, tu)
	}
	if len(tus) == 0 {
		return TruthFile{}, nil, fmt.Errorf("clang could not parse any of %d .m files (is Xcode or the Command Line Tools installed?)", len(sources))
	}

	// Implementations across all translation units, keyed as clang names
	// them: "-[Class sel]" / "+[Class sel]" for methods, the bare name for
	// C functions. The class hierarchy is the union of every TU's view.
	impls := map[string]FuncRef{}
	supers := map[string]string{}
	for _, tu := range tus {
		for key, ref := range tu.impls {
			impls[key] = ref
		}
		for cls, super := range tu.supers {
			if super != "" {
				supers[cls] = super
			}
		}
	}
	resolveMethod := func(cls, sel string, instance bool) (FuncRef, bool) {
		prefix := "-"
		if !instance {
			prefix = "+"
		}
		for depth := 0; cls != "" && depth < 16; depth++ {
			if ref, ok := impls[prefix+"["+cls+" "+sel+"]"]; ok {
				return ref, true
			}
			cls = supers[cls]
		}
		return FuncRef{}, false
	}

	seen := map[string]bool{}
	funcs := map[string]bool{}
	var edges []TruthEdge
	for _, tu := range tus {
		for _, call := range tu.calls {
			var callee FuncRef
			var ok bool
			if call.function != "" {
				callee, ok = impls[call.function]
			} else {
				callee, ok = resolveMethod(call.class, call.selector, call.instance)
			}
			if !ok {
				continue
			}
			funcs[call.caller.funcKey()] = true
			funcs[callee.funcKey()] = true
			key := call.caller.funcKey() + "→" + callee.funcKey()
			if seen[key] || call.caller.funcKey() == callee.funcKey() {
				continue
			}
			seen[key] = true
			edges = append(edges, TruthEdge{Caller: call.caller, Callee: callee})
		}
		for _, ref := range tu.impls {
			funcs[ref.funcKey()] = true
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Caller.File != b.Caller.File {
			return a.Caller.File < b.Caller.File
		}
		if a.Caller.Line != b.Caller.Line {
			return a.Caller.Line < b.Caller.Line
		}
		return a.Callee.funcKey() < b.Callee.funcKey()
	})
	header := TruthFile{
		Schema:    "grove-eval/calls/v1",
		Repo:      filepath.Base(root),
		Generator: "clang-ast",
		Functions: len(funcs),
		Edges:     len(edges),
	}
	return header, edges, nil
}

func objcSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case "test", "tests", "example", "examples", "vendor", "pods", "carthage", "build", "node_modules":
		return true
	}
	return false
}

// objcNode is the subset of clang's JSON AST the oracle reads.
type objcNode struct {
	Kind         string     `json:"kind"`
	Name         string     `json:"name"`
	MangledName  string     `json:"mangledName"`
	Loc          objcRawLoc `json:"loc"`
	Range        objcRange  `json:"range"`
	Instance     *bool      `json:"instance"`
	Selector     string     `json:"selector"`
	ReceiverKind string     `json:"receiverKind"`
	ClassType    *objcType  `json:"classType"`
	SuperType    *objcType  `json:"superType"`
	Type         *objcType  `json:"type"`
	Super        *objcRef   `json:"super"`
	Interface    *objcRef   `json:"interface"`
	Referenced   *objcRef   `json:"referencedDecl"`
	Inner        []objcNode `json:"inner"`
}

type objcType struct {
	QualType string `json:"qualType"`
}

type objcRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type objcRange struct {
	Begin objcRawLoc `json:"begin"`
	End   objcRawLoc `json:"end"`
}

// objcRawLoc is a location as printed: sparse (file/line only when they
// change), or a macro pair of spelling and expansion locations.
type objcRawLoc struct {
	File         string      `json:"file"`
	Line         int         `json:"line"`
	SpellingLoc  *objcRawLoc `json:"spellingLoc"`
	ExpansionLoc *objcRawLoc `json:"expansionLoc"`
}

// objcLoc is the walk's running location state.
type objcLoc struct {
	file string
	line int
}

// apply folds one printed location into the running state, in the order
// clang printed its parts, and returns the effective (expansion) location.
func (l *objcLoc) apply(raw *objcRawLoc) objcLoc {
	if raw == nil {
		return *l
	}
	if raw.SpellingLoc != nil || raw.ExpansionLoc != nil {
		l.apply(raw.SpellingLoc)
		return l.apply(raw.ExpansionLoc)
	}
	if raw.File != "" {
		l.file = raw.File
	}
	if raw.Line != 0 {
		l.line = raw.Line
	}
	return *l
}

type objcCall struct {
	caller   FuncRef
	class    string // message receiver's static class ("" for a C call)
	selector string
	instance bool
	function string // C callee name (CallExpr)
}

type objcTU struct {
	file   string
	impls  map[string]FuncRef // "-[Class sel]" / "+[Class sel]" / "func"
	supers map[string]string  // class → superclass
	calls  []objcCall
}

// walk visits nodes in document order, tracking the sparse location, the
// enclosing implementation class, and the enclosing implemented method or
// function (the caller for message sends and calls beneath it).
func (tu *objcTU) walk(n *objcNode, loc *objcLoc, implClass string, caller string, hasCaller bool) {
	at := loc.apply(&n.Loc)
	loc.apply(&n.Range.Begin)
	end := loc.apply(&n.Range.End)
	_ = end
	callerRef, callerOK := FuncRef{}, false
	if hasCaller {
		callerRef, callerOK = tu.impls[caller], true
	}

	switch n.Kind {
	case "ObjCInterfaceDecl":
		if n.Name != "" && n.Super != nil && n.Super.Name != "" {
			tu.supers[n.Name] = n.Super.Name
		}
	case "ObjCImplementationDecl":
		implClass = n.Name
		if n.Super != nil && n.Super.Name != "" {
			tu.supers[n.Name] = n.Super.Name
		}
	case "ObjCCategoryImplDecl":
		if n.Interface != nil {
			implClass = n.Interface.Name
		}
	case "ObjCMethodDecl":
		if implClass != "" && objcHasBody(n) && tu.inFile(at.file) {
			key := n.MangledName
			if key == "" {
				prefix := "-"
				if n.Instance != nil && !*n.Instance {
					prefix = "+"
				}
				key = prefix + "[" + implClass + " " + n.Name + "]"
			}
			if tu.impls == nil {
				tu.impls = map[string]FuncRef{}
			}
			tu.impls[key] = FuncRef{File: tu.file, Line: at.line, Name: implClass + "." + n.Name}
			caller, hasCaller = key, true
			callerRef, callerOK = tu.impls[key], true
		}
	case "FunctionDecl":
		if objcHasBody(n) && tu.inFile(at.file) && n.Name != "" {
			if tu.impls == nil {
				tu.impls = map[string]FuncRef{}
			}
			tu.impls[n.Name] = FuncRef{File: tu.file, Line: at.line, Name: n.Name}
			caller, hasCaller = n.Name, true
			callerRef, callerOK = tu.impls[n.Name], true
		}
	case "ObjCMessageExpr":
		if callerOK {
			if call, ok := objcMessageTarget(n); ok {
				call.caller = callerRef
				tu.calls = append(tu.calls, call)
			}
		}
	case "CallExpr":
		if callerOK {
			if fn := objcCalledFunction(n); fn != "" {
				tu.calls = append(tu.calls, objcCall{caller: callerRef, function: fn})
			}
		}
	}
	for i := range n.Inner {
		tu.walk(&n.Inner[i], loc, implClass, caller, hasCaller)
	}
}

// inFile reports whether the running location file is this TU's own
// source (clang prints paths as given on its command line; the walk runs
// from the repo root with a repo-relative path).
func (tu *objcTU) inFile(file string) bool {
	return file != "" && filepath.ToSlash(filepath.Clean(file)) == tu.file
}

func objcHasBody(n *objcNode) bool {
	for i := range n.Inner {
		if n.Inner[i].Kind == "CompoundStmt" {
			return true
		}
	}
	return false
}

// objcMessageTarget reads a message send's static receiver class and
// selector. `id`/protocol receivers have no static class and yield false.
func objcMessageTarget(n *objcNode) (objcCall, bool) {
	call := objcCall{selector: n.Selector}
	switch {
	case strings.HasPrefix(n.ReceiverKind, "super"):
		if n.SuperType == nil {
			return call, false
		}
		call.class = objcClassOfType(n.SuperType.QualType)
		call.instance = !strings.Contains(n.ReceiverKind, "class")
	case n.ReceiverKind == "class":
		if n.ClassType == nil {
			return call, false
		}
		call.class = objcClassOfType(n.ClassType.QualType)
		call.instance = false
	case n.ReceiverKind == "instance":
		if len(n.Inner) == 0 || n.Inner[0].Type == nil {
			return call, false
		}
		call.class = objcClassOfType(n.Inner[0].Type.QualType)
		call.instance = true
	default:
		return call, false
	}
	if call.class == "" || call.selector == "" {
		return call, false
	}
	return call, true
}

// objcClassOfType reduces a clang qualType ("SBJson5Writer *", "NSObject
// *__strong", "Class", "id<SBJson5StreamParserDelegate>") to a class name,
// or "" when the static type names no class.
func objcClassOfType(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexAny(t, "*<"); i >= 0 {
		t = t[:i]
	}
	fields := strings.Fields(t)
	name := ""
	for _, f := range fields {
		switch f {
		case "const", "volatile", "__strong", "__weak", "__unsafe_unretained", "__autoreleasing", "__kindof", "struct":
			continue
		}
		name = f
	}
	switch name {
	case "", "id", "Class", "void", "instancetype", "SEL":
		return ""
	}
	if name[0] == '_' || (name[0] >= '0' && name[0] <= '9') {
		return ""
	}
	return name
}

// objcCalledFunction returns the FunctionDecl a CallExpr's callee names
// (through implicit casts), or "" for an indirect call.
func objcCalledFunction(n *objcNode) string {
	if len(n.Inner) == 0 {
		return ""
	}
	callee := &n.Inner[0]
	for depth := 0; depth < 4; depth++ {
		if callee.Kind == "DeclRefExpr" {
			if callee.Referenced != nil && callee.Referenced.Kind == "FunctionDecl" {
				return callee.Referenced.Name
			}
			return ""
		}
		if len(callee.Inner) == 0 {
			return ""
		}
		callee = &callee.Inner[0]
	}
	return ""
}
