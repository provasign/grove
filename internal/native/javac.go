package native

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/provasign/grove/internal/core"
)

//go:embed javac_resolver.java.txt
var javacResolverSource string

// jdk is the JDK the resolver runs with: its home (for JAVA_HOME, which
// Maven also reads) and the java launcher.
type jdk struct{ home, java string }

const jdkProbeTimeout = 20 * time.Second

var (
	jdkOnce  sync.Once
	jdkFound *jdk
	jdkAll   []*jdk
)

// findJDK locates a working JDK 11+ (single-file source launch needs 11, and
// the resolver needs the system Java compiler, so a JRE does not count).
// Candidates, first working one wins: $JAVA_HOME (the project's chosen JDK),
// javac on PATH, macOS /usr/libexec/java_home, then Homebrew's keg-only
// openjdk formulae, which brew deliberately leaves off PATH. macOS ships
// /usr/bin/java{,c} stubs that exist but fail with "Unable to locate a Java
// Runtime" -- and can block for minutes on a GUI install prompt -- so
// they are never probed (they only delegate to java_home, which is), and
// every probe runs under a timeout.
func findJDK() *jdk {
	jdkOnce.Do(func() {
		var homes []string
		if h := os.Getenv("JAVA_HOME"); h != "" {
			homes = append(homes, h)
		}
		if p, err := exec.LookPath("javac"); err == nil && !(runtime.GOOS == "darwin" && strings.HasPrefix(p, "/usr/bin/")) {
			if r, err := filepath.EvalSymlinks(p); err == nil {
				p = r
			}
			homes = append(homes, filepath.Dir(filepath.Dir(p)))
		}
		if runtime.GOOS == "darwin" {
			ctx, cancel := context.WithTimeout(context.Background(), jdkProbeTimeout)
			out, err := exec.CommandContext(ctx, "/usr/libexec/java_home").Output()
			cancel()
			if err == nil {
				homes = append(homes, strings.TrimSpace(string(out)))
			}
			for _, pattern := range []string{"/opt/homebrew/opt/openjdk*", "/usr/local/opt/openjdk*"} {
				m, _ := filepath.Glob(pattern)
				sort.Strings(m) // "openjdk" (current) before "openjdk@17"
				homes = append(homes, m...)
			}
		}
		seen := map[string]bool{}
		for _, h := range homes {
			if r, err := filepath.EvalSymlinks(h); err == nil {
				h = r
			}
			if seen[h] {
				continue
			}
			seen[h] = true
			if j := probeJDK(h); j != nil {
				jdkAll = append(jdkAll, j)
			}
		}
		if len(jdkAll) > 0 {
			jdkFound = jdkAll[0]
		}
	})
	return jdkFound
}

// allJDKs returns every usable JDK found, in findJDK's preference order.
// Build tools lag new JDKs (Gradle 9.1 cannot run on JDK 27), so the
// classpath step tries them in turn.
func allJDKs() []*jdk {
	findJDK()
	return jdkAll
}

func probeJDK(home string) *jdk {
	exe := func(n string) string {
		if runtime.GOOS == "windows" {
			n += ".exe"
		}
		return filepath.Join(home, "bin", n)
	}
	java, javac := exe("java"), exe("javac")
	if _, err := os.Stat(java); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), jdkProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, javac, "-version")
	cmd.Env = scrubbedEnv("JAVA_HOME=" + home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	// "javac 1.8.0_392" is Java 8: no single-file launch.
	if v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "javac")); strings.HasPrefix(v, "1.") {
		return nil
	}
	return &jdk{home: home, java: java}
}

