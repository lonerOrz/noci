package cmd

import (
	"context"
	"fmt"
	"noci/pkg/log"
	"os"

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
	RootCmd.AddCommand(&cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion scripts",
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			}
			return fmt.Errorf("unsupported shell: %q", args[0])
		},
	})
}
