package main

import (
	"os"

	"github.com/ruaandeysel/vault/internal/cli"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	cli.SetVersion(version)
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
