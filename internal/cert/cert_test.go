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

	report := CertifyDiff(cg, core.DiffInput{
		Policy: core.CertificationPolicy{RequireTestsForCode: true},
		UnifiedDiff: `diff --git a/auth.go b/auth.go
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

func TestCertifyDiffAllowWhenRequireTestsForCodeFalse(t *testing.T) {
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

	report := CertifyDiff(cg, core.DiffInput{
		Policy: core.CertificationPolicy{RequireTestsForCode: false},
		UnifiedDiff: `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -3,4 +3,4 @@
 func Login() bool {
-	return false
+	return true
 }
`})

	if report.Verdict != core.VerdictAllow {
		t.Fatalf("verdict = %q, want allow when RequireTestsForCode=false; unknowns=%+v", report.Verdict, report.Unknowns)
	}
}

func TestCertifyDiffManualReviewPartialTestCoverage(t *testing.T) {
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
			ID:            "auth.go::Logout@sha",
			FilePath:      "auth.go",
			BlobSHA:       "sha",
			Language:      "go",
			Kind:          core.KindFunction,
			Name:          "Logout",
			QualifiedName: "Logout",
			Span:          core.LineRange{Start: 8, End: 11},
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
	}, 3)

	report := CertifyDiff(cg, core.DiffInput{
		Policy: core.CertificationPolicy{RequireTestsForCode: true},
		UnifiedDiff: `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -3,4 +3,4 @@
 func Login() bool {
-	return false
+	return true
 }
@@ -8,4 +8,4 @@
 func Logout() {
-	sessions.Clear()
+	sessions.ClearAll()
 }
`})

	if report.Verdict != core.VerdictManualReview {
		t.Fatalf("verdict = %q, want manual_review", report.Verdict)
	}
	// Logout has no covering test; Login is covered by TestLogin via graph edge.
	if len(report.Unknowns) != 1 || report.Unknowns[0].Code != "tests_unknown" {
		t.Fatalf("unknowns = %+v, want exactly one tests_unknown for uncovered symbol", report.Unknowns)
	}
}

// TestCertifyDiffContextLinesDoNotMarkNeighboursChanged guards against the
// bug where hunk ranges were seeded from the @@ header (context lines
// included), reporting untouched adjacent symbols as changed.
func TestCertifyDiffContextLinesDoNotMarkNeighboursChanged(t *testing.T) {
	cg := graph.New()
	cg.Replace([]core.SymbolRecord{
		{
			ID:            "auth.go::Header@sha",
			FilePath:      "auth.go",
			BlobSHA:       "sha",
			Language:      "go",
			Kind:          core.KindFunction,
			Name:          "Header",
			QualifiedName: "Header",
			Span:          core.LineRange{Start: 1, End: 3},
		},
		{
			ID:            "auth.go::Login@sha",
			FilePath:      "auth.go",
			BlobSHA:       "sha",
			Language:      "go",
			Kind:          core.KindFunction,
			Name:          "Login",
			QualifiedName: "Login",
			Span:          core.LineRange{Start: 4, End: 8},
		},
	}, 1)

	// Only line 6 changes; lines 1-3 (the whole Header symbol) are context.
	report := CertifyDiff(cg, core.DiffInput{UnifiedDiff: `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -1,8 +1,8 @@
 func Header() string {
 	return "h"
 }
 func Login() bool {
 	x := 1
-	return false
+	return true
 	_ = x
 }
`})

	if len(report.ChangedSymbols) != 1 || report.ChangedSymbols[0].Name != "Login" {
		t.Fatalf("changed symbols = %+v, want only Login", report.ChangedSymbols)
	}
}

// TestCertifyDiffDeletionOnlyHunkMapsToSymbol verifies that a hunk containing
// only deletions still attributes the change to the enclosing symbol.
func TestCertifyDiffDeletionOnlyHunkMapsToSymbol(t *testing.T) {
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
			Span:          core.LineRange{Start: 3, End: 7},
		},
	}, 1)

	report := CertifyDiff(cg, core.DiffInput{UnifiedDiff: `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -4,3 +4,2 @@
 	x := 1
-	log.Println(x)
 	return true
`})

	if len(report.ChangedSymbols) != 1 || report.ChangedSymbols[0].Name != "Login" {
		t.Fatalf("changed symbols = %+v, want Login for deletion-only hunk", report.ChangedSymbols)
	}
}

// TestCertifyDiffStaleIndexEscalatesToManualReview verifies the index_stale
// gate: when the indexed blob SHA no longer matches the file on disk, the
// verdict must not be allow.
func TestCertifyDiffStaleIndexEscalatesToManualReview(t *testing.T) {
	cg := graph.New()
	cg.Replace([]core.SymbolRecord{
		{
			ID:            "auth.go::Login@indexedsha",
			FilePath:      "auth.go",
			BlobSHA:       "indexedsha",
			Language:      "go",
			Kind:          core.KindFunction,
			Name:          "Login",
			QualifiedName: "Login",
			Span:          core.LineRange{Start: 3, End: 6},
		},
	}, 1)

	diff := core.DiffInput{UnifiedDiff: `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -3,4 +3,4 @@
 func Login() bool {
-	return false
+	return true
 }
`}

	stale := CertifyDiffWithStaleness(cg, diff, func(string) (string, bool) { return "differentsha", true })
	if stale.Verdict != core.VerdictManualReview {
		t.Fatalf("stale verdict = %q, want manual_review", stale.Verdict)
	}
	if len(stale.Unknowns) != 1 || stale.Unknowns[0].Code != "index_stale" {
		t.Fatalf("stale unknowns = %+v", stale.Unknowns)
	}

	missing := CertifyDiffWithStaleness(cg, diff, func(string) (string, bool) { return "", false })
	if missing.Verdict != core.VerdictManualReview {
		t.Fatalf("missing-file verdict = %q, want manual_review", missing.Verdict)
	}

	fresh := CertifyDiffWithStaleness(cg, diff, func(string) (string, bool) { return "indexedsha", true })
	if fresh.Verdict != core.VerdictAllow {
		t.Fatalf("fresh verdict = %q, want allow; unknowns=%+v", fresh.Verdict, fresh.Unknowns)
	}
}

// TestCleanDiffPathStripsTimestampSuffix covers traditional `diff -u` headers
// that append "\t<timestamp>" to the path.
func TestCleanDiffPathStripsTimestampSuffix(t *testing.T) {
	got := cleanDiffPath("b/auth.go\t2026-06-11 10:00:00.000000000 +0000")
	if got != "auth.go" {
		t.Fatalf("cleanDiffPath = %q, want auth.go", got)
	}
}

func TestParseUnifiedDiffChmodOnly(t *testing.T) {
	files, err := ParseUnifiedDiff(`diff --git a/auth.go b/auth.go
old mode 100644
new mode 100755
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %+v, want empty for mode-only change", files)
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
