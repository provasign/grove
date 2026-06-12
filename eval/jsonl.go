package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// WriteTruth writes the header followed by one TruthEdge per line.
func WriteTruth(path string, header TruthFile, edges []TruthEdge) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	if err := enc.Encode(header); err != nil {
		return err
	}
	for _, e := range edges {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return w.Flush()
}

// ReadTruth reads a truth JSONL file produced by WriteTruth.
func ReadTruth(path string) (TruthFile, []TruthEdge, error) {
	f, err := os.Open(path)
	if err != nil {
		return TruthFile{}, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	if !sc.Scan() {
		return TruthFile{}, nil, fmt.Errorf("%s: empty truth file", path)
	}
	var header TruthFile
	if err := json.Unmarshal(sc.Bytes(), &header); err != nil {
		return TruthFile{}, nil, fmt.Errorf("%s: bad header: %w", path, err)
	}
	var edges []TruthEdge
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e TruthEdge
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return TruthFile{}, nil, fmt.Errorf("%s: bad edge line: %w", path, err)
		}
		edges = append(edges, e)
	}
	return header, edges, sc.Err()
}
