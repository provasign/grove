# Contributing to Grove

Grove accepts focused code, fixtures, benchmark adapters, documentation, and
performance work. Graph-quality changes must include positive and negative
fixtures and must not lower a gated precision, recall, or universe floor.

Run `go test ./...`, `go test ./... -race -count=1`, `go vet ./...`, and the
relevant command under `eval/`. The standalone eval module must also remain tidy
and testable with `GOWORK=off`.

Pull requests should state the operation/language tier affected, expected and
forbidden edges, commands run, compatibility impact, and any performance or
security tradeoff. Contributions are licensed under Apache-2.0.
