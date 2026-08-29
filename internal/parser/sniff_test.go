package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixedLine(code string) string {
	return "000100 " + code + strings.Repeat(" ", 65-len(code)) + "SEQ73-80"
}

func TestSniffMainframe(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"jcl job", "//NIGHTLY  JOB (ACCT),'X',CLASS=A\n//S1 EXEC PGM=FOO\n", "jcl"},
		{"cobol program", fixedLine(" IDENTIFICATION DIVISION.") + "\n" + fixedLine(" PROGRAM-ID. X."), "cobol"},
		{"copybook levels+pic", strings.Join([]string{
			fixedLine(" 01  REC."),
			fixedLine("     05  F1  PIC 9(8)."),
			fixedLine("     05  F2  PIC X(30)."),
		}, "\n"), "cobol"},
		{"readme prose", "# Title\n\nThis project does things.\nInstall with make.\n", ""},
		{"license", "Apache License\nVersion 2.0, January 2004\n", ""},
		{"levels without pic", fixedLine(" 01  A.") + "\n" + fixedLine(" 01  B.") + "\n" + fixedLine(" 01  C."), ""},
		{"path comment slashes", "// this is a Go comment file?\n// more comments\n", ""},
	}
	for _, c := range cases {
		if got := sniffMainframe([]byte(c.body)); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestDetectLanguageFile_ExtensionlessMember(t *testing.T) {
	dir := t.TempDir()
	member := filepath.Join(dir, "CUSTUPD")
	body := fixedLine(" IDENTIFICATION DIVISION.") + "\n" + fixedLine(" PROGRAM-ID. CUSTUPD.")
	if err := os.WriteFile(member, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DetectLanguageFile(member); got != "cobol" {
		t.Errorf("extensionless COBOL member: got %q", got)
	}

	// Unknown extensions are respected, never sniffed.
	odd := filepath.Join(dir, "CUSTUPD.dat")
	if err := os.WriteFile(odd, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DetectLanguageFile(odd); got != "" {
		t.Errorf(".dat file sniffed to %q, want skipped", got)
	}

	// Known extensions keep their meaning regardless of content.
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DetectLanguageFile(goFile); got != "go" {
		t.Errorf("main.go: got %q", got)
	}
}

// Field-reported: mainframe exports carry uppercase extensions; the language
// switch is case-sensitive by design for modern code (.C means C++), so the
// mainframe set gets a lowercase fallback. 32% -> 99% include resolution.
func TestDetectLanguage_UppercaseMainframeExtensions(t *testing.T) {
	for path, want := range map[string]string{
		"cobol/copybook/AUCCS020.CPY": "cobol",
		"cobol/TESTPGM.CBL":           "cobol",
		"jcl/NIGHTLY.JCL":             "jcl",
		"legacy/Batch.Cbl":            "cobol",
		"src/foo.C":                   "", // modern semantics preserved
		"src/FOO.GO":                  "",
	} {
		if got := DetectLanguage(path); got != want {
			t.Errorf("%s: got %q want %q", path, got, want)
		}
	}
}
