package native

import (
	"os"
	"path/filepath"
	"strings"
)

func glob(root, pattern string) []string {
	matches, err := filepath.Glob(filepath.Join(root, pattern))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if rel, ok := relFile(root, match); ok {
			out = append(out, rel)
		}
	}
	return out
}

func filesWithExt(root, ext string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".grove", "node_modules", "vendor", "dist", "bin", "target":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ext) {
			if rel, ok := relFile(root, path); ok {
				out = append(out, rel)
			}
		}
		return nil
	})
	return out
}
