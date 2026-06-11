package core

import (
	"path"
	"strings"
)

// IsTestPath reports whether a repo-relative file path follows a test-file
// convention. Shared by graph edge construction and certification so both
// agree on what counts as test evidence.
func IsTestPath(filePath string) bool {
	slashed := strings.ReplaceAll(filePath, "\\", "/")
	base := path.Base(slashed)
	lower := strings.ToLower(base)

	switch {
	case strings.HasSuffix(lower, "_test.go"),
		strings.HasPrefix(lower, "test_") && strings.HasSuffix(lower, ".py"),
		strings.HasSuffix(lower, "_test.py"),
		strings.HasSuffix(lower, ".test.ts"), strings.HasSuffix(lower, ".spec.ts"),
		strings.HasSuffix(lower, ".test.tsx"), strings.HasSuffix(lower, ".spec.tsx"),
		strings.HasSuffix(lower, ".test.js"), strings.HasSuffix(lower, ".spec.js"),
		strings.HasSuffix(lower, ".test.jsx"), strings.HasSuffix(lower, ".spec.jsx"),
		strings.HasSuffix(base, "Test.java"), strings.HasSuffix(base, "Spec.java"),
		strings.HasSuffix(base, "Tests.cs"), strings.HasSuffix(base, "Test.cs"),
		strings.HasSuffix(base, "Test.php"):
		return true
	}

	// Directory conventions: rust tests/, pytest tests/, Jest __tests__/,
	// Maven src/test/java.
	for _, segment := range strings.Split(path.Dir(slashed), "/") {
		switch segment {
		case "tests", "test", "__tests__":
			return true
		}
	}
	return false
}

// IsTestSymbol reports whether a symbol is a test: either it lives in a test
// file, or it carries a test annotation (Rust #[test], JUnit @Test, xUnit
// [Fact]/[Theory], NUnit [Test]/[TestCase], MSTest [TestMethod], pytest
// markers). Annotation detection covers languages whose tests live alongside
// production code (notably Rust mod tests).
func IsTestSymbol(s *SymbolRecord) bool {
	if IsTestPath(s.FilePath) {
		return true
	}
	return HasTestAnnotation(s)
}

// HasTestAnnotation reports whether the symbol's annotations mark it as a
// test independent of file naming.
func HasTestAnnotation(s *SymbolRecord) bool {
	for _, a := range s.Annotations {
		la := strings.ToLower(strings.TrimSpace(a))
		switch {
		case strings.HasPrefix(la, "#[test"),
			strings.HasPrefix(la, "#[tokio::test"),
			strings.HasPrefix(la, "#[async_std::test"),
			la == "[test]", la == "[fact]", la == "[theory]",
			la == "[testmethod]", strings.HasPrefix(la, "[testcase"),
			la == "@test", strings.HasPrefix(la, "@test("),
			strings.HasPrefix(la, "@pytest.mark"):
			return true
		}
	}
	return false
}
