package main

import (
	"os"

	"github.com/sthadka/jai/v5/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
