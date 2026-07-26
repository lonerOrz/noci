package cmd

import (
	"context"
	"fmt"
	"noci/pkg/log"
	"noci/pkg/oci"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

var (
	cleanFlags     CommonFlags
	cleanDryRun    bool
	cleanDeleteOrph bool
)

var indexCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove corrupted or orphaned entries from the index",
	Long: `Scan every entry in the index, fetch its manifest, and remove entries
where the manifest is missing, has no layers, or has no annotations.
Optionally delete the corrupted manifest tags from the registry.`,
	Args: cobra.NoArgs,
	RunE: runIndexClean,
}

func init() {
	cleanFlags.Register(indexCleanCmd)
	indexCleanCmd.Flags().BoolVar(&cleanDryRun, "dry-run", true, "Show what would be cleaned without writing")
	indexCleanCmd.Flags().BoolVar(&cleanDeleteOrph, "delete", false, "Also delete corrupted manifest tags from the registry")
}

type cleanResult struct {
	hash   string
	reason string
	err    error
}

func runIndexClean(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	cfg, err := cleanFlags.Resolve()
	if err != nil {
		return err
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)

	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("fetch index: %w", err)
	}

	log.Action("Scanning %d index entries for corrupted manifests...", len(index.Entries))

	hashes := make([]string, 0, len(index.Entries))
	for h := range index.Entries {
		hashes = append(hashes, h)
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var mu sync.Mutex
	var corrupted []cleanResult

	errs := runConcurrent(checkCtx, hashes, 8, func(ctx context.Context, h string) error {
		manifest, err := client.FetchManifest(ctx, h)
		if err != nil {
			mu.Lock()
			corrupted = append(corrupted, cleanResult{hash: h, reason: fmt.Sprintf("fetch failed: %v", err)})
			mu.Unlock()
			return nil // not a fatal error
		}

		if len(manifest.Layers) == 0 {
			mu.Lock()
			corrupted = append(corrupted, cleanResult{hash: h, reason: "no layers"})
			mu.Unlock()
			return nil
		}

		if manifest.Annotations == nil || manifest.Annotations["org.nix.name"] == "" {
			mu.Lock()
			corrupted = append(corrupted, cleanResult{hash: h, reason: "no nix annotations"})
			mu.Unlock()
			return nil
		}

		return nil
	})

	_ = errs // all errors are handled inline via reason tracking

	if len(corrupted) == 0 {
		log.Success("Index is clean. No corrupted entries found.")
		return nil
	}

	log.Warning("Found %d corrupted entries:", len(corrupted))
	for _, c := range corrupted {
		log.Info("  %s — %s", c.hash, c.reason)
	}

	if cleanDryRun {
		log.Warning("DRY RUN: No changes made. Run with --dry-run=false to apply.")
		return nil
	}

	// Remove corrupted entries from index.
	for _, c := range corrupted {
		delete(index.Entries, c.hash)
	}
	index.Generated = time.Now()

	// Push cleaned index.
	log.Action("Pushing cleaned index (%d entries)...", len(index.Entries))
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("push index: %w", err)
	}

	// Optionally delete corrupted manifest tags from registry.
	if cleanDeleteOrph {
		log.Action("Deleting %d corrupted manifest tags...", len(corrupted))
		var delFailed int
		for _, c := range corrupted {
			if err := client.DeleteManifest(ctx, c.hash); err != nil {
				log.Warning("Failed to delete %s: %v", c.hash, err)
				delFailed++
			}
		}
		if delFailed > 0 {
			log.Warning("Deleted %d tags, %d failed.", len(corrupted)-delFailed, delFailed)
		} else {
			log.Success("Deleted %d corrupted manifest tags.", len(corrupted))
		}
	}

	log.Success("Cleaned %d corrupted entries from index.", len(corrupted))
	return nil
}
