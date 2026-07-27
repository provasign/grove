package native

import (
	"bytes"
	"encoding/json"
	"os"
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
