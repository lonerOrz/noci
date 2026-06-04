package cmd

import (
	"fmt"
	"noci/pkg/gc"
	"noci/pkg/log"
	"noci/pkg/oci"
	"strings"
	"sync"
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
	Use:   "gc [paths, targets, or 32-char hashes...]",
	Short: "Garbage collect orphaned, quota-exceeded, or targeted packages",
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
	ctx := cmd.Context()

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
	var result *gc.Result

	if len(args) > 0 {
		inputHashes, err := resolveHashes(ctx, args, false)
		if err != nil {
			return err
		}
		log.Action("Targeted eviction resolved to %d input hashes.", len(inputHashes))
		result = engine.CascadeEvict(inputHashes)
	} else {
		result = engine.Sweep(time.Now(), maxBytes)
	}

	log.Info("GC Summary:")
	fmt.Printf("  Live:    %d (%d B)\n", result.OriginalCount, result.OriginalSize)
	fmt.Printf("  Keep:    %d (%d B)\n", result.RetainedCount, result.RetainedSize)
	fmt.Printf("  Evict:   %d (%d B)\n", result.EvictedCount, result.EvictedSize)

	if result.EvictedCount == 0 {
		log.Success("No packages to clean.")
		return nil
	}

	if gcDryRun {
		log.Warning("DRY RUN: Evicting hashes:")
		for _, key := range result.EvictedKeys {
			fmt.Printf("  - %s\n", key)
		}
		return nil
	}

	type blobSweepInfo struct {
		key    string
		digest string
	}
	var sweepList []blobSweepInfo
	for _, key := range result.EvictedKeys {
		if entry, exists := index.Entries[key]; exists {
			sweepList = append(sweepList, blobSweepInfo{
				key:    key,
				digest: entry.NarDigest,
			})
		}
	}

	engine.Apply(result)

	log.Action("Updating OCI state...")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}

	if gcPhysicalSweep && len(sweepList) > 0 {
		log.Action("Sweeping blobs concurrently (8 workers)...")
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		var warnOnce sync.Once

		for _, sweep := range sweepList {
			sem <- struct{}{}
			wg.Add(1)

			go func(digest, key string) {
				defer func() {
					<-sem
					wg.Done()
				}()
				log.Action("Deleting blob: %s", key)
				if err := client.DeleteBlob(ctx, "sha256:"+digest); err != nil {
					errMsg := err.Error()
					if strings.Contains(errMsg, "405") || strings.Contains(errMsg, "UNSUPPORTED") {
						warnOnce.Do(func() {
							log.Warning("Blob deletion unsupported (405). Skipping remaining.")
						})
						return
					}
					log.Warning("Failed to delete blob sha256:%s: %v", digest, err)
				}
			}(sweep.digest, sweep.key)
		}
		wg.Wait()
	}

	log.Success("GC completed.")
	return nil
}
