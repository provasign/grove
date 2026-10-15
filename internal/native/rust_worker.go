package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/provasign/grove/internal/core"
)

// A resident rust-analyzer: one language server per repository root, kept
// warm by resident engines (MCP, watch) so an edit is answered from its
// incremental analysis instead of a whole-workspace `rust-analyzer scip`
// run (tokio: ~11s per edit; the server answers the edited file in well
// under a second once warm).
//
// An edit is resolved the way the batch pass resolves the whole workspace:
// every function or type reference in the edited files (semantic tokens) is
// followed to its definition, and attributed to the innermost declaration
// holding it. Declarations the edit added or whose signature changed are
// looked up with find-references, so callers in unchanged files follow
// them. Every other edge comes from the record of the last run. Anything
// the worker cannot answer falls back to the batch pass.

type rustLSP struct {
	mu      sync.Mutex // one index run at a time
	root    string
	bin     string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	nextID  int
	pending map[int]chan rustLSPResponse
	pmu     sync.Mutex
	quiet   chan struct{} // closed once the server first reports quiescent
	qonce   sync.Once
	done    chan struct{} // closed when the process exits
	legend  []string
	open    map[string]int // open document -> version
	idle    *time.Timer
	errText *limitedBuffer
}

type rustLSPResponse struct {
	Result json.RawMessage
	Err    string
}

var rustLSPs = struct {
	sync.Mutex
	m map[string]*rustLSP
}{m: map[string]*rustLSP{}}

func rustWorkerEnabled() bool { return os.Getenv("GROVE_RUST_WORKER") != "0" }

// rustLSPFor returns the running server for root, starting one if needed.
func rustLSPFor(root, bin string) (*rustLSP, error) {
	rustLSPs.Lock()
	defer rustLSPs.Unlock()
	if w := rustLSPs.m[root]; w != nil && w.alive() {
		return w, nil
	}
	w := &rustLSP{root: root, bin: bin, pending: map[int]chan rustLSPResponse{}, quiet: make(chan struct{}), done: make(chan struct{}), open: map[string]int{}}
	if err := w.start(); err != nil {
		return nil, err
	}
	rustLSPs.m[root] = w
	return w, nil
}

func (w *rustLSP) alive() bool {
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}

