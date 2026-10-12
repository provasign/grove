package native

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/grove/internal/core"
)

func bytesReader(b []byte) *bytes.Reader {
	return bytes.NewReader(b)
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func stringTrim(b []byte) string {
	return strings.TrimSpace(string(b))
}

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func unmarshalJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// envAllowlist is the set of environment variables a native analyzer
// subprocess is permitted to inherit. Everything else — API keys, cloud
// credentials, npm/pip tokens, anything secret in grove's own environment —
// is dropped, so a subprocess that ends up running attacker-influenced code
// (a repo-resident analyzer plugin, a hostile go.mod toolchain) cannot exfil
// grove's secrets. Only variables the toolchains genuinely need to run are
// carried.
var envAllowlist = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true,
	"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true,
	// Windows runtime essentials.
	"SystemRoot": true, "SystemDrive": true, "windir": true, "USERPROFILE": true,
	"HOMEDRIVE": true, "HOMEPATH": true, "PATHEXT": true, "ComSpec": true,
	"LOCALAPPDATA": true,
	// Language-runtime search/config that are safe and sometimes required.
	// The Go vars let `go list` honor the operator's module setup (private
	// proxies, GOPRIVATE) in trusted mode; untrusted mode overrides the
	// network-facing ones explicitly (see goAnalyzerEnv).
	"GOPATH": true, "GOMODCACHE": true, "GOCACHE": true, "NODE_PATH": true,
	"GOPROXY": true, "GOFLAGS": true, "GOPRIVATE": true, "GONOSUMDB": true,
	"GOSUMDB": true, "GOINSECURE": true, "GONOSUMCHECK": true, "GOOS": true,
	"GOARCH": true, "GOROOT": true,
}

// scrubbedEnv returns a minimal, allowlisted environment plus the caller's
// extra KEY=VALUE entries. Use this for every subprocess that runs inside or
// against an untrusted repository.
func scrubbedEnv(extra ...string) []string {
	out := make([]string, 0, len(envAllowlist)+len(extra))
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 && envAllowlist[kv[:i]] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

// appendEnv carries only the allowlisted environment (see scrubbedEnv) plus
// the given entries. It intentionally no longer inherits the full os.Environ()
// — that leaked grove's secrets into every analyzer subprocess.
func appendEnv(values ...string) []string {
	return scrubbedEnv(values...)
}

func osReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func countNativeEdges(edges []core.Edge, edgeType core.EdgeType) int {
	count := 0
	for _, edge := range edges {
		if edge.Type == edgeType {
			count++
		}
	}
	return count
}

// positionedEdges collects native edges deduplicated by (from, type, to)
// and, for calls, the positions of every call behind each edge ("line:col"
// of the called name, Edge.Lines) — the javac pattern, shared by the Go,
// TypeScript and Rust passes. A later duplicate merges its position instead
// of being dropped: mergeEdges keeps the first of two equal native edges,
// so positions must be merged here.
type positionedEdges struct {
	edges []core.Edge
	idx   map[string]int
	pos   map[int]map[[2]int]bool
}

func newPositionedEdges() *positionedEdges {
	return &positionedEdges{idx: map[string]int{}, pos: map[int]map[[2]int]bool{}}
}

// add records e (once per from/type/to) and, for a call with line > 0, the
// call's position; col 0 means unknown.
func (p *positionedEdges) add(e core.Edge, line, col int) {
	key := e.From + "\x00" + string(e.Type) + "\x00" + e.To
	i, ok := p.idx[key]
	if !ok {
		i = len(p.edges)
		p.idx[key] = i
		p.edges = append(p.edges, e)
	}
	if e.Type == core.EdgeCalls && line > 0 {
		if p.pos[i] == nil {
			p.pos[i] = map[[2]int]bool{}
		}
		p.pos[i][[2]int{line, col}] = true
	}
}

// finish writes each call edge's positions, ascending, and returns the edges.
func (p *positionedEdges) finish() []core.Edge {
	for i, set := range p.pos {
		list := make([][2]int, 0, len(set))
		for at := range set {
			list = append(list, at)
		}
		sort.Slice(list, func(a, b int) bool {
			if list[a][0] != list[b][0] {
				return list[a][0] < list[b][0]
			}
			return list[a][1] < list[b][1]
		})
		parts := make([]string, len(list))
		for j, at := range list {
			parts[j] = strconv.Itoa(at[0])
			if at[1] > 0 {
				parts[j] += ":" + strconv.Itoa(at[1])
			}
		}
		p.edges[i].Lines = strings.Join(parts, ",")
	}
	return p.edges
}

// charColumn converts a 0-based byte offset in a source line to the 1-based
// column javac and tsc report: UTF-16 code units, so positions from every
// pass name the same token. 0 when the offset is outside the line.
func charColumn(line string, byteOff int) int {
	if byteOff < 0 || byteOff > len(line) {
		return 0
	}
	n := 0
	for _, r := range line[:byteOff] {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n + 1
}