// env runs a JDK tool (the resolver, Maven) against this JDK.
func (j *jdk) env() []string {
	return scrubbedEnv("JAVA_HOME="+j.home, "PATH="+filepath.Join(j.home, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// javacClasspath asks Maven for the project's test-scope classpath, offline
// first, then online. Best-effort: without it javac still resolves every
// call whose types live in the project's own sources. -fae with appended
// output keeps every module that resolves: one unresolvable module (guava's
// gwt module needs a test jar the reactor has not built) used to fail the
// whole command and leave every module without library types.
func javacClasspath(ctx context.Context, j *jdk, root, tmp string) (string, string) {
	if untrustedMode() {
		// Maven and Gradle both execute repository-defined build logic.
		return "", "build-tool classpath skipped in untrusted mode; external types unresolved"
	}
	if !anyFile(root, "pom.xml") {
		if anyFile(root, "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts") {
			return gradleClasspath(ctx, j, root, tmp)
		}
		return "", "no Maven or Gradle build found; external types unresolved"
	}
	if !commandExists("mvn") {
		return "", "mvn not found, so the Maven classpath is unavailable; external types unresolved"
	}
	out := filepath.Join(tmp, "cp.txt")
	for _, offline := range []bool{true, false} {
		_ = os.Remove(out)
		args := []string{"-q", "-B", "-fae", "dependency:build-classpath", "-Dmdep.outputFile=" + out,
			"-Dmdep.appendOutput=true", "-Dmdep.includeScope=test"}
		if offline {
			args = append([]string{"-o"}, args...)
		}
		cmd := exec.CommandContext(ctx, "mvn", args...)
		cmd.Dir = root
		cmd.Env = j.env()
		runErr := cmd.Run()
		b, err := os.ReadFile(out)
		if err != nil {
			continue
		}
		if cp := normalizeClasspath(string(b)); cp != "" {
			if runErr != nil {
				return cp, "Maven classpath resolved for some modules only; types from the rest are unresolved"
			}
			return cp, ""
		}
	}
	return "", "Maven classpath not resolvable; external types unresolved"
}

// gradleInitScript adds a task to every project that writes its resolved
// test and main classpaths, one entry per line.
const gradleInitScript = `allprojects {
    tasks.register("groveClasspath") {
        def configs = ["testCompileClasspath", "testRuntimeClasspath", "compileClasspath", "runtimeClasspath"]
            .collect { project.configurations.findByName(it) }
            .findAll { it != null && it.canBeResolved }
        def out = new File(System.getProperty("grove.cp.out"))
        doLast {
            configs.each { c ->
                try { c.resolve().each { out << it.absolutePath + "\n" } } catch (Exception ignored) {}
            }
        }
    }
}
`

// gradleClasspath asks Gradle (the project's wrapper when present) for every
// project's resolved classpath, offline first, then online.
func gradleClasspath(ctx context.Context, j *jdk, root, tmp string) (string, string) {
	runner := filepath.Join(root, "gradlew")
	if st, err := os.Stat(runner); err != nil || st.Mode()&0o111 == 0 {
		if !commandExists("gradle") {
			return "", "no Gradle wrapper or gradle executable, so the Gradle classpath is unavailable; external types unresolved"
		}
		runner = "gradle"
	}
	script := filepath.Join(tmp, "grove-classpath.gradle")
	if err := os.WriteFile(script, []byte(gradleInitScript), 0o600); err != nil {
		return "", "Gradle classpath skipped: " + err.Error()
	}
	out := filepath.Join(tmp, "gradle-cp.txt")
	jdks := allJDKs()
	if len(jdks) > 3 {
		jdks = jdks[:3]
	}
	for _, jj := range jdks {
		if cp, note, ok := gradleClasspathWith(ctx, jj, runner, root, script, out); ok {
			return cp, note
		}
	}
	return "", "Gradle classpath not resolvable; external types unresolved"
}

func gradleClasspathWith(ctx context.Context, j *jdk, runner, root, script, out string) (string, string, bool) {
	for _, offline := range []bool{true, false} {
		_ = os.Remove(out)
		args := []string{"-q", "--continue", "--no-configuration-cache", "-I", script, "-Dgrove.cp.out=" + out, "groveClasspath"}
		if offline {
			args = append([]string{"--offline"}, args...)
		}
		cmd := exec.CommandContext(ctx, runner, args...)
		cmd.Dir = root
		cmd.Env = j.env()
		runErr := cmd.Run()
		b, err := os.ReadFile(out)
		if err != nil {
			continue
		}
		if cp := normalizeClasspath(string(b)); cp != "" {
			if runErr != nil {
				return cp, "Gradle classpath resolved for some projects only; types from the rest are unresolved", true
			}
			return cp, "", true
		}
	}
	return "", "", false
}

// javacClasspathCached reuses the last resolved classpath while the build
// files are unchanged: an incremental index then pays no Maven/Gradle start
// (seconds per edit), and a transient build-tool failure cannot silently
// strip library types from an index that had them. Any pom.xml or Gradle
// file change re-resolves.
func javacClasspathCached(ctx context.Context, j *jdk, root, tmp string) (string, string) {
	key := buildFilesKey(root, j.home)
	cachePath := filepath.Join(root, ".grove", "java-classpath.json")
	var cached struct{ Key, Classpath, Note string }
	if b, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(b, &cached) == nil &&
		key != "" && cached.Key == key && classpathEntriesExist(cached.Classpath) {
		return cached.Classpath, cached.Note
	}
	cp, note := javacClasspath(ctx, j, root, tmp)
	if key != "" && cp != "" {
		cached.Key, cached.Classpath, cached.Note = key, cp, note
		if b, err := json.Marshal(cached); err == nil {
			_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
			_ = os.WriteFile(cachePath, b, 0o644)
		}
	}
	return cp, note
}

// buildFilesKey hashes every Maven/Gradle build file under root (skipping
// build output and dependency directories) plus the JDK, or "" when there
// are none.
func buildFilesKey(root, jdkHome string) string {
	h := sha256.New()
	n := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".grove", "node_modules", "target", "build", ".gradle":
				return filepath.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gradle.properties", "libs.versions.toml":
			if b, err := os.ReadFile(path); err == nil {
				rel, _ := filepath.Rel(root, path)
				h.Write([]byte(rel + "\x00"))
				h.Write(b)
				n++
			}
		}
		return nil
	})
	if n == 0 {
		return ""
	}
	h.Write([]byte(jdkHome))
	return hex.EncodeToString(h.Sum(nil))
}

