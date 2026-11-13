package graph

import (
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// pyExternalRefMarker records, on a Python symbol, its calls through names
// it imported from another distribution: "py:<module>#<name>@line:col".
// A second repository that declares the module can then find its callers
// here (core.EdgeExternalRef).
//
// Python has no compiler pass, so this is import-scoped, not resolved: the
// call names something the file imported, and what that name is at run time
// is not checked. A method called on an instance (m.save()) names no import
// and is not recorded.
//
// The record depends on the symbol alone, never on which other files the
// repository holds, so an incremental run that leaves the symbol untouched
// carries the same record a full run would write. That is why "is this
// module the project's own?" is answered from the file's own path only:
// relative imports, and absolute imports under the file's own top-level
// package, are the project's. An absolute import of the project's package
// from a file outside it (tests/) is recorded too; nothing queries it.
func pyExternalRefMarker(symbol *core.SymbolRecord) []core.Edge {
	if len(symbol.CallSites) == 0 {
		return nil
	}
	bindings := map[string]string{} // local name -> "module" or "module#name"
	for _, imp := range symbol.Imports {
		if _, local, target, ok := core.ParsePythonImportBinding(imp); ok {
			bindings[local] = target
		}
	}
	if len(bindings) == 0 {
		return nil
	}
	own := pyOwnTopLevels(symbol.FilePath)
	seen := map[string]bool{}
	var marks []string
	for _, cs := range symbol.CallSites {
		if cs.Line <= 0 {
			continue
		}
		head, rest, _ := strings.Cut(cs.Callee, ".")
		target, ok := bindings[head]
		if !ok {
			continue
		}
		module, name, named := strings.Cut(target, "#")
		switch {
		case named && rest != "":
			name += "." + rest // Model.create through `from m import Model`
		case named:
		case strings.HasPrefix(cs.Callee, module+"."):
			name = strings.TrimPrefix(cs.Callee, module+".") // `import a.b`; a.b.go()
		case rest != "":
			name = rest // `import a.b as ab`; ab.go()
		default:
			continue // the module itself called
		}
		top, _, _ := strings.Cut(module, ".")
		if name == "" || strings.HasPrefix(module, ".") || own[top] || pyStdlib[top] {
			continue
		}
		mark := "py:" + module + "#" + name + "@" + strconv.Itoa(cs.Line)
		if cs.Col > 0 {
			mark += ":" + strconv.Itoa(cs.Col)
		}
		if !seen[mark] {
			seen[mark] = true
			marks = append(marks, mark)
		}
	}
	if len(marks) == 0 {
		return nil
	}
	sort.Strings(marks)
	return []core.Edge{{From: symbol.ID, To: symbol.ID, Type: core.EdgeExternalRef, Confidence: 0.6,
		Source: core.EvidenceSourceHeuristic, Lines: strings.Join(marks, ",")}}
}

// pyOwnTopLevels returns the top-level package names a file's own path
// places it under: its first directory, and the second when the first is a
// conventional source root.
func pyOwnTopLevels(filePath string) map[string]bool {
	parts := strings.Split(filePath, "/")
	own := map[string]bool{}
	if len(parts) > 1 {
		own[parts[0]] = true
		if (parts[0] == "src" || parts[0] == "lib" || parts[0] == "python") && len(parts) > 2 {
			own[parts[1]] = true
		}
	}
	return own
}

// pyStdlib: top-level standard library modules. Calls into them are the
// platform, not a dependency another repository declares. An omission only
// adds a record nobody queries.
var pyStdlib = func() map[string]bool {
	m := map[string]bool{}
	for _, name := range strings.Fields(`__future__ abc argparse array ast asyncio atexit base64 bdb binascii bisect builtins bz2
calendar cmath cmd code codecs codeop collections colorsys compileall concurrent configparser contextlib contextvars copy
copyreg cProfile csv ctypes curses dataclasses datetime dbm decimal difflib dis doctest email encodings ensurepip enum errno
faulthandler fcntl filecmp fileinput fnmatch fractions ftplib functools gc getopt getpass gettext glob graphlib grp gzip hashlib
heapq hmac html http idlelib imaplib importlib inspect io ipaddress itertools json keyword linecache locale logging lzma mailbox
marshal math mimetypes mmap modulefinder multiprocessing netrc numbers operator optparse os pathlib pdb pickle pickletools pkgutil
platform plistlib poplib posix posixpath pprint profile pstats pty pwd py_compile pyclbr pydoc queue quopri random re readline
reprlib resource rlcompleter runpy sched secrets select selectors shelve shlex shutil signal site smtplib socket socketserver
sqlite3 ssl stat statistics string stringprep struct subprocess symtable sys sysconfig syslog tabnanny tarfile tempfile termios
textwrap threading time timeit tkinter token tokenize tomllib trace traceback tracemalloc tty turtle types typing unicodedata
unittest urllib uuid venv warnings wave weakref webbrowser winreg winsound wsgiref xml xmlrpc zipapp zipfile zipimport zlib
zoneinfo`) {
		m[name] = true
	}
	return m
}()
