package cmd

import (
	"context"
	"noci/pkg/log"

	"github.com/spf13/cobra"
)

var jsonOutput bool

var RootCmd = &cobra.Command{
	Use:   "noci",
	Short: "noci is a highly modular Nix binary cache over OCI registry",
}

func ExecuteContext(ctx context.Context) error {
	if jsonOutput {
		log.SetMode(log.ModeJSON)
	}
	return RootCmd.ExecuteContext(ctx)
}

func init() {
	RootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output logs as structured JSON lines")
	RootCmd.AddCommand(pushCmd)
	RootCmd.AddCommand(proxyCmd)
	RootCmd.AddCommand(searchCmd)
}