func (w *rustLSP) start() error {
	cmd := exec.Command(w.bin)
	cmd.Dir = w.root
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+rustAnalyzerTargetDir(w.root))
	w.errText = &limitedBuffer{buf: &bytes.Buffer{}, max: 8 << 10}
	cmd.Stderr = w.errText
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	w.cmd, w.stdin = cmd, stdin
	go w.read(bufio.NewReaderSize(stdout, 1<<16))
	go func() {
		_ = cmd.Wait()
		close(w.done)
		w.pmu.Lock()
		for id, ch := range w.pending {
			ch <- rustLSPResponse{Err: "rust-analyzer exited"}
			delete(w.pending, id)
		}
		w.pmu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	raw, err := w.call(ctx, "initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rustFileURI(w.root),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"semanticTokens": map[string]any{
					"requests":       map[string]any{"full": true},
					"tokenTypes":     []string{},
					"tokenModifiers": []string{},
					"formats":        []string{"relative"},
				},
			},
			"experimental": map[string]any{"serverStatusNotification": true},
		},
		"initializationOptions": map[string]any{
			"checkOnSave":  false,
			"cachePriming": map[string]any{"enable": true},
			"files":        map[string]any{"watcher": "server"},
		},
	})
	if err != nil {
		w.stop()
		return fmt.Errorf("initialize: %w", err)
	}
	var init struct {
		Capabilities struct {
			SemanticTokensProvider struct {
				Legend struct {
					TokenTypes []string `json:"tokenTypes"`
				} `json:"legend"`
			} `json:"semanticTokensProvider"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &init); err != nil || len(init.Capabilities.SemanticTokensProvider.Legend.TokenTypes) == 0 {
		w.stop()
		return errors.New("initialize: no semantic token legend")
	}
	w.legend = init.Capabilities.SemanticTokensProvider.Legend.TokenTypes
	w.notify("initialized", map[string]any{})
	w.touch()
	return nil
}

// touch restarts the idle timer: the server exits after workerIdle unused.
func (w *rustLSP) touch() {
	if w.idle != nil {
		w.idle.Stop()
	}
	w.idle = time.AfterFunc(workerIdle(), w.stop)
}

func (w *rustLSP) stop() {
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
}

func (w *rustLSP) read(r *bufio.Reader) {
	for {
		length := -1
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if v, ok := strings.CutPrefix(strings.ToLower(line), "content-length:"); ok {
				length, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		if length < 0 {
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var msg struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
			Result json.RawMessage  `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		switch {
		case msg.Method != "" && msg.ID != nil:
			// A server request (workspace/configuration, progress
			// creation): an empty answer keeps the defaults.
			var reply any
			if msg.Method == "workspace/configuration" {
				reply = []any{}
			}
			w.send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": reply})
		case msg.Method == "experimental/serverStatus":
			var st struct {
				Quiescent bool `json:"quiescent"`
			}
			if json.Unmarshal(msg.Params, &st) == nil && st.Quiescent {
				w.qonce.Do(func() { close(w.quiet) })
			}
		case msg.ID != nil:
			id, err := strconv.Atoi(string(*msg.ID))
			if err != nil {
				continue
			}
			w.pmu.Lock()
			ch := w.pending[id]
			delete(w.pending, id)
			w.pmu.Unlock()
			if ch != nil {
				resp := rustLSPResponse{Result: msg.Result}
				if msg.Error != nil {
					resp.Err = msg.Error.Message
				}
				ch <- resp
			}
		}
	}
}

