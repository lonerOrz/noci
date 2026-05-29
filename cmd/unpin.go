package cmd

import (
	"fmt"
	"noci/pkg/log"
	"noci/pkg/oci"

	"github.com/spf13/cobra"
)

var unpinFlags CommonFlags

var unpinCmd = &cobra.Command{
	Use:   "unpin [paths or 32-char hashes...]",
	Short: "Unpin specific packages in the OCI cache to allow them to be garbage collected",
	RunE:  runUnpin,
}

func init() {
	unpinFlags.Register(unpinCmd)
	RootCmd.AddCommand(unpinCmd)
}

func runUnpin(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no paths or hashes specified to unpin")
	}

	ctx := cmd.Context()
	cfg, err := unpinFlags.Resolve()
	if err != nil {
		return err
	}

	// 统一利用 resolveHashes 解析（策略上强制禁止 unpin 触发本地构建，提升效率）
	inputHashes, err := resolveHashes(ctx, args, false)
	if err != nil {
		return err
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch index: %w", err)
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
		return fmt.Errorf("failed to push index: %w", err)
	}

	return nil
}
