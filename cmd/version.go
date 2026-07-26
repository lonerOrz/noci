package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Set via ldflags at build time: go build -ldflags "-X noci/cmd.version=v1.0.0"
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("noci", version)
	},
}

func init() {
	RootCmd.AddCommand(versionCmd)
}
