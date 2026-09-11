package core

import (
	"strconv"
	"strings"
)

const pythonImportBindingPrefix = "@python-binding:"

// PythonImportBinding preserves the lexical line and local name that the
// generic Imports []string representation otherwise loses.
func PythonImportBinding(line int, local, target string) string {
	if line < 1 || local == "" || target == "" {
		return ""
	}
	return pythonImportBindingPrefix + strconv.Itoa(line) + ":" + local + "=" + target
}

func ParsePythonImportBinding(value string) (line int, local, target string, ok bool) {
	rest, ok := strings.CutPrefix(value, pythonImportBindingPrefix)
	if !ok {
		return 0, "", "", false
	}
	lineText, rest, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, "", "", false
	}
	line, err := strconv.Atoi(lineText)
	if err != nil || line < 1 {
		return 0, "", "", false
	}
	local, target, ok = strings.Cut(rest, "=")
	return line, local, target, ok && local != "" && target != ""
}
