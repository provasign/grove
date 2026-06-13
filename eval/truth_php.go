package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// PHP call-edge ground truth from an Xdebug function trace of the repo's own
// test suite — a dynamic, exact-but-partial oracle, the same design as
// Python's pytruth: every asserted edge really executed, untested paths are
// absent, so recall is the headline and precision a lower bound.
//
// The repo's phpunit runs under `xdebug.mode=trace` with boot.php prepended;
// boot.php dumps a reflection map of in-repo declaration locations at
// shutdown. We reconstruct caller→callee edges from the trace's call-stack
// levels (closures collapse to their enclosing function, mirroring Grove)
// and resolve each to a declaration via the reflection map.
//
// Requires php with the xdebug extension and the repo's dev dependencies
// installed (composer install). Used only to generate the snapshot; CI
// scores the committed snapshot with no PHP toolchain.

type phpReflEntry struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Name string `json:"name"`
}

func PHPCallTruth(repoRoot string) (TruthFile, []TruthEdge, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return TruthFile{}, nil, err
	}
	php, err := exec.LookPath("php")
	if err != nil {
		return TruthFile{}, nil, fmt.Errorf("php not found on PATH")
	}
	phpunit := filepath.Join(root, "vendor", "bin", "phpunit")
	if _, err := os.Stat(phpunit); err != nil {
		return TruthFile{}, nil, fmt.Errorf("vendor/bin/phpunit missing — run composer install in %s", root)
	}
	boot, err := phptruthBoot()
	if err != nil {
		return TruthFile{}, nil, err
	}

	tmp, err := os.MkdirTemp("", "grove-php-truth-*")
	if err != nil {
		return TruthFile{}, nil, err
	}
	defer os.RemoveAll(tmp)
	reflPath := filepath.Join(tmp, "refl.json")

	// Run the suite under an Xdebug function trace (format 1, uncompressed).
	cmd := exec.Command(php,
		"-dxdebug.mode=trace",
		"-dxdebug.start_with_request=yes",
		"-dxdebug.trace_format=1",
		"-dxdebug.output_dir="+tmp,
		"-dxdebug.trace_output_name=trace",
		"-dxdebug.use_compression=0",
		"-dauto_prepend_file="+boot,
		"vendor/bin/phpunit", "--no-coverage",
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PHPTRUTH_ROOT="+root, "PHPTRUTH_REFL="+reflPath)
	// phpunit's exit code is nonzero when tests fail; the trace is still
	// valid, so don't treat a test failure as an oracle failure.
	out, runErr := cmd.CombinedOutput()

	refl, err := loadPHPRefl(reflPath)
	if err != nil {
		return TruthFile{}, nil, fmt.Errorf("reflection map: %w (phpunit said:\n%s)", err, tailBytes(out))
	}
	tracePath := filepath.Join(tmp, "trace.xt")
	if _, err := os.Stat(tracePath); err != nil {
		return TruthFile{}, nil, fmt.Errorf("no trace produced (xdebug enabled? phpunit said:\n%s, runErr=%v)", tailBytes(out), runErr)
	}

	edges, err := parsePHPTrace(tracePath, refl)
	if err != nil {
		return TruthFile{}, nil, err
	}

	funcs := map[string]phpReflEntry{}
	for _, e := range edges {
		funcs[e.Caller.funcKey()] = phpReflEntry{e.Caller.File, e.Caller.Line, e.Caller.Name}
		funcs[e.Callee.funcKey()] = phpReflEntry{e.Callee.File, e.Callee.Line, e.Callee.Name}
	}
	header := TruthFile{
		Schema:    "grove-eval/calls/v1",
		Repo:      filepath.Base(root),
		Generator: "xdebug-trace",
		Functions: len(funcs),
		Edges:     len(edges),
	}
	return header, edges, nil
}

func loadPHPRefl(path string) (map[string]phpReflEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]phpReflEntry
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// parsePHPTrace reconstructs caller→callee edges from an Xdebug format-1
// trace. Each entry record (type field "0") names the called function and
// its stack level; the caller is the nearest enclosing non-closure frame, so
// in-repo closures attribute to the function that defines them (as Grove
// does) while internal higher-order frames terminate attribution.
func parsePHPTrace(path string, refl map[string]phpReflEntry) ([]TruthEdge, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)

	stack := make([]string, 0, 256) // index = level; value = function name
	seen := map[[2]string]bool{}
	var edges []TruthEdge

	for sc.Scan() {
		line := sc.Text()
		// Entry records have at least: level, num, "0", time, mem, func, ...
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 6 || fields[2] != "0" {
			continue
		}
		level, err := strconv.Atoi(fields[0])
		if err != nil || level < 1 {
			continue
		}
		fn := fields[5]
		// Grow/seat the stack at this level.
		for len(stack) <= level {
			stack = append(stack, "")
		}
		stack[level] = fn
		for i := level + 1; i < len(stack); i++ {
			stack[i] = ""
		}

		callee, ok := refl[fn]
		if !ok {
			continue // internal or vendor callee
		}
		// Nearest enclosing non-closure frame is the caller.
		caller := ""
		for l := level - 1; l >= 1; l-- {
			if l < len(stack) && phpIsClosure(stack[l]) {
				continue
			}
			if l < len(stack) {
				caller = stack[l]
			}
			break
		}
		callerRef, ok := refl[caller]
		if !ok || caller == fn {
			continue
		}
		ck := callerRef.File + "\x00" + strconv.Itoa(callerRef.Line) + "\x00" + callerRef.Name
		ek := callee.File + "\x00" + strconv.Itoa(callee.Line) + "\x00" + callee.Name
		if ck == ek {
			continue
		}
		key := [2]string{ck, ek}
		if seen[key] {
			continue
		}
		seen[key] = true
		edges = append(edges, TruthEdge{
			Caller: FuncRef{File: callerRef.File, Line: callerRef.Line, Name: callerRef.Name},
			Callee: FuncRef{File: callee.File, Line: callee.Line, Name: callee.Name},
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Caller != b.Caller {
			return a.Caller.funcKey() < b.Caller.funcKey()
		}
		return a.Callee.funcKey() < b.Callee.funcKey()
	})
	return edges, nil
}

func phpIsClosure(name string) bool {
	return strings.Contains(name, "{closure")
}

// phptruthBoot locates the auto-prepend bootstrap shipped with the harness.
func phptruthBoot() (string, error) {
	if env := os.Getenv("GROVE_EVAL_PHPTRUTH_BOOT"); env != "" {
		return env, nil
	}
	for _, cand := range []string{"phptruth/boot.php", "eval/phptruth/boot.php"} {
		if abs, err := filepath.Abs(cand); err == nil {
			if st, err := os.Stat(abs); err == nil && !st.IsDir() {
				return abs, nil
			}
		}
	}
	return "", fmt.Errorf("phptruth/boot.php not found (set $GROVE_EVAL_PHPTRUTH_BOOT)")
}

func tailBytes(b []byte) string {
	s := strings.TrimSpace(string(b))
	if lines := strings.Split(s, "\n"); len(lines) > 10 {
		return strings.Join(lines[len(lines)-10:], "\n")
	}
	return s
}
