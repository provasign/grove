package core

import "strings"

const goImportAliasPrefix = "@go-alias:"

func GoImportAlias(local, target string) string {
	if local == "" || target == "" {
		return ""
	}
	return goImportAliasPrefix + local + "=" + target
}

func ParseGoImportAlias(value string) (local, target string, ok bool) {
	rest, ok := strings.CutPrefix(value, goImportAliasPrefix)
	if !ok {
		return "", "", false
	}
	local, target, ok = strings.Cut(rest, "=")
	return local, target, ok && local != "" && target != ""
}
