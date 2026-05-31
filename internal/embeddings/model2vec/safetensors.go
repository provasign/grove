package model2vec

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
)

// Safetensors is a minimal parser for the HuggingFace .safetensors format,
// scoped to what Model2Vec needs: a single float32 tensor named "embeddings"
// of shape [vocabSize, dim].
//
// Format reference: https://github.com/huggingface/safetensors
//
//   ┌─────────────────────┬──────────────────────────┬─────────────────┐
//   │ uint64 little-endian│ header_length JSON bytes │ raw tensor data │
//   │ N = header length   │ { "tensor": {...}, ... } │                 │
//   └─────────────────────┴──────────────────────────┴─────────────────┘
//
// The parser deliberately rejects anything other than the expected single
// F32 embeddings tensor — Model2Vec models follow that contract, and any
// deviation is a corrupted-file signal we want to fail loudly on.

// tensorMeta mirrors the per-tensor JSON entry in the safetensors header.
type tensorMeta struct {
	DType       string  `json:"dtype"`
	Shape       []int64 `json:"shape"`
	DataOffsets [2]int64 `json:"data_offsets"`
}

// LoadEmbeddings reads a safetensors stream and returns the embedding matrix
// as a flat []float32 of length vocabSize*dim, along with the inferred shape.
// The flat layout means row i (token id i) occupies indices [i*dim, (i+1)*dim).
func LoadEmbeddings(r io.Reader) (matrix []float32, vocabSize, dim int, err error) {
	// 1. Read 8-byte little-endian header length.
	var headerLen uint64
	if err := binary.Read(r, binary.LittleEndian, &headerLen); err != nil {
		return nil, 0, 0, fmt.Errorf("safetensors: read header length: %w", err)
	}
	// Cap at 16 MB to defend against a corrupted/malicious header field.
	const maxHeader = 16 * 1024 * 1024
	if headerLen == 0 || headerLen > maxHeader {
		return nil, 0, 0, fmt.Errorf("safetensors: bogus header length %d", headerLen)
	}

	// 2. Read the JSON header.
	headerBytes := make([]byte, headerLen)
	if _, err := io.ReadFull(r, headerBytes); err != nil {
		return nil, 0, 0, fmt.Errorf("safetensors: read header: %w", err)
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, 0, 0, fmt.Errorf("safetensors: parse header: %w", err)
	}

	// 3. Locate the embeddings tensor metadata.
	rawMeta, ok := header["embeddings"]
	if !ok {
		return nil, 0, 0, fmt.Errorf("safetensors: missing \"embeddings\" tensor")
	}
	var meta tensorMeta
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		return nil, 0, 0, fmt.Errorf("safetensors: parse embeddings meta: %w", err)
	}
	if meta.DType != "F32" {
		return nil, 0, 0, fmt.Errorf("safetensors: dtype %q not supported, want F32", meta.DType)
	}
	if len(meta.Shape) != 2 {
		return nil, 0, 0, fmt.Errorf("safetensors: shape rank %d, want 2", len(meta.Shape))
	}
	vocabSize = int(meta.Shape[0])
	dim = int(meta.Shape[1])
	if vocabSize <= 0 || dim <= 0 {
		return nil, 0, 0, fmt.Errorf("safetensors: invalid shape [%d,%d]", vocabSize, dim)
	}

	// 4. Validate the declared byte offsets match a contiguous F32 buffer
	// of exactly vocabSize*dim*4 bytes starting at offset 0 of the tensor
	// area. Anything else means there are other tensors we don't expect.
	tensorBytes := int64(vocabSize) * int64(dim) * 4
	if meta.DataOffsets[1]-meta.DataOffsets[0] != tensorBytes {
		return nil, 0, 0, fmt.Errorf(
			"safetensors: byte span %d does not match tensor size %d",
			meta.DataOffsets[1]-meta.DataOffsets[0], tensorBytes,
		)
	}

	// 5. Skip to the tensor's start offset (relative to the start of the
	// data area, which is the current reader position).
	if meta.DataOffsets[0] > 0 {
		if _, err := io.CopyN(io.Discard, r, meta.DataOffsets[0]); err != nil {
			return nil, 0, 0, fmt.Errorf("safetensors: skip to tensor: %w", err)
		}
	}

	// 6. Read the float32 buffer directly. safetensors guarantees
	// little-endian byte order.
	matrix = make([]float32, vocabSize*dim)
	rawBuf := make([]byte, tensorBytes)
	if _, err := io.ReadFull(r, rawBuf); err != nil {
		return nil, 0, 0, fmt.Errorf("safetensors: read tensor: %w", err)
	}
	for i := 0; i < len(matrix); i++ {
		bits := binary.LittleEndian.Uint32(rawBuf[i*4 : i*4+4])
		matrix[i] = math.Float32frombits(bits)
	}

	return matrix, vocabSize, dim, nil
}