func classpathEntriesExist(cp string) bool {
	for _, e := range strings.Split(cp, string(os.PathListSeparator)) {
		if e == "" {
			continue
		}
		if _, err := os.Stat(e); err != nil {
			return false
		}
	}
	return true
}

// normalizeClasspath joins Maven's per-module classpath lines into one
// de-duplicated classpath of entries that exist.
func normalizeClasspath(raw string) string {
	seen := map[string]bool{}
	var entries []string
	for _, line := range strings.Split(raw, "\n") {
		for _, e := range strings.Split(strings.TrimSpace(line), string(os.PathListSeparator)) {
			if e == "" || seen[e] {
				continue
			}
			seen[e] = true
			if _, err := os.Stat(e); err == nil {
				entries = append(entries, e)
			}
		}
	}
	return strings.Join(entries, string(os.PathListSeparator))
}

// javaScope picks the files an incremental run re-attributes: every .java
// file in the changed files' package directories, so the indexer carries the
// stored compiler edges of every other directory forward (edges into edited
// files follow their declarations to the new symbol IDs; see the indexer's
// symbolRemap). nil means analyze everything: a cold index, a rebaseline,
// deletions, or a change touching most of the project.
func javaScope(req Request, files []string) (units []string, dirs []string) {
	if req.ChangedFiles == nil {
		return nil, nil
	}
	all := fileSet(files)
	dirSet := map[string]bool{}
	for _, f := range req.ChangedFiles {
		if !strings.HasSuffix(f, ".java") {
			continue
		}
		if !all[f] {
			return nil, nil // a deleted file: callers elsewhere need re-attribution
		}
		dirSet[packageDir(f)] = true
	}
	if len(dirSet) == 0 {
		return nil, nil
	}
	for _, f := range files {
		if dirSet[packageDir(f)] {
			units = append(units, f)
		}
	}
	if 2*len(units) > len(files) {
		return nil, nil
	}
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return units, dirs
}

