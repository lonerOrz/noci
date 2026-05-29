package cmd

import (
	"fmt"
	"noci/pkg/log"
	"noci/pkg/nix"
	"noci/pkg/oci"
	"strings"

	"github.com/spf13/cobra"
)

var unpinFlags CommonFlags

var unpinCmd = &cobra.Command{
	Use:   "unpin [paths or targets...]",
	Short: "Unpin specific packages/targets in the OCI cache to allow them to be garbage collected",
	RunE:  runUnpin,
}

func init() {
	unpinFlags.Register(unpinCmd)
	RootCmd.AddCommand(unpinCmd)
}

func runUnpin(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no paths or targets specified to unpin")
	}

	ctx := cmd.Context()
	cfg, err := unpinFlags.Resolve()
	if err != nil {
		return err
	}

	var inputPaths []string
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "/nix/store") {
			log.Action("Target %q is not a store path. Evaluating output via `nix build`...", arg)
			buildPaths, err := nix.BuildTarget(ctx, arg)
			if err != nil {
				return fmt.Errorf("failed to evaluate target %q: %w", arg, err)
			}
			inputPaths = append(inputPaths, buildPaths...)
		} else {
			inputPaths = append(inputPaths, arg)
		}
	}

	if len(inputPaths) == 0 {
		return fmt.Errorf("no valid store paths resolved to unpin")
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch index: %w", err)
	}

	modified := false
	for _, path := range inputPaths {
		hash := nix.GetPathHash(path)
		if index.Roots != nil {
			if _, exists := index.Roots[hash]; exists {
				delete(index.Roots, hash)
				log.Success("Successfully unpinned root: %s (hash: %s)", path, hash)
				modified = true
			} else {
				log.Warning("Store path %s (hash: %s) was not pinned.", path, hash)
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
