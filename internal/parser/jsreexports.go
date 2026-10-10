package parser

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/provasign/astkit/textmask"
	"github.com/provasign/grove/internal/core"
)

// jsExportClauseRE matches an export clause in masked source (comments and
// string literals blanked); jsExportFromRE matches the `from` that may
// follow it, whose module string is read from the original source.
var (
	jsExportClauseRE = regexp.MustCompile(`(?s)\bexport\s+(?:type\s+)?\{([^}]*)\}`)
	jsExportFromRE   = regexp.MustCompile(`^\s*from\b`)
)

// jsMaskLanguage is the textmask language key for a TS/JS path.
func jsMaskLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsx":
		return "tsx"
	case ".ts", ".mts", ".cts":
		return "typescript"
	case ".jsx":
		return "jsx"
	}
	return "javascript"
}

func jsSourceExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		return true
	}
	return false
}

// JSExportSpecifiers lists every `export { name [as alias] } [from "m"]`
// specifier whose local name is name, in the TS/JS files under root. It is
// a lexical scan (export clauses have no nesting that matters), prefiltered
// by the name's bytes so a change-impact query touches only candidate files.
func JSExportSpecifiers(root, name string) ([]core.JSExportSpecifier, int) {
	if name == "" {
		return nil, 0
	}
	byName, skipped := scanJSExports(root, []byte(name), name)
	return byName[name], skipped
}

// JSExportIndex scans every TS/JS file under root once and returns all
// export specifiers keyed by local name — the engine builds it lazily per
// installed graph so repeated change-impact queries do not re-walk the tree.
func JSExportIndex(root string) (map[string][]core.JSExportSpecifier, int) {
	return scanJSExports(root, nil, "")
}

func scanJSExports(root string, prefilter []byte, name string) (map[string][]core.JSExportSpecifier, int) {
	out := map[string][]core.JSExportSpecifier{}
	skipped := 0
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			skipped++
			return nil
		}
		if info.IsDir() {
			if p != root && refSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !jsSourceExt(p) {
			return nil
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			skipped++
			return nil
		}
		if prefilter != nil && !bytes.Contains(src, prefilter) || !bytes.Contains(src, []byte("export")) {
			return nil
		}
		rel := filepath.ToSlash(relPath(root, p))
		for _, spec := range jsExportSpecifiersIn(rel, src, name) {
			out[spec.Local] = append(out[spec.Local], spec)
		}
		return nil
	})
	for k := range out {
		specs := out[k]
		sort.SliceStable(specs, func(i, j int) bool {
			if specs[i].File != specs[j].File {
				return specs[i].File < specs[j].File
			}
			return specs[i].Line < specs[j].Line
		})
	}
	return out, skipped
}

// jsExportSpecifiersIn returns the specifiers of one file; name == "" means all.
func jsExportSpecifiersIn(rel string, orig []byte, name string) []core.JSExportSpecifier {
	var out []core.JSExportSpecifier
	// Comments and literals are blanked (offsets kept) so lines stay valid
	// and an export clause inside a string, template literal or comment
	// never matches: zod's `/** @deprecated Use z.gte() */ _gte as _min`
	// is a specifier.
	src := []byte(textmask.Mask(jsMaskLanguage(rel), string(orig)))
	for _, m := range jsExportClauseRE.FindAllSubmatchIndex(src, -1) {
		bodyStart, bodyEnd := m[2], m[3]
		source := ""
		if f := jsExportFromRE.FindIndex(src[m[1]:]); f != nil {
			// The module string is masked in src; read it from orig.
			q := m[1] + f[1]
			for q < len(orig) && (orig[q] == ' ' || orig[q] == '\t' || orig[q] == '\n' || orig[q] == '\r') {
				q++
			}
			if q < len(orig) && (orig[q] == '"' || orig[q] == '\'') {
				if end := bytes.IndexByte(orig[q+1:], orig[q]); end > 0 {
					source = string(orig[q+1 : q+1+end])
				}
			}
		}
		offset := bodyStart
		for _, item := range bytes.Split(src[bodyStart:bodyEnd], []byte(",")) {
			itemStart := offset
			offset += len(item) + 1
			text := strings.TrimSpace(string(item))
			text = strings.TrimSpace(strings.TrimPrefix(text, "type "))
			fields := strings.Fields(text)
			local, exported := "", ""
			switch {
			case len(fields) == 1:
				local, exported = fields[0], fields[0]
			case len(fields) == 3 && fields[1] == "as":
				local, exported = fields[0], fields[2]
			default:
				continue
			}
			if name != "" && local != name {
				continue
			}
			// Line of the local name within the specifier.
			at := itemStart + bytes.Index(item, []byte(local))
			line := 1 + bytes.Count(src[:at], []byte("\n"))
			out = append(out, core.JSExportSpecifier{
				File: rel, Line: line, Local: local, Exported: exported, Source: source,
				Text: strings.TrimSpace(lineAt(orig, at)),
			})
		}
	}
	return out
}

func lineAt(src []byte, at int) string {
	start := bytes.LastIndexByte(src[:at], '\n') + 1
	end := bytes.IndexByte(src[at:], '\n')
	if end < 0 {
		return string(src[start:])
	}
	return string(src[start : at+end])
}
