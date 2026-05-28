package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const DefaultPort = 7777

type Config struct {
	Root      string
	StorePath string
	Port      int
}

func Resolve(root string, port int) (Config, error) {
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
	if port == 0 {
		port = DefaultPort
	}
	return Config{
		Root:      absRoot,
		StorePath: filepath.Join(absRoot, ".grove", "grove.db"),
		Port:      port,
	}, nil
}
