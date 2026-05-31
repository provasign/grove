package model2vec

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"
)

// buildSafetensors emits a minimal valid safetensors stream wrapping a single
// F32 "embeddings" tensor of shape [vocabSize, dim] with the given values.
// It is the test-side mirror of the format LoadEmbeddings parses.
func buildSafetensors(t *testing.T, vocabSize, dim int, values []float32) []byte {
	t.Helper()
	if len(values) != vocabSize*dim {
		t.Fatalf("buildSafetensors: %d values, want %d", len(values), vocabSize*dim)
	}
	tensorBytes := int64(vocabSize) * int64(dim) * 4
	header := map[string]any{
		"embeddings": map[string]any{
			"dtype":        "F32",
			"shape":        []int{vocabSize, dim},
			"data_offsets": []int64{0, tensorBytes},
		},
	}
	hdrJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint64(len(hdrJSON)))
	buf.Write(hdrJSON)
	for _, v := range values {
		_ = binary.Write(&buf, binary.LittleEndian, math.Float32bits(v))
	}
	return buf.Bytes()
}

func TestLoadEmbeddings_HappyPath(t *testing.T) {
	values := []float32{1, 2, 3, 4, 5, 6}
	data := buildSafetensors(t, 3, 2, values)
	matrix, vocab, dim, err := LoadEmbeddings(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if vocab != 3 || dim != 2 {
		t.Fatalf("shape = (%d, %d), want (3, 2)", vocab, dim)
	}
	if len(matrix) != 6 {
		t.Fatalf("matrix len = %d, want 6", len(matrix))
	}
	for i, want := range values {
		if matrix[i] != want {
			t.Errorf("matrix[%d] = %v, want %v", i, matrix[i], want)
		}
	}
}

func TestLoadEmbeddings_HeaderLengthZero(t *testing.T) {
	buf := make([]byte, 8) // header length = 0
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "bogus header length") {
		t.Errorf("err = %v, want bogus header length", err)
	}
}

func TestLoadEmbeddings_HeaderLengthTooLarge(t *testing.T) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, 32*1024*1024) // 32 MB > 16 MB cap
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "bogus header length") {
		t.Errorf("err = %v, want bogus header length", err)
	}
}

func TestLoadEmbeddings_TruncatedHeaderLength(t *testing.T) {
	// Only 4 bytes — io.ReadFull on the uint64 header length fails.
	_, _, _, err := LoadEmbeddings(bytes.NewReader([]byte{1, 2, 3, 4}))
	if err == nil || !strings.Contains(err.Error(), "read header length") {
		t.Errorf("err = %v, want read header length", err)
	}
}

func TestLoadEmbeddings_TruncatedHeader(t *testing.T) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, 100) // claim 100-byte header
	buf = append(buf, []byte("short")...)
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "read header") {
		t.Errorf("err = %v, want read header", err)
	}
}

func TestLoadEmbeddings_InvalidJSON(t *testing.T) {
	header := []byte("{not json")
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(header)))
	buf = append(buf, header...)
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "parse header") {
		t.Errorf("err = %v, want parse header", err)
	}
}

func TestLoadEmbeddings_MissingEmbeddingsTensor(t *testing.T) {
	header, _ := json.Marshal(map[string]any{
		"other_tensor": map[string]any{"dtype": "F32", "shape": []int{1, 1}, "data_offsets": []int{0, 4}},
	})
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(header)))
	buf = append(buf, header...)
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "missing \"embeddings\"") {
		t.Errorf("err = %v, want missing embeddings tensor", err)
	}
}

func TestLoadEmbeddings_WrongDType(t *testing.T) {
	header, _ := json.Marshal(map[string]any{
		"embeddings": map[string]any{"dtype": "F16", "shape": []int{2, 2}, "data_offsets": []int{0, 8}},
	})
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(header)))
	buf = append(buf, header...)
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "F32") {
		t.Errorf("err = %v, want F32 dtype error", err)
	}
}

func TestLoadEmbeddings_WrongShapeRank(t *testing.T) {
	header, _ := json.Marshal(map[string]any{
		"embeddings": map[string]any{"dtype": "F32", "shape": []int{4}, "data_offsets": []int{0, 16}},
	})
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(header)))
	buf = append(buf, header...)
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "shape rank") {
		t.Errorf("err = %v, want shape rank error", err)
	}
}

