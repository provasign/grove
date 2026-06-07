# Changelog

## v0.5.0 - 2026-06-07

- Added native semantic analyzers for Go, Python, Java, Rust, C, C++, C#, PHP, JavaScript, and TypeScript.
- Persisted native edge source so graph consumers can distinguish AST, heuristic, and native evidence.
- Fixed symlink-root normalization so `/tmp` and `/private/tmp` resolve consistently during indexing.
- Tightened Go fallback resolution and C++ member extraction to reduce false positives and symbol loss.
- Updated documentation to describe the native enrichment architecture and current release surface.