var javaPackageDecl = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_$][\w$.]*)\s*;`)

func javacResolve(ctx context.Context, req Request) ([]core.Edge, []string) {
	edges, diags, _ := javacResolveScoped(ctx, req)
	return edges, diags
}

// javacResolveScoped also returns the package directories an incremental run
// re-attributed (nil for a full run).
func javacResolveScoped(ctx context.Context, req Request) ([]core.Edge, []string, []string) {
	j := findJDK()
	if j == nil {
		return nil, []string{"javac resolver skipped: no JDK 11+ found (JAVA_HOME, PATH, java_home, Homebrew openjdk); calls are name-resolved"}, nil
	}
	var files []string
	for _, f := range req.Files {
		// module-info.java switches javac to module mode, where the
		// classpath's jars sit in the unnamed module and every package they
		// export is "not visible" (939 of guava's 948 errors). Classpath mode
		// resolves the same calls.
		if strings.HasSuffix(f, ".java") && filepath.Base(f) != "module-info.java" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil, nil, nil
	}
	units, scopedDirs := javaScope(req, files)
	if units == nil {
		units = files
	}
	tmp, err := os.MkdirTemp("", "grove-javac-")
	if err != nil {
		return nil, []string{"javac resolver skipped: " + err.Error()}, nil
	}
	defer os.RemoveAll(tmp)
	src := filepath.Join(tmp, "GroveJavacResolver.java")
	cpFile := filepath.Join(tmp, "classpath.txt")
	cp, cpNote := javacClasspathCached(ctx, j, req.Root, tmp)
	if err := os.WriteFile(src, []byte(javacResolverSource), 0o600); err != nil {
		return nil, []string{"javac resolver skipped: " + err.Error()}, nil
	}
	if err := os.WriteFile(cpFile, []byte(cp), 0o600); err != nil {
		return nil, []string{"javac resolver skipped: " + err.Error()}, nil
	}
	var payload javacPayload
	batches := javaBatches(req.Root, files)
	for bi, b := range batches {
		inBatch := fileSet(b.files)
		var batchUnits []string
		for _, u := range units {
			if inBatch[u] {
				batchUnits = append(batchUnits, u)
			}
		}
		if len(batchUnits) == 0 {
			continue
		}
		// A scoped run reads the rest of its batch from source as needed; a
		// full batch lists every file already.
		sourcepath := ""
		if len(batchUnits) < len(b.files) {
			sourcepath = strings.Join(b.roots, string(os.PathListSeparator))
		}
		part, err := runJavacResolver(ctx, j, req.Root, tmp, src, cpFile, batchUnits, sourcepath, bi)
		if err != nil {
			return nil, []string{err.Error()}, nil
		}
		payload.Files += part.Files
		payload.Errors += part.Errors
		payload.Calls = append(payload.Calls, part.Calls...)
		payload.Members = append(payload.Members, part.Members...)
	}
	symbols := newSymbolLocator(req.Symbols, map[string]bool{"java": true})
	var edges []core.Edge
	// One call edge per (caller, callee); it is lambda-body only when every
	// call behind it sits in a lambda.
	type pair struct{ from, to string }
	callIdx := map[pair]int{}
	for _, c := range payload.Calls {
		from, okFrom := symbols.at(c.From, c.FromName, c.FromLine)
		to, okTo := symbols.at(c.To, c.ToName, c.ToLine)
		if !okFrom || !okTo || from.ID == to.ID {
			continue
		}
		k := pair{from.ID, to.ID}
		reason := core.EdgeReason("")
		switch {
		case c.Ref:
			reason = core.ReasonMethodRef
		case c.Lambda:
			reason = core.ReasonLambdaBody
		}
		if i, ok := callIdx[k]; ok {
			if reason == "" {
				edges[i].Reason = "" // a plain call outranks lambda/ref forms
			}
			continue
		}
		e := symbolEdge(from, to, core.EdgeCalls, 0.99)
		e.Reason = reason
		callIdx[k] = len(edges)
		edges = append(edges, e)
	}
	for _, m := range payload.Members {
		from, okFrom := symbols.at(m.From, m.FromName, m.FromLine)
		to, okTo := symbols.at(m.To, m.ToName, m.ToLine)
		if !okFrom || !okTo || from.ID == to.ID {
			continue
		}
		t := core.EdgeReads
		if m.Write {
			t = core.EdgeWrites
		}
		edges = append(edges, symbolEdge(from, to, t, 0.98))
	}
	batchNote := ""
	if len(batches) > 1 {
		batchNote = " in " + itoa(len(batches)) + " batches (source roots redefining the same classes)"
	}
	diags := []string{"javac attributed " + itoa(payload.Files) + " file(s)" + batchNote + " (" + itoa(payload.Errors) + " compile error(s) tolerated); " +
		itoa(len(payload.Calls)) + " call and " + itoa(len(payload.Members)) + " field reference candidate(s)"}
	if cpNote != "" {
		diags = append(diags, cpNote)
	}
	if scopedDirs != nil {
		diags = append(diags, "scoped to "+itoa(len(scopedDirs))+" affected package dir(s); other packages' compiler edges carried forward")
	}
	return edges, diags, scopedDirs
}

type javacRef struct {
	From     string `json:"from"`
	FromName string `json:"fromName"`
	FromLine int    `json:"fromLine"`
	To       string `json:"to"`
	ToName   string `json:"toName"`
	ToLine   int    `json:"toLine"`
	Write    bool   `json:"write"`
	Lambda   bool   `json:"lambda"`
	Ref      bool   `json:"ref"`
}

type javacPayload struct {
	Files   int        `json:"files"`
	Errors  int        `json:"errors"`
	Calls   []javacRef `json:"calls"`
	Members []javacRef `json:"members"`
}

func runJavacResolver(ctx context.Context, j *jdk, root, tmp, src, cpFile string, units []string, sourcepath string, batch int) (javacPayload, error) {
	var payload javacPayload
	list := filepath.Join(tmp, "files-"+itoa(batch)+".txt")
	spFile := filepath.Join(tmp, "sourcepath-"+itoa(batch)+".txt")
	if err := os.WriteFile(list, []byte(strings.Join(units, "\n")), 0o600); err != nil {
		return payload, fmt.Errorf("javac resolver skipped: %v", err)
	}
	if err := os.WriteFile(spFile, []byte(sourcepath), 0o600); err != nil {
		return payload, fmt.Errorf("javac resolver skipped: %v", err)
	}
	cmd := exec.CommandContext(ctx, j.java, "-Xss8m", src, root, list, cpFile, spFile)
	cmd.Dir = tmp
	cmd.Env = j.env()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return payload, fmt.Errorf("javac resolver failed: %v: %s", err, clip(firstLines(stderr.String(), 3), 400))
	}
	if err := unmarshalJSON(out, &payload); err != nil {
		return payload, fmt.Errorf("javac resolver JSON decode failed: %v", err)
	}
	return payload, nil
}

type javaBatch struct {
	roots []string // absolute source roots
	files []string // repo-relative .java files under them
}

// javaBatches groups the project's source roots so no batch holds two
// definitions of the same class. Compiled together, duplicates (guava's
// guava/ and android/guava/ trees define every class twice) fail
// attribution wholesale: 29,660 errors on guava in one batch, 66 when each
// copy is attributed on its own.
func javaBatches(root string, files []string) []javaBatch {
	rootOf := map[string]string{}
	for _, f := range files {
		rootOf[f] = javaSourceRoot(root, f)
	}
	keysByRoot := map[string]map[string]bool{}
	filesByRoot := map[string][]string{}
	for _, f := range files {
		r := rootOf[f]
		rel := strings.TrimPrefix(filepath.ToSlash(f), r+"/")
		if r == "." {
			rel = filepath.ToSlash(f)
		}
		if keysByRoot[r] == nil {
			keysByRoot[r] = map[string]bool{}
		}
		keysByRoot[r][rel] = true
		filesByRoot[r] = append(filesByRoot[r], f)
	}
	roots := make([]string, 0, len(filesByRoot))
	for r := range filesByRoot {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	conflict := func(a, b string) bool {
		small, big := keysByRoot[a], keysByRoot[b]
		if len(small) > len(big) {
			small, big = big, small
		}
		for k := range small {
			if big[k] && !strings.HasSuffix(k, "package-info.java") && !strings.HasSuffix(k, "module-info.java") {
				return true
			}
		}
		return false
	}
	var groups [][]string
	for _, r := range roots {
		placed := false
		for gi := range groups {
			ok := true
			for _, other := range groups[gi] {
				if conflict(r, other) {
					ok = false
					break
				}
			}
			if ok {
				groups[gi] = append(groups[gi], r)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []string{r})
		}
	}
	batches := make([]javaBatch, 0, len(groups))
	for _, g := range groups {
		var b javaBatch
		for _, r := range g {
			b.roots = append(b.roots, filepath.Join(root, r))
			b.files = append(b.files, filesByRoot[r]...)
		}
		batches = append(batches, b)
	}
	return batches
}

// javaSourceRoot is the repo-relative source root of one file, derived from
// its package declaration ("." for the repo root; the file's own directory
// when the package does not match the layout).
func javaSourceRoot(root, file string) string {
	dir := filepath.ToSlash(filepath.Dir(file))
	data, err := osReadFile(filepath.Join(root, file))
	if err != nil {
		return dir
	}
	m := javaPackageDecl.FindSubmatch(data)
	if m == nil {
		return dir
	}
	pkgPath := strings.ReplaceAll(string(m[1]), ".", "/")
	switch {
	case dir == pkgPath:
		return "."
	case strings.HasSuffix(dir, "/"+pkgPath):
		return strings.TrimSuffix(dir, "/"+pkgPath)
	}
	return dir
}

// firstLines returns n lines of s starting at the first exception or error
// line (the JDK prints its own warnings first), or at the top.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		if strings.Contains(l, "Exception") || strings.Contains(l, "Error") {
			lines = lines[i:]
			break
		}
	}
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " | ")
}
