package cmd

import (
	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "noci",
	Short: "noci is a highly modular Nix binary cache over OCI registry",
}

func Execute() error {
	return RootCmd.Execute()
}

func init() {
	RootCmd.AddCommand(pushCmd)
	RootCmd.AddCommand(proxyCmd)
}
