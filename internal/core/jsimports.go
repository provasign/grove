package core

import "strings"

const jsImportAliasPrefix = "@js-alias:"

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
