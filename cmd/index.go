package cmd

import "github.com/spf13/cobra"

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "OCI index management",
	Long: `Manage the OCI cache index. Subcommands for repairing and cleaning
the index are available under 'noci index repair' and 'noci index clean'.`,
}

func init() {
	indexCmd.AddCommand(repairCmd)
	indexCmd.AddCommand(indexCleanCmd)
}
