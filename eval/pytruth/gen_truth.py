#!/usr/bin/env python3
"""Dynamic call-edge ground truth for Python repos.

Runs the repo's own pytest suite under sys.setprofile and records every
caller->callee pair where both endpoints are non-test functions defined in
the repo. Emits the grove-eval truth JSONL schema (header line + one edge
per line).

This oracle is exact but partial: every edge it asserts really executed
(no false edges), but unexecuted paths are absent. Grove's RECALL against
it is meaningful; Grove's PRECISION against it is a lower bound, because a
correct static edge on an untested path scores as a false positive. The
generator name ("py-dynamic-pytest") marks scorecards so readers apply
that caveat.

Usage:
    python3 gen_truth.py --repo /path/to/repo --out truth.jsonl \
        [--commit SHA] [--pytest-arg=-x ...]
"""

import argparse
import ast
import json
import os
import sys
import threading

EXCLUDED_DIR_PARTS = {"tests", "test", "testing", "examples", "docs", ".venv", "venv", "build"}


def is_test_path(rel: str) -> bool:
    base = os.path.basename(rel)
    if base.startswith("test_") or base.endswith("_test.py") or base == "conftest.py":
        return True
    return any(part in EXCLUDED_DIR_PARTS for part in rel.split(os.sep)[:-1])


def build_def_lines(repo: str) -> dict:
    """Map (relpath, dotted qualname) -> def line, for closure attribution."""
    out = {}
    for dirpath, dirnames, filenames in os.walk(repo):
        dirnames[:] = [d for d in dirnames if not d.startswith(".") and d not in EXCLUDED_DIR_PARTS]
        for fn in filenames:
            if not fn.endswith(".py"):
                continue
            full = os.path.join(dirpath, fn)
            rel = os.path.relpath(full, repo)
            try:
                tree = ast.parse(open(full, encoding="utf-8", errors="replace").read())
            except SyntaxError:
                continue

            def walk(node, prefix):
                for child in ast.iter_child_nodes(node):
                    if isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef)):
                        q = f"{prefix}{child.name}" if prefix else child.name
                        out[(rel, q)] = child.lineno
                        walk(child, q + ".")
                    elif isinstance(child, ast.ClassDef):
                        q = f"{prefix}{child.name}" if prefix else child.name
                        walk(child, q + ".")
                    else:
                        walk(child, prefix)

            walk(tree, "")
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--tests-out", default="", help="also emit test→function coverage truth JSONL")
    ap.add_argument("--commit", default="")
    ap.add_argument("--pytest-arg", action="append", default=[], dest="pytest_args")
    args = ap.parse_args()

    repo = os.path.realpath(args.repo)
    def_lines = build_def_lines(repo)
    edges = set()
    funcs = set()

    def ref_for(code):
        """code object -> (rel, line, name) or None if outside scope."""
        fn = code.co_filename
        if not fn.startswith(repo + os.sep):
            return None
        rel = os.path.relpath(fn, repo)
        if is_test_path(rel):
            return None
        qual = getattr(code, "co_qualname", code.co_name)
        if qual.endswith("<module>") or "<" in code.co_name:
            return None
        # co_firstlineno points at the first decorator; normalize to the
        # `def` line via the AST map so identities match symbol extractors.
        line = def_lines.get((rel, qual), code.co_firstlineno)
        if "<locals>" in qual:
            # Attribute nested functions to their enclosing named def,
            # mirroring the Go oracle's closure attribution.
            outer = qual.split(".<locals>.")[0]
            outer_line = def_lines.get((rel, outer))
            if outer_line is None:
                return None
            return (rel.replace(os.sep, "/"), outer_line, outer)
        return (rel.replace(os.sep, "/"), line, qual)

    main_thread = threading.get_ident()
    test_edges = set()

    class TestTracker:
        """Pytest plugin: tracks the currently executing test so the
        profiler can attribute covered functions to it."""

        def __init__(self):
            self.current = None

        def pytest_runtest_setup(self, item):
            path, line0, name = item.location
            # Strip parametrization ("test_x[301]" → "test_x") so instances
            # collapse onto the declared function and match symbol extractors.
            if "[" in name:
                name = name[: name.index("[")]
            self.current = (
                path.replace(os.sep, "/"),
                (line0 or 0) + 1,
                name.replace("::", "."),
            )

        def pytest_runtest_teardown(self, item):
            self.current = None

    tracker = TestTracker()

    def profiler(frame, event, arg):
        if event != "call" or threading.get_ident() != main_thread:
            return
        callee = ref_for(frame.f_code)
        if callee is None:
            return
        funcs.add(callee)
        if tracker.current is not None:
            test_edges.add((tracker.current, callee))
        back = frame.f_back
        while back is not None:
            caller = ref_for(back.f_code)
            if caller is not None:
                funcs.add(caller)
                if caller != callee:
                    edges.add((caller, callee))
                return
            back = back.f_back

    sys.path.insert(0, repo)
    os.chdir(repo)
    import pytest  # noqa: PLC0415

    pytest_args = args.pytest_args or ["-q", "-p", "no:cacheprovider", "--no-header"]
    sys.setprofile(profiler)
    threading.setprofile(profiler)
    try:
        pytest.main(pytest_args, plugins=[tracker])
    finally:
        sys.setprofile(None)
        threading.setprofile(None)

    header = {
        "schema": "grove-eval/calls/v1",
        "repo": os.path.basename(repo),
        "commit": args.commit,
        "generator": "py-dynamic-pytest",
        "functions": len(funcs),
        "edges": len(edges),
    }
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(json.dumps(header) + "\n")
        for (cf, cl, cn), (ef, el, en) in sorted(edges):
            f.write(json.dumps({
                "caller": {"file": cf, "line": cl, "name": cn},
                "callee": {"file": ef, "line": el, "name": en},
            }) + "\n")
    print(f"truth: {len(funcs)} functions, {len(edges)} edges -> {args.out}", file=sys.stderr)

    if args.tests_out:
        tests = {t for t, _ in test_edges}
        theader = {
            "schema": "grove-eval/tests/v1",
            "repo": os.path.basename(repo),
            "commit": args.commit,
            "generator": "py-dynamic-pytest",
            "functions": len(tests),
            "edges": len(test_edges),
        }
        with open(args.tests_out, "w", encoding="utf-8") as f:
            f.write(json.dumps(theader) + "\n")
            for (tf, tl, tn), (ef, el, en) in sorted(test_edges):
                f.write(json.dumps({
                    "caller": {"file": tf, "line": tl, "name": tn},
                    "callee": {"file": ef, "line": el, "name": en},
                }) + "\n")
        print(f"tests truth: {len(tests)} tests, {len(test_edges)} coverage edges -> {args.tests_out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
