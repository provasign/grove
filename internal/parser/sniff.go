package parser

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Content-based detection for mainframe artifacts (docs/mainframe-build-plan.md,
// phase 0.2). Estate exports are routinely extensionless — PDS members land as
// bare "CUSTUPD" files — so extension dispatch alone renders a whole corpus
// invisible. The rules here are deliberately narrow:
//
//   - A file with a KNOWN extension is never sniffed; DetectLanguage's answer
//     stands. The modern path is unchanged by construction.
//   - Only extensionless files (not name-matched ones like Makefile) are
//     sniffed, and only the first sniffHeadBytes of them.
//   - Signatures are strong: JCL statement shape, or explicit COBOL division
//     headers, or repeated fixed-format level-number lines WITH a PIC clause.
//     A README never matches these.

const sniffHeadBytes = 2048

var (
	sniffJCL = regexp.MustCompile(`(?m)^//[A-Z0-9$#@]{1,8} +(JOB|PROC|EXEC|DD)\b`)
	// Explicit COBOL markers, tolerant of a fixed-format sequence area.
	sniffCOBOLKeyword = regexp.MustCompile(`(?im)^.{0,7}\s*(IDENTIFICATION\s+DIVISION|PROGRAM-ID\s*\.)`)
	// Copybook shape: a fixed-format level-number line (data name in area B).
	sniffLevelLine = regexp.MustCompile(`(?m)^.{7}\s*\d{2}\s+[A-Z0-9][A-Z0-9-]*`)
	sniffPIC      = regexp.MustCompile(`(?i)\bPIC(?:TURE)?\s+`)
)

// sniffMainframe classifies head bytes as "cobol", "jcl", or "".
func sniffMainframe(head []byte) string {
	s := string(head)
	if sniffJCL.MatchString(s) {
		return "jcl"
	}
	if sniffCOBOLKeyword.MatchString(s) {
		return "cobol"
	}
	// Copybooks carry no divisions: require repeated level lines plus a PIC
	// clause so prose and data files never qualify.
	if len(sniffLevelLine.FindAllStringIndex(s, 3)) >= 3 && sniffPIC.MatchString(s) {
		return "cobol"
	}
	return ""
}

// DetectLanguageFile is DetectLanguage plus the extensionless-file sniff.
// It may read up to sniffHeadBytes from disk, only when the extension gives
// no answer and the file has no extension at all.
func DetectLanguageFile(path string) string {
	if lang := DetectLanguage(path); lang != "" {
		return lang
	}
	if filepath.Ext(path) != "" {
		return "" // unknown extension: respect it, do not sniff
	}
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return "" // dotfiles are config, not PDS members
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, sniffHeadBytes)
	n, _ := f.Read(head)
	return sniffMainframe(head[:n])
}

// DetectLanguageContent is DetectLanguage plus the same sniff applied to
// in-memory content — for callers that already hold the bytes.
func DetectLanguageContent(relPath string, content []byte) string {
	if lang := DetectLanguage(relPath); lang != "" {
		return lang
	}
	if filepath.Ext(relPath) != "" || strings.HasPrefix(filepath.Base(relPath), ".") {
		return ""
	}
	if len(content) > sniffHeadBytes {
		content = content[:sniffHeadBytes]
	}
	return sniffMainframe(content)
}

// SupportedFile reports whether Grove can index the file, including the
// extensionless mainframe sniff.
func SupportedFile(path string) bool {
	return DetectLanguageFile(path) != ""
}
