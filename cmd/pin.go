package cmd

import (
	"fmt"
	"noci/pkg/app"
	"noci/pkg/config"
	"noci/pkg/log"
	"noci/pkg/oci"

	"github.com/spf13/cobra"
)

var (
	pinFlags CommonFlags
	pinTTL   string
)

var pinCmd = &cobra.Command{
	Use:   "pin [paths, targets, or 32-char hashes...]",
	Short: "Pin specific packages/targets in the OCI cache to protect them from GC",
	Long: `Mark one or more packages as pinned roots in the index. Pinned packages
are excluded from garbage collection until their TTL expires.

Accepts store paths, flake targets, or 32-char Nix hashes. Store paths and
targets are resolved to hashes before pinning.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runPin,
}

func init() {
	pinFlags.Register(pinCmd)
	pinCmd.Flags().StringVar(&pinTTL, "ttl", "30d", "Time to keep the package pinned (e.g., '30d', '24h', '0' for permanent)")
}

func runPin(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	cfg, err := pinFlags.Resolve()
	if err != nil {
		return err
	}

	ttlSeconds, err := config.ParseTTL(pinTTL)
	if err != nil {
		return fmt.Errorf("invalid --ttl value: %w", err)
	}

	hashes, err := app.NewTargetResolver(nil).ResolveHashes(ctx, args, true)
	if err != nil {
		return err
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("fetch index: %w", err)
	}

	for _, hash := range hashes {
		if _, exists := index.Entries[hash.String()]; !exists {
			log.Warning("Hash %s is not currently in the OCI cache entries. Pinned as root anyway.", hash)
		}
		index.PinRoot(hash, ttlSeconds)
		log.Success("Successfully pinned root: %s with TTL: %s", hash, pinTTL)
	}

	log.Action("Saving updated index back to OCI...")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("push index: %w", err)
	}

	return nil
}
