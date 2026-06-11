# CertifyDiff — known issues to fix

> **Status: RESOLVED.** All three issues below were fixed in commit b43c372
> ("Fix three CertifyDiff issues from docs/certify-diff-issues.md"). Kept for
> historical context only. Two further CertifyDiff fixes (context-line hunk
> over-approximation and the index_stale freshness gate) landed in the
> 2026-06-11 fix pass — see `docs/grove-assessment-2026-06-11.md` §H1/§H2.

Three issues identified by code review of `internal/cert/cert.go` (v0.5.0).

---

## Issue 1 — `RequireTestsForCode` policy field is unconditionally overridden

**Location:** `cert.go:25–27`

```go
policy := input.Policy
if !policy.RequireTestsForCode {
    policy.RequireTestsForCode = true
}
```

This forces `RequireTestsForCode = true` regardless of what the caller passes in
`DiffInput.Policy`. As a result, `CertificationPolicy` is dead configuration — a caller
that explicitly sets `RequireTestsForCode: false` (e.g., for a docs-only diff or a policy
that defers test checking to a runtime gate) gets no effect.

**Fix:** Remove the override entirely. Callers that need the default-on behaviour should
set `RequireTestsForCode: true` before calling, or the field's zero value should map to
"off" with an explicit opt-in.

Alternatively, if the intent is that the conservative default is `true`, the documentation
should say so and the condition should be inverted: apply a default only when the field is
unset and the caller did not explicitly turn it off. The cleanest approach is to add a
sentinel (e.g. `RequireTestsForCode *bool`) so zero-value and explicit-false are
distinguishable.

---

## Issue 2 — Missing-test detection is all-or-nothing across the whole changeset

**Location:** `cert.go:50–53`, `missingTestFindings` (line 198)

```go
if policy.RequireTestsForCode {
    report.Unknowns = append(report.Unknowns, missingTestFindings(report.ChangedSymbols, report.Tests)...)
}
```

`missingTestFindings` returns at most one finding and only when `len(tests) == 0`. If a
changeset touches five functions and only one has a covering test in the graph, `CertifyDiff`
returns `allow` because `len(tests) > 0` — the other four untested functions are silently
ignored.

**Fix:** Evaluate test coverage per changed symbol, not per report. For each changed symbol
that `requiresTestEvidence` is true, check whether any test in `report.Tests` covers that
specific symbol (i.e. has an inbound edge to it). Only symbols with no covering test should
produce an `Unknowns` entry.

This requires passing the edge set through to `missingTestFindings` (or a new helper) so the
per-symbol reachability check can be performed. Rough signature:

```go
func missingTestFindings(
    changed []core.SymbolRecord,
    tests []core.SymbolRecord,
    edges []core.Edge,
) []core.CertificationFinding
```

The per-symbol verdict should be a `warning` (not `block`) so the aggregate verdict stays
`manual_review` — callers can escalate if needed.

---

## Issue 3 — `finish()` rejects content-free file changes (e.g. chmod)

**Location:** `cert.go:426–429`

```go
if len(p.files[i].Hunks) == 0 && !p.files[i].Binary && !p.files[i].Deleted {
    return nil, fmt.Errorf("diff_malformed: %s has no hunks", p.files[i].Path)
}
```

A file-mode-only change (`git diff` with only `old mode`/`new mode` lines, no content delta)
produces a diff file entry with zero hunks, not marked deleted or binary. The current code
returns a `diff_malformed` error for the entire diff, which escalates to `VerdictBlock`.

**Fix:** Treat a file with zero hunks, not deleted, not binary as a no-op entry — skip it
rather than erroring. The content is unchanged; there is nothing to map to symbols.

```go
if len(p.files[i].Hunks) == 0 && !p.files[i].Binary && !p.files[i].Deleted {
    // mode-only or rename-only change — no content delta, skip silently
    continue
}
```

A separate test case should verify that a chmod-only diff produces an empty (nil) file
list rather than an error.

---

## Priority

| # | Severity | Impact |
|---|----------|--------|
| 1 | Medium | Policy field silently ignored; caller cannot disable test requirement |
| 2 | Medium | Over-permissive: multi-function changes pass with a single covering test |
| 3 | Low | Rare but produces a hard `block` verdict for a benign diff |
