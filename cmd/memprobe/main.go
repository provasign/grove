// memprobe measures Grove's indexing memory on a repository: peak heap
// during a forced cold index (with a heap profile written to
// /tmp/zzmemprobe-heap.pprof whenever the peak grows by 10%), the size of
// stored symbol text against the source tree and the database, and the
// resident heap after Open + first query (what a long-lived MCP session
// holds). Usage: go run ./cmd/memprobe <repo>. Not part of the release.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync/atomic"
	"time"

	"github.com/provasign/grove/internal/index"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
	"github.com/provasign/grove/pkg/grove"
)

func mb(b uint64) float64 { return float64(b) / (1 << 20) }

func main() {
	root := os.Args[1]
	ctx := context.Background()

	var peakInuse, peakSys atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var ms runtime.MemStats
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				runtime.ReadMemStats(&ms)
				if ms.HeapInuse > peakInuse.Load() {
					if ms.HeapInuse > peakInuse.Load()*11/10 {
						if f, err := os.Create("/tmp/grove-memprobe-heap.pprof"); err == nil {
							_ = pprof.WriteHeapProfile(f)
							f.Close()
						}
					}
					peakInuse.Store(ms.HeapInuse)
				}
				if ms.Sys > peakSys.Load() {
					peakSys.Store(ms.Sys)
				}
			}
		}
	}()

	st, err := store.Open(root)
	if err != nil {
		panic(err)
	}
	idx := index.New(parser.NewEngine(), st)
	start := time.Now()
	_, res, err := idx.IndexWithOptions(ctx, root, index.Options{Force: true, SkipNoopGraph: true})
	if err != nil {
		panic(err)
	}
	close(stop)
	fmt.Printf("index: files=%d symbols=%d edges=%d wall=%.1fs\n", res.FilesSeen, res.SymbolCount, res.EdgeCount, time.Since(start).Seconds())
	fmt.Printf("index peak: heapInuse=%.0f MB sys=%.0f MB\n", mb(peakInuse.Load()), mb(peakSys.Load()))

	// Storage: raw_text vs source bytes vs db size.
	symbols, err := st.AllSymbols(ctx)
	if err != nil {
		panic(err)
	}
	var raw, sig int64
	for _, s := range symbols {
		raw += int64(len(s.RawText))
		sig += int64(len(s.Signature)) + int64(len(s.Docstring))
	}
	var src int64
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == ".grove") {
				return filepath.SkipDir
			}
			return nil
		}
		if parser.SupportedFile(p) {
			if fi, e := d.Info(); e == nil {
				src += fi.Size()
			}
		}
		return nil
	})
	dbfi, _ := os.Stat(filepath.Join(root, ".grove", "grove.db"))
	fmt.Printf("storage: source=%.0f MB raw_text=%.0f MB (%.1fx) signatures+docs=%.0f MB db=%.0f MB (%.1fx source)\n",
		mb(uint64(src)), mb(uint64(raw)), float64(raw)/float64(src), mb(uint64(sig)), mb(uint64(dbfi.Size())), float64(dbfi.Size())/float64(src))
	symbols = nil
	st.Close()

	// Rehydration: resident heap after Open (what an MCP session holds).
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	t0 := time.Now()
	eng, err := grove.Open(ctx, grove.Config{RepoRoot: root})
	if err != nil {
		panic(err)
	}
	if _, err := eng.Symbols(ctx, "Json", 1); err != nil { // forces the graph load
		panic(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	fmt.Printf("open+first query: %.1fs resident heap=%.0f MB (delta %.0f MB)\n", time.Since(t0).Seconds(), mb(after.HeapInuse), mb(after.HeapInuse-before.HeapInuse))
	eng.Close()
}
