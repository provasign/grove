package core

import "strings"

const (
	cppUsingNamespacePrefix = "@cpp-using-namespace:"
	cppNamespaceAliasPrefix = "@cpp-namespace-alias:"
	cppUsingTypePrefix      = "@cpp-using-type:"
)

func CppUsingNamespace(namespace string) string {
	return cppUsingNamespacePrefix + namespace
}

func ParseCppUsingNamespace(value string) (string, bool) {
	namespace, ok := strings.CutPrefix(value, cppUsingNamespacePrefix)
	return namespace, ok && namespace != ""
}

func CppNamespaceAlias(local, target string) string {
	return cppNamespaceAliasPrefix + local + "=" + target
}

func ParseCppNamespaceAlias(value string) (local, target string, ok bool) {
	rest, ok := strings.CutPrefix(value, cppNamespaceAliasPrefix)
	if !ok {
		return "", "", false
	}
	local, target, ok = strings.Cut(rest, "=")
	return local, target, ok && local != "" && target != ""
}

func CppUsingType(local, target string) string {
	return cppUsingTypePrefix + local + "=" + target
}

func ParseCppUsingType(value string) (local, target string, ok bool) {
	rest, ok := strings.CutPrefix(value, cppUsingTypePrefix)
	if !ok {
		return "", "", false
	}
	local, target, ok = strings.Cut(rest, "=")
	return local, target, ok && local != "" && target != ""
}
