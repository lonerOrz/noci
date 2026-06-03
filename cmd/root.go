package cmd

import (
	"context"
	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "noci",
	Short: "noci is a highly modular Nix binary cache over OCI registry",
}

func Execute() error {
	return RootCmd.Execute()
}

func ExecuteContext(ctx context.Context) error {
	return RootCmd.ExecuteContext(ctx)
}

func init() {
	RootCmd.AddCommand(pushCmd)
	RootCmd.AddCommand(proxyCmd)
	RootCmd.AddCommand(searchCmd)
}
