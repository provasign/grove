package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Root      string
	StorePath string
}

func Resolve(root string) (Config, error) {
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Config{}, err
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return Config{}, err
	}
	if !info.IsDir() {
		return Config{}, fmt.Errorf("root is not a directory: %s", absRoot)
	}
	return Config{
		Root:      absRoot,
		StorePath: filepath.Join(absRoot, ".grove", "grove.db"),
	}, nil
}
