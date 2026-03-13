package main

import (
	"os"

	"github.com/sthadka/jai/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