func TestLoadEmbeddings_InvalidShapeValues(t *testing.T) {
	header, _ := json.Marshal(map[string]any{
		"embeddings": map[string]any{"dtype": "F32", "shape": []int{0, 4}, "data_offsets": []int{0, 0}},
	})
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(header)))
	buf = append(buf, header...)
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "invalid shape") {
		t.Errorf("err = %v, want invalid shape error", err)
	}
}

func TestLoadEmbeddings_ByteSpanMismatch(t *testing.T) {
	// Declare a 4-byte span but a 2×2=16-byte tensor would be expected.
	header, _ := json.Marshal(map[string]any{
		"embeddings": map[string]any{"dtype": "F32", "shape": []int{2, 2}, "data_offsets": []int{0, 4}},
	})
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(header)))
	buf = append(buf, header...)
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "does not match tensor size") {
		t.Errorf("err = %v, want byte span mismatch", err)
	}
}

func TestLoadEmbeddings_NonZeroOffsetSkipped(t *testing.T) {
	// Build payload where the tensor sits at offset 8 instead of 0; the
	// loader is required to skip those leading bytes.
	values := []float32{7, 8}
	tensorBytes := int64(2 * 4)
	header, _ := json.Marshal(map[string]any{
		"embeddings": map[string]any{
			"dtype":        "F32",
			"shape":        []int{1, 2},
			"data_offsets": []int{8, 8 + int(tensorBytes)},
		},
	})
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(header)))
	buf = append(buf, header...)
	buf = append(buf, make([]byte, 8)...) // 8 bytes of padding
	for _, v := range values {
		_ = binary.Write(bytes.NewBuffer(nil), binary.LittleEndian, v)
		var tmp [4]byte
		binary.LittleEndian.PutUint32(tmp[:], math.Float32bits(v))
		buf = append(buf, tmp[:]...)
	}
	matrix, vocab, dim, err := LoadEmbeddings(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if vocab != 1 || dim != 2 || matrix[0] != 7 || matrix[1] != 8 {
		t.Errorf("decoded (vocab=%d, dim=%d, m=%v)", vocab, dim, matrix)
	}
}

func TestLoadEmbeddings_TruncatedTensor(t *testing.T) {
	header, _ := json.Marshal(map[string]any{
		"embeddings": map[string]any{"dtype": "F32", "shape": []int{2, 2}, "data_offsets": []int{0, 16}},
	})
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(header)))
	buf = append(buf, header...)
	buf = append(buf, []byte{0, 0, 0, 0}...) // only 4 of 16 needed bytes
	_, _, _, err := LoadEmbeddings(bytes.NewReader(buf))
	if err == nil || !strings.Contains(err.Error(), "read tensor") {
		t.Errorf("err = %v, want truncated tensor", err)
	}
}

// failingReader returns an error after the first N bytes — used to force a
// read failure mid-stream while exercising the "skip to tensor" branch.
type failingReader struct {
	src    io.Reader
	failAt int
	read   int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.read >= f.failAt {
		return 0, io.ErrUnexpectedEOF
	}
	remaining := f.failAt - f.read
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := f.src.Read(p)
	f.read += n
	return n, err
}

func TestLoadEmbeddings_SkipFailureReportsError(t *testing.T) {
	tensorBytes := int64(8)
	header, _ := json.Marshal(map[string]any{
		"embeddings": map[string]any{
			"dtype":        "F32",
			"shape":        []int{1, 2},
			"data_offsets": []int{16, 16 + int(tensorBytes)},
		},
	})
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(header)))
	buf = append(buf, header...)
	buf = append(buf, make([]byte, 16)...) // padding to be skipped
	buf = append(buf, make([]byte, tensorBytes)...)
	// Fail right after the header is read, before padding is skipped.
	failAfter := 8 + len(header)
	r := &failingReader{src: bytes.NewReader(buf), failAt: failAfter}
	_, _, _, err := LoadEmbeddings(r)
	if err == nil || !strings.Contains(err.Error(), "skip to tensor") {
		t.Errorf("err = %v, want skip to tensor", err)
	}
}
