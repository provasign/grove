package cert

import (
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
)

func TestCertifyDiffAllowWithMappedSymbolAndTest(t *testing.T) {
	cg := graph.New()
	cg.Replace([]core.SymbolRecord{
		{
			ID:            "auth.go::Login@sha",
			FilePath:      "auth.go",
			BlobSHA:       "sha",
			Language:      "go",
			Kind:          core.KindFunction,
			Name:          "Login",
			QualifiedName: "Login",
			Span:          core.LineRange{Start: 3, End: 6},
		},
		{
			ID:            "auth_test.go::TestLogin@sha",
			FilePath:      "auth_test.go",
			BlobSHA:       "sha",
			Language:      "go",
			Kind:          core.KindFunction,
			Name:          "TestLogin",
			QualifiedName: "TestLogin",
			Span:          core.LineRange{Start: 3, End: 5},
		},
	}, 2)

	report := CertifyDiff(cg, core.DiffInput{UnifiedDiff: `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -3,4 +3,4 @@
 func Login(username, password string) bool {
-	return false
+	return true
 }
`})

	if report.Verdict != core.VerdictAllow {
		t.Fatalf("verdict = %q, want allow; report=%+v", report.Verdict, report)
	}
	if len(report.ChangedSymbols) != 1 || report.ChangedSymbols[0].Name != "Login" {
		t.Fatalf("changed symbols = %+v", report.ChangedSymbols)
	}
	if len(report.Tests) != 1 || report.Tests[0].Name != "TestLogin" {
		t.Fatalf("tests = %+v", report.Tests)
	}
}

func TestCertifyDiffManualReviewForUnmappedHunk(t *testing.T) {
	cg := graph.New()
	cg.Replace([]core.SymbolRecord{
		{
			ID:            "auth.go::Login@sha",
			FilePath:      "auth.go",
			Language:      "go",
			Kind:          core.KindFunction,
			Name:          "Login",
			QualifiedName: "Login",
			Span:          core.LineRange{Start: 10, End: 20},
		},
	}, 1)

	report := CertifyDiff(cg, core.DiffInput{UnifiedDiff: `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -1,2 +1,2 @@
-package auth
+package login
`})

	if report.Verdict != core.VerdictManualReview {
		t.Fatalf("verdict = %q, want manual_review", report.Verdict)
	}
	if len(report.Unknowns) != 1 || report.Unknowns[0].Code != "hunk_unmapped" {
		t.Fatalf("unknowns = %+v", report.Unknowns)
	}
}

func TestCertifyDiffManualReviewForUnsupportedOrIgnoredFile(t *testing.T) {
	report := CertifyDiff(graph.New(), core.DiffInput{UnifiedDiff: `diff --git a/.env b/.env
--- a/.env
+++ b/.env
@@ -1,1 +1,1 @@
-TOKEN=old
+TOKEN=new
`})

	if report.Verdict != core.VerdictManualReview {
		t.Fatalf("verdict = %q, want manual_review", report.Verdict)
	}
	if len(report.Unknowns) != 1 || report.Unknowns[0].Code != "file_not_indexed" {
		t.Fatalf("unknowns = %+v", report.Unknowns)
	}
}

func TestCertifyDiffBlockForMalformedDiff(t *testing.T) {
	report := CertifyDiff(graph.New(), core.DiffInput{UnifiedDiff: "not a diff"})

	if report.Verdict != core.VerdictBlock {
		t.Fatalf("verdict = %q, want block", report.Verdict)
	}
	if len(report.Findings) != 1 || report.Findings[0].Code != "diff_malformed" {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

func TestCertifyDiffManualReviewForDeletedFile(t *testing.T) {
	report := CertifyDiff(graph.New(), core.DiffInput{UnifiedDiff: `diff --git a/auth.go b/auth.go
deleted file mode 100644
--- a/auth.go
+++ /dev/null
@@ -1,1 +0,0 @@
-package auth
`})

	if report.Verdict != core.VerdictManualReview {
		t.Fatalf("verdict = %q, want manual_review", report.Verdict)
	}
	if len(report.Unknowns) != 1 || report.Unknowns[0].Code != "deleted_file" {
		t.Fatalf("unknowns = %+v", report.Unknowns)
	}
}

func TestCertifyDiffManualReviewForBinaryFile(t *testing.T) {
	report := CertifyDiff(graph.New(), core.DiffInput{UnifiedDiff: `diff --git a/logo.png b/logo.png
Binary files a/logo.png and b/logo.png differ
`})

	if report.Verdict != core.VerdictManualReview {
		t.Fatalf("verdict = %q, want manual_review", report.Verdict)
	}
	if len(report.Unknowns) != 1 || report.Unknowns[0].Code != "binary_change" {
		t.Fatalf("unknowns = %+v", report.Unknowns)
	}
}

func TestCertifyDiffManualReviewForMissingTestEvidence(t *testing.T) {
	cg := graph.New()
	cg.Replace([]core.SymbolRecord{
		{
			ID:            "auth.go::Login@sha",
			FilePath:      "auth.go",
			BlobSHA:       "sha",
			Language:      "go",
			Kind:          core.KindFunction,
			Name:          "Login",
			QualifiedName: "Login",
			Span:          core.LineRange{Start: 3, End: 6},
		},
	}, 1)

	report := CertifyDiff(cg, core.DiffInput{UnifiedDiff: `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -3,4 +3,4 @@
 func Login() bool {
-	return false
+	return true
 }
`})

	if report.Verdict != core.VerdictManualReview {
		t.Fatalf("verdict = %q, want manual_review", report.Verdict)
	}
	if len(report.Unknowns) != 1 || report.Unknowns[0].Code != "tests_unknown" {
		t.Fatalf("unknowns = %+v", report.Unknowns)
	}
}

func TestParseUnifiedDiffEmpty(t *testing.T) {
	files, err := ParseUnifiedDiff("\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %+v, want none", files)
	}
}

func TestParseUnifiedDiffRejectsInvalidRange(t *testing.T) {
	_, err := ParseUnifiedDiff(`diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -1,1 +0,2 @@
-package auth
+package login
`)
	if err == nil {
		t.Fatal("expected invalid range error")
	}
}
