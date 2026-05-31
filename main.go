package main

import (
	"fmt"
	"noci/cmd"
	"os"
)

func main() {
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "proxy")
	}

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
