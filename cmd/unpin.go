package cmd

import (
	"fmt"
	"noci/pkg/config"
	"noci/pkg/log"
	"noci/pkg/oci"

	"github.com/spf13/cobra"
)

var unpinFlags CommonFlags

var unpinCmd = &cobra.Command{
	Use:   "unpin [paths or 32-char hashes...]",
	Short: "Unpin specific packages in the OCI cache to allow them to be garbage collected",
	Long: `Remove pin protection from one or more packages, making them eligible for
garbage collection. Accepts store paths or 32-char Nix hashes.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runUnpin,
}

func init() {
	unpinFlags.Register(unpinCmd)
}

func runUnpin(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cfg, err := unpinFlags.Resolve()
	if err != nil {
		return err
	}

	// Resolve inputs: no nix build fallback.
	inputHashes, err := config.ResolveHashes(ctx, args, false)
	if err != nil {
		return err
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("fetch index: %w", err)
	}

	modified := false
	for _, hash := range inputHashes {
		if index.Roots != nil {
			if _, exists := index.Roots[hash]; exists {
				delete(index.Roots, hash)
				log.Success("Successfully unpinned root hash: %s", hash)
				modified = true
			} else {
				log.Warning("Hash %s was not pinned.", hash)
			}
		}
	}

	if !modified {
		log.Info("No modifications made to OCI index.")
		return nil
	}

	log.Action("Saving updated index back to OCI...")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("push index: %w", err)
	}

	return nil
}
