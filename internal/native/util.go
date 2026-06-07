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

func appendEnv(values ...string) []string {
	return append(os.Environ(), values...)
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
