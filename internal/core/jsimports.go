package core

import "strings"

const (
	jsImportAliasPrefix    = "@js-alias:"
	jsImportReExportPrefix = "@js-reexport:"
)

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