func (w *rustLSP) send(msg any) {
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w.stdin, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func (w *rustLSP) notify(method string, params any) {
	w.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// start sends a request and returns the channel its answer arrives on.
func (w *rustLSP) request(method string, params any) chan rustLSPResponse {
	ch := make(chan rustLSPResponse, 1)
	w.pmu.Lock()
	w.nextID++
	id := w.nextID
	w.pending[id] = ch
	w.pmu.Unlock()
	w.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return ch
}

func (w *rustLSP) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	select {
	case resp := <-w.request(method, params):
		if resp.Err != "" {
			return nil, errors.New(resp.Err)
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ready waits until the server has finished its first analysis.
func (w *rustLSP) ready(ctx context.Context) error {
	select {
	case <-w.quiet:
		return nil
	case <-w.done:
		return errors.New("rust-analyzer exited: " + lastLine(w.errText.String())) // locked: exec's copy goroutine may still write
	case <-ctx.Done():
		return errors.New("rust-analyzer still warming up")
	}
}

func rustFileURI(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path // Windows drive paths
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// rustURIFile maps a server URI back to a repo-relative path ("" outside).
func rustURIFile(root, uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	path := u.Path
	if len(path) > 2 && path[0] == '/' && path[2] == ':' {
		path = path[1:] // /C:/... on Windows
	}
	rel, ok := relFile(root, filepath.FromSlash(path))
	if !ok {
		return ""
	}
	return rel
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

// sync gives the server the current text of the edited files.
func (w *rustLSP) sync(files []string) error {
	for _, file := range files {
		data, err := osReadFile(filepath.Join(w.root, filepath.FromSlash(file)))
		if err != nil {
			return err
		}
		uri := rustFileURI(filepath.Join(w.root, filepath.FromSlash(file)))
		if v, ok := w.open[file]; ok {
			w.open[file] = v + 1
			w.notify("textDocument/didChange", map[string]any{
				"textDocument":   map[string]any{"uri": uri, "version": v + 1},
				"contentChanges": []any{map[string]any{"text": string(data)}},
			})
			continue
		}
		w.open[file] = 1
		w.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{"uri": uri, "languageId": "rust", "version": 1, "text": string(data)},
		})
	}
	return nil
}

// rustRef is one resolved reference: where it is, and the definition's
// file, line and name.
type rustRef struct {
	file string
	line int // 1-based
	col  int // 1-based UTF-16 column of the reference (0 = unknown)
	to   rustDefSite
}

type rustDefSite struct {
	file      string
	line, end int // 1-based range of the definition
	name      string
}

// fileRefs resolves every function and type reference in file (semantic
// tokens followed to their definition).
func (w *rustLSP) fileRefs(ctx context.Context, file string) ([]rustRef, error) {
	uri := rustFileURI(filepath.Join(w.root, filepath.FromSlash(file)))
	raw, err := w.call(ctx, "textDocument/semanticTokens/full", map[string]any{"textDocument": map[string]any{"uri": uri}})
	if err != nil {
		return nil, err
	}
	var toks struct {
		Data []int `json:"data"`
	}
	if err := json.Unmarshal(raw, &toks); err != nil {
		return nil, err
	}
	wanted := map[int]bool{}
	for i, name := range w.legend {
		switch name {
		case "function", "method", "struct", "enum", "interface", "typeAlias", "union":
			wanted[i] = true
		}
	}
	data, err := osReadFile(filepath.Join(w.root, filepath.FromSlash(file)))
	if err != nil {
		return nil, err
	}
	text := strings.Split(string(data), "\n")
	type pos struct {
		line, char int
		name       string
	}
	var sites []pos
	line, char := 0, 0
	for i := 0; i+4 < len(toks.Data); i += 5 {
		dl, dc, length, typ := toks.Data[i], toks.Data[i+1], toks.Data[i+2], toks.Data[i+3]
		if dl > 0 {
			line, char = line+dl, dc
		} else {
			char += dc
		}
		if !wanted[typ] || line >= len(text) {
			continue
		}
		// The name is the reference's own text: a definition range is the
		// declared name only when rust-analyzer has one to focus on.
		c0, c1 := utf16Col(text[line], char), utf16Col(text[line], char+length)
		if c0 < 0 || c1 < 0 || c0 >= c1 {
			continue
		}
		sites = append(sites, pos{line, char, strings.TrimPrefix(text[line][c0:c1], "r#")})
	}
	// Pipeline the definition requests, then collect them in order.
	chans := make([]chan rustLSPResponse, len(sites))
	for i, p := range sites {
		chans[i] = w.request("textDocument/definition", map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": p.line, "character": p.char},
		})
	}
	var out []rustRef
	dbg := map[string]int{}
	defer func() {
		if os.Getenv("GROVE_RUST_WORKER_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "rust worker %s: %d sites %v\n", file, len(sites), dbg)
		}
	}()
	for i, ch := range chans {
		var resp rustLSPResponse
		select {
		case resp = <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if resp.Err != "" {
			dbg["err:"+resp.Err]++
			continue
		}
		locs := decodeLocations(resp.Result)
		if len(locs) == 0 {
			dbg["nolocs"]++
		}
		for _, loc := range locs {
			def := rustDefSite{file: rustURIFile(w.root, loc.URI), line: loc.Range.Start.Line + 1, end: loc.Range.End.Line + 1, name: sites[i].name}
			// A one-line range is the declared name (as the batch pass reads
			// it from SCIP); it differs from the reference under `use a as b`.
			if def.file != "" && def.line == def.end {
				if name := w.nameAt(def.file, loc.Range); name != "" && name != def.name {
					continue // named by an alias (use a as b): the batch pass skips it too
				}
			}
			if def.file == "" || def.file == file && def.line == sites[i].line+1 && def.end == def.line {
				continue // outside the repository, or the declaration itself
			}
			out = append(out, rustRef{file: file, line: sites[i].line + 1, col: sites[i].char + 1, to: def})
		}
	}
	return out, nil
}

// references finds the references to the declaration named at (file, pos).
func (w *rustLSP) references(ctx context.Context, file string, at lspPosition) ([]rustRef, error) {
	uri := rustFileURI(filepath.Join(w.root, filepath.FromSlash(file)))
	raw, err := w.call(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     at,
		"context":      map[string]any{"includeDeclaration": false},
	})
	if err != nil {
		return nil, err
	}
	var out []rustRef
	for _, loc := range decodeLocations(raw) {
		if f := rustURIFile(w.root, loc.URI); f != "" {
			out = append(out, rustRef{file: f, line: loc.Range.Start.Line + 1})
		}
	}
	return out, nil
}

// documentSymbols returns the name positions of every declaration in file.
func (w *rustLSP) documentSymbols(ctx context.Context, file string) ([]rustDocSymbol, error) {
	uri := rustFileURI(filepath.Join(w.root, filepath.FromSlash(file)))
	raw, err := w.call(ctx, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": uri}})
	if err != nil {
		return nil, err
	}
	var top []rustDocSymbol
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	var out []rustDocSymbol
	var walk func([]rustDocSymbol)
	walk = func(list []rustDocSymbol) {
		for _, s := range list {
			out = append(out, s)
			walk(s.Children)
		}
	}
	walk(top)
	return out, nil
}

type rustDocSymbol struct {
	Name           string          `json:"name"`
	SelectionRange lspRange        `json:"selectionRange"`
	Children       []rustDocSymbol `json:"children"`
}

func decodeLocations(raw json.RawMessage) []lspLocation {
	var many []lspLocation
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	var one lspLocation
	if json.Unmarshal(raw, &one) == nil && one.URI != "" {
		return []lspLocation{one}
	}
	return nil
}

// nameAt returns the text of a one-line range in a repository file.
func (w *rustLSP) nameAt(file string, r lspRange) string {
	data, err := osReadFile(filepath.Join(w.root, filepath.FromSlash(file)))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if r.Start.Line >= len(lines) {
		return ""
	}
	text := lines[r.Start.Line]
	c0, c1 := utf16Col(text, r.Start.Character), utf16Col(text, r.End.Character)
	if c0 < 0 || c1 < 0 || c0 >= c1 {
		return ""
	}
	return strings.TrimPrefix(text[c0:c1], "r#")
}

// utf16Col converts an LSP UTF-16 column to a byte offset in line.
func utf16Col(line string, col int) int {
	units := 0
	for i, r := range line {
		if units >= col {
			return i
		}
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
	}
	if units >= col {
		return len(line)
	}
	return -1
}

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	mu  sync.Mutex
	buf *bytes.Buffer
	max int
}

func (b *limitedBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

// rustWorkerUpdate answers an incremental run from the resident server: the
// edited files, and the files referencing a declaration the edit removed,
// added or re-signed, are re-resolved; every other edge comes from the
// record. It returns the complete rust-analyzer edge set. ok is false when
// the caller must run the batch pass instead.
func rustWorkerUpdate(ctx context.Context, req Request, record *rustScipCacheFile, macroFiles map[string]string) ([]core.Edge, string, bool) {
	if !rustWorkerEnabled() || !req.Resident || record == nil || req.FileSetChanged || len(req.ChangedFiles) == 0 {
		return nil, "", false
	}
	var changed []string
	for _, f := range req.ChangedFiles {
		if !strings.HasSuffix(f, ".rs") {
			continue
		}
		// Items a macro generates are declared by the batch pass only, and
		// an edited macro_rules! changes every expansion of it.
		if slices.Contains(record.Declared, f) || strings.ContainsAny(macroFiles[f], "([") || rustDefinesMacro(req.Root, f) {
			return nil, "", false
		}
		changed = append(changed, f)
	}
	bin, _ := rustAnalyzerBin()
	if bin == "" {
		return nil, "", false
	}
	w, err := rustLSPFor(req.Root, bin)
	if err != nil {
		return nil, "rust-analyzer server failed to start: " + err.Error(), false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.touch()
	if err := w.ready(ctx); err != nil {
		return nil, err.Error(), false
	}
	if err := w.sync(changed); err != nil {
		return nil, "rust-analyzer sync failed: " + err.Error(), false
	}

	symbols := req.Symbols
	changedSet := map[string]bool{}
	for _, f := range changed {
		changedSet[f] = true
	}
	// Declarations the edit removed, added or re-signed, by exact identity
	// (cfg twins share a qualified name). Rust signatures are explicit, so a
	// change reaches other files only through their references to these:
	// those files are re-resolved whole (an inferred type may change with
	// the declaration, e.g. a renamed constructor leaves its callers' later
	// method calls unresolved).
	identity := func(s core.SymbolRecord) string {
		return s.FilePath + "\x00" + s.QualifiedName + "\x00" + string(s.Kind) + "\x00" + s.Signature
	}
	before := map[string]bool{}
	for _, s := range req.PrevChanged {
		before[identity(s)] = true
	}
	now := map[string]bool{}
	var added []core.SymbolRecord
	for _, s := range symbols {
		if s.Language != "rust" || !changedSet[s.FilePath] {
			continue
		}
		now[identity(s)] = true
		if (callableKind(s.Kind) || typeKind(s.Kind)) && !before[identity(s)] {
			added = append(added, s)
		}
	}
	gone := map[string]bool{} // file, qualified name, kind of removed or re-signed declarations
	for _, s := range req.PrevChanged {
		if !now[identity(s)] {
			gone[s.FilePath+"\x00"+s.QualifiedName+"\x00"+string(s.Kind)] = true
		}
	}
	affected := map[string]bool{}
	for f := range changedSet {
		affected[f] = true
	}
	for _, c := range record.Edges {
		if gone[c.ToFile+"\x00"+c.ToName+"\x00"+c.ToKind] {
			affected[c.FromFile] = true
		}
	}
	if len(added) > 0 {
		names := map[string][]rustDocSymbol{}
		for _, f := range changed {
			ds, err := w.documentSymbols(ctx, f)
			if err != nil {
				return nil, "rust-analyzer symbols failed: " + err.Error(), false
			}
			names[f] = ds
		}
		for _, s := range added {
			at, ok := rustNamePosition(names[s.FilePath], s)
			if !ok {
				continue
			}
			rs, err := w.references(ctx, s.FilePath, at)
			if err != nil {
				return nil, "rust-analyzer references failed: " + err.Error(), false
			}
			for _, r := range rs {
				affected[r.file] = true
			}
		}
	}
	if len(affected) > rustWorkerMaxFiles {
		return nil, "edit affects " + itoa(len(affected)) + " files", false
	}
	var refs []rustRef
	for f := range affected {
		r, err := w.fileRefs(ctx, f)
		if err != nil {
			return nil, "rust-analyzer references failed: " + err.Error(), false
		}
		refs = append(refs, r...)
	}

	resolver := newRustEdgeResolver(symbols)
	pe := newPositionedEdges()
	add := func(e core.Edge) { pe.add(e, 0, 0) } // kept edges carry their own Lines
	// Kept: the record's edges from files the edit does not affect,
	// re-resolved by name (an edited file's declarations get new IDs).
	kept := &rustScipCacheFile{Version: record.Version, Declared: record.Declared}
	for _, c := range record.Edges {
		if !affected[c.FromFile] {
			kept.Edges = append(kept.Edges, c)
		}
	}
	for _, e := range rustCachedEdges(kept, symbols) {
		add(e)
	}
	for _, r := range refs {
		if e, ok := resolver.edge(r.file, r.line, resolver.target(r.to)); ok {
			pe.add(e, r.line, r.col)
		}
	}
	return pe.finish(), "", true
}

// rustWorkerMaxFiles bounds the files one edit may re-resolve through the
// server; a wider edit (renaming a widely used function) runs the batch
// pass, which re-resolves everything at a fixed cost.
const rustWorkerMaxFiles = 50

func rustDefinesMacro(root, file string) bool {
	data, err := osReadFile(filepath.Join(root, filepath.FromSlash(file)))
	return err == nil && strings.Contains(maskCode("rust", string(data)), "macro_rules!")
}

// rustNamePosition finds the name position of s among the server's
// document symbols: same name, on a line inside s's span.
func rustNamePosition(list []rustDocSymbol, s core.SymbolRecord) (lspPosition, bool) {
	for _, d := range list {
		line := d.SelectionRange.Start.Line + 1
		if d.Name == s.Name && s.Span.Start <= line && line <= s.Span.End {
			return d.SelectionRange.Start, true
		}
	}
	return lspPosition{}, false
}

// rustEdgeResolver applies the batch pass's attribution rules (rustScipPass)
// to references the server resolved.
type rustEdgeResolver struct {
	all        []core.SymbolRecord
	index      map[string]int
	byFileName map[string][]int
	callers    map[string][]int
	declared   []bool
}

func newRustEdgeResolver(symbols []core.SymbolRecord) *rustEdgeResolver {
	r := &rustEdgeResolver{index: map[string]int{}, byFileName: map[string][]int{}, callers: map[string][]int{}}
	for _, s := range symbols {
		if s.Language != "rust" {
			continue
		}
		i := len(r.all)
		r.all = append(r.all, s)
		r.index[s.ID] = i
		r.byFileName[s.FilePath+"\x00"+s.Name] = append(r.byFileName[s.FilePath+"\x00"+s.Name], i)
		if callableKind(s.Kind) || typeKind(s.Kind) {
			r.callers[s.FilePath] = append(r.callers[s.FilePath], i)
		}
		r.declared = append(r.declared, slices.Contains(s.Modifiers, "compiler-declared"))
	}
	return r
}

func (r *rustEdgeResolver) innermost(candidates []int, line int) int {
	best := -1
	for _, i := range candidates {
		span := r.all[i].Span
		if span.Start <= line && line <= span.End &&
			(best < 0 || span.End-span.Start < r.all[best].Span.End-r.all[best].Span.Start) {
			best = i
		}
	}
	return best
}

// target is the innermost declaration of that name overlapping the
// definition range (a name range when rust-analyzer focuses one, else the
// item's range).
func (r *rustEdgeResolver) target(def rustDefSite) int {
	best := -1
	for _, i := range r.byFileName[def.file+"\x00"+def.name] {
		span := r.all[i].Span
		if span.Start <= def.end && def.line <= span.End &&
			(best < 0 || span.End-span.Start < r.all[best].Span.End-r.all[best].Span.Start) {
			best = i
		}
	}
	return best
}

func (r *rustEdgeResolver) edge(file string, line, to int) (core.Edge, bool) {
	if to < 0 {
		return core.Edge{}, false
	}
	from := r.innermost(r.callers[file], line)
	if from < 0 || from == to {
		return core.Edge{}, false
	}
	switch {
	case callableKind(r.all[to].Kind) && (callableKind(r.all[from].Kind) || r.declared[from]):
		return symbolEdge(r.all[from], r.all[to], core.EdgeCalls, 0.98), true
	case typeKind(r.all[to].Kind) && (r.declared[from] || r.declared[to]):
		return symbolEdge(r.all[from], r.all[to], core.EdgeUsesType, 0.98), true
	}
	return core.Edge{}, false
}

// rustWorkerWarm starts the resident server for a root in the background,
// so it is warm by the first edit.
func rustWorkerWarm(req Request) {
	if !rustWorkerEnabled() || !req.Resident {
		return
	}
	bin, _ := rustAnalyzerBin()
	if bin == "" {
		return
	}
	go func() { _, _ = rustLSPFor(req.Root, bin) }()
}
