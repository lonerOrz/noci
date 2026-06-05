package cmd

import "github.com/spf13/cobra"

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "OCI index management",
}

func init() {
	indexCmd.AddCommand(repairCmd)
	RootCmd.AddCommand(indexCmd)
}
