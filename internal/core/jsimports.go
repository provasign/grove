package core

import "strings"

const (
	jsImportAliasPrefix    = "@js-alias:"
	jsImportReExportPrefix = "@js-reexport:"
	jsImportPackagePrefix  = "@js-package:"
)

// JSImportPackage records a name a file imports from a package (a bare
// specifier: `import { Emitter } from "@socket.io/component-emitter"`).
func JSImportPackage(local, pkg string) string {
	if local == "" || pkg == "" {
		return ""
	}
	return jsImportPackagePrefix + local + "=" + pkg
}

// ParseJSImportPackage decodes a JSImportPackage value.
func ParseJSImportPackage(value string) (local, pkg string, ok bool) {
	rest, ok := strings.CutPrefix(value, jsImportPackagePrefix)
	if !ok {
		return "", "", false
	}
	local, pkg, ok = strings.Cut(rest, "=")
	return local, pkg, ok && local != "" && pkg != ""
}

func JSImportAlias(local, target string) string {
	if local == "" || target == "" {
		return ""
	}
	return jsImportAliasPrefix + local + "=" + target
}

func ParseJSImportAlias(value string) (local, target string, ok bool) {
	rest, ok := strings.CutPrefix(value, jsImportAliasPrefix)
	if !ok {
		return "", "", false
	}
	local, target, ok = strings.Cut(rest, "=")
	return local, target, ok && local != "" && target != ""
}

func JSImportReExport(path string) string {
	if path == "" {
		return ""
	}
	return jsImportReExportPrefix + path
}

func ParseJSImportReExport(value string) (path string, ok bool) {
	path, ok = strings.CutPrefix(value, jsImportReExportPrefix)
	return path, ok && path != ""
}
