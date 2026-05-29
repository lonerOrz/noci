package cmd

import (
	"context"
	"fmt"
	"noci/pkg/gc"
	"noci/pkg/oci"
	"time"

	"github.com/spf13/cobra"
)

var (
	gcRepo         string
	gcRegistry     string
	gcDryRun       bool
	gcMaxSize      string
	gcGracePeriod  string
	gcPhysicalSweep bool
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Garbage collect orphaned or quota-exceeded packages",
	RunE:  runGC,
}

func init() {
	gcCmd.Flags().StringVar(&gcRepo, "repo", "", "OCI repository (e.g. username/repo)")
	gcCmd.Flags().StringVar(&gcRegistry, "registry", "ghcr.io", "OCI registry endpoint")
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", true, "Perform dry-run without writing back to OCI")
	gcCmd.Flags().StringVar(&gcMaxSize, "max-size", "", "Storage budget cap (e.g., '10GB', '500MB')")
	gcCmd.Flags().StringVar(&gcGracePeriod, "grace-period", "6h", "Safety grace period for newly uploaded files")
	gcCmd.Flags().BoolVar(&gcPhysicalSweep, "physical-sweep", false, "Physically delete OCI blobs (requires token write/delete permissions)")

	RootCmd.AddCommand(gcCmd)
}

func runGC(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	cfg, err := resolveOCIConfig(gcRegistry, gcRepo)
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

	fmt.Printf(">>> Garbage Collection Summary:\n")
	fmt.Printf("    Original package count: %d (%d bytes)\n", result.OriginalCount, result.OriginalSize)
	fmt.Printf("    Retained package count: %d (%d bytes)\n", result.RetainedCount, result.RetainedSize)
	fmt.Printf("    Evicted package count:  %d (%d bytes)\n", result.EvictedCount, result.EvictedSize)

	if result.EvictedCount == 0 {
		fmt.Println(">>> No packages require cleanup.")
		return nil
	}

	if gcDryRun {
		fmt.Println(">>> DRY RUN: The following package hashes would be evicted:")
		for _, key := range result.EvictedKeys {
			fmt.Printf("    - %s (%s)\n", key, index.Entries[key].Name)
		}
		return nil
	}

	// 如果指定了硬物理清除，在删除索引记录前先彻底销毁 OCI Blob
	if gcPhysicalSweep {
		fmt.Println(">>> Performing physical sweep of OCI Blobs...")
		for _, key := range result.EvictedKeys {
			entry := index.Entries[key]
			fmt.Printf("    Deleting blob for %s (%s)...\n", key, entry.Name)
			if err := client.DeleteBlob(ctx, entry.NarDigest); err != nil {
				fmt.Printf("    [Warning] Failed to delete blob %s: %v\n", entry.NarDigest, err)
			}
		}
	}

	// 应用物理内存回收
	engine.Apply(result)

	fmt.Println(">>> Pushing updated index back to OCI...")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}

	fmt.Println(">>> Garbage collection completed successfully.")
	return nil
}
