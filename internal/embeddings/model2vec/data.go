package model2vec

import _ "embed"

// embeddedModel is the bundled potion-base-8M model in safetensors format.
// Layout: 8-byte LE header length + JSON header + raw float32 matrix of
// shape [29528, 256]. Total size ≈ 29 MB.
//
//go:embed data/embeddings.safetensors
var embeddedModel []byte

// embeddedVocab is the bundled BERT WordPiece vocabulary (one token per
// line; line N maps to token ID N). Total size ≈ 210 KB.
//
//go:embed data/vocab.txt
var embeddedVocab []byte
