package main

import (
	"os"

	"github.com/tabladrum/grove-suite/grove/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
