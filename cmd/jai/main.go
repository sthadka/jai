package main

import (
	"os"

	"github.com/syethadk/jai/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
