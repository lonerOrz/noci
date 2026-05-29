package cmd

import (
	"context"
	"fmt"
	"noci/pkg/gc"
	"noci/pkg/log"
	"noci/pkg/oci"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	gcFlags         CommonFlags
	gcDryRun        bool
	gcMaxSize       string
	gcGracePeriod   string
	gcPhysicalSweep bool
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Garbage collect orphaned or quota-exceeded packages",
	RunE:  runGC,
}

func init() {
	gcFlags.Register(gcCmd)
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", true, "Perform dry-run without writing back to OCI")
	gcCmd.Flags().StringVar(&gcMaxSize, "max-size", "", "Storage budget cap (e.g., '10GB', '500MB')")
	gcCmd.Flags().StringVar(&gcGracePeriod, "grace-period", "6h", "Safety grace period for newly uploaded files")
	gcCmd.Flags().BoolVar(&gcPhysicalSweep, "physical-sweep", false, "Physically delete OCI blobs (requires token write/delete permissions)")

	RootCmd.AddCommand(gcCmd)
}

func runGC(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	cfg, err := gcFlags.Resolve()
	if err != nil {
		return err
	}

	maxBytes, err := parseSizeString(gcMaxSize)
	if err != nil {
		return fmt.Errorf("failed to parse max-size: %w", err)
	}

	dur, err := time.ParseDuration(gcGracePeriod)
	if err != nil {
		return fmt.Errorf("failed to parse grace-period: %w", err)
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch index: %w", err)
	}

	engine := gc.NewEngine(index, dur)
	result := engine.Sweep(time.Now(), maxBytes)

	log.Info("Garbage Collection Summary:")
	fmt.Printf("    Original package count: %d (%d bytes)\n", result.OriginalCount, result.OriginalSize)
	fmt.Printf("    Retained package count: %d (%d bytes)\n", result.RetainedCount, result.RetainedSize)
	fmt.Printf("    Evicted package count:  %d (%d bytes)\n", result.EvictedCount, result.EvictedSize)

	if result.EvictedCount == 0 {
		log.Success("No packages require cleanup.")
		return nil
	}

	if gcDryRun {
		log.Warning("DRY RUN: The following package hashes would be evicted:")
		for _, key := range result.EvictedKeys {
			fmt.Printf("    - %s (%s)\n", key, index.Entries[key].Name)
		}
		return nil
	}

	if gcPhysicalSweep {
		log.Action("Performing physical sweep of OCI Blobs...")
		for _, key := range result.EvictedKeys {
			entry := index.Entries[key]
			log.Action("Deleting blob for %s (%s)...", key, entry.Name)
			if err := client.DeleteBlob(ctx, entry.NarDigest); err != nil {
				errMsg := err.Error()
				// 如果捕获到 405 / UNSUPPORTED，说明当前 OCI 平台（如 GHCR）不支持物理擦除
				if strings.Contains(errMsg, "405") || strings.Contains(errMsg, "UNSUPPORTED") {
					log.Warning("The OCI registry does not support physical blob deletion (HTTP 405). " +
						"Skipping remaining physical sweeps; relying on registry-side automatic GC (soft-delete is active).")
					break
				}
				log.Warning("Failed to delete blob %s: %v", entry.NarDigest, err)
			}
		}
	}

	engine.Apply(result)

	log.Action("Pushing updated index back to OCI...")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}

	log.Success("Garbage collection completed successfully.")
	return nil
}
