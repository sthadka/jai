package main

import (
	"os"

	"github.com/sthadka/jai/v7/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
