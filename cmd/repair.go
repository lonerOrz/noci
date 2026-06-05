package cmd

import (
	"context"
	"fmt"
	"noci/pkg/log"
	"noci/pkg/oci"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

var (
	repairFlags CommonFlags
	repairDryRun bool
)

var repairCmd = &cobra.Command{
	Use:   "repair",
	Short: "Reconcile OCI manifests into the index",
	Long: `Scan all OCI manifest tags and add any missing entries to the index.
This repairs the index when it has fallen out of sync with the registry
(e.g., after a failed index push or an incomplete push).`,
	RunE: runRepair,
}

func init() {
	repairFlags.Register(repairCmd)
	repairCmd.Flags().BoolVar(&repairDryRun, "dry-run", true, "Show missing entries without writing")
}

func runRepair(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	cfg, err := repairFlags.Resolve()
	if err != nil {
		return err
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)

	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("fetch index: %w", err)
	}

	log.Action("Listing OCI tags...")
	tags, err := client.ListTags(ctx)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	log.Info("Found %d total tags.", len(tags))

	var candidateHashes []string
	for _, tag := range tags {
		lower := strings.ToLower(tag)
		if nixHashRegex.MatchString(lower) {
			if _, exists := index.Entries[lower]; !exists {
				candidateHashes = append(candidateHashes, lower)
			}
		}
	}
	sort.Strings(candidateHashes)

	if len(candidateHashes) == 0 {
		log.Success("Index is up to date. No missing entries found.")
		return nil
	}

	log.Info("Found %d OCI manifests missing from index.", len(candidateHashes))

	if repairDryRun {
		log.Warning("DRY RUN: Would repair these entries:")
		for _, hash := range candidateHashes {
			fmt.Printf("  - %s\n", hash)
		}
		return nil
	}

	log.Action("Repairing %d entries (8 workers)...", len(candidateHashes))

	type repairResult struct {
		hash string
		err  error
	}
	results := make(chan repairResult, len(candidateHashes))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup

	repairCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	for _, hash := range candidateHashes {
		sem <- struct{}{}
		wg.Add(1)

		go func(h string) {
			defer func() {
				<-sem
				wg.Done()
			}()

			if err := client.RepairIndexEntry(repairCtx, h, index); err != nil {
				results <- repairResult{hash: h, err: err}
			} else {
				results <- repairResult{hash: h}
			}
		}(hash)
	}

	wg.Wait()
	close(results)

	var repaired, failed int
	for r := range results {
		if r.err != nil {
			log.Warning("Failed to repair %s: %v", r.hash, r.err)
			failed++
		} else {
			repaired++
		}
	}

	if repaired == 0 {
		return fmt.Errorf("all %d repair attempts failed", failed)
	}

	log.Action("Pushing updated index...")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("push index: %w", err)
	}

	if failed > 0 {
		log.Warning("Repaired %d entries, %d failed.", repaired, failed)
	} else {
		log.Success("Repaired %d entries.", repaired)
	}
	return nil
}
