package cmd

import (
	"fmt"
	"noci/pkg/app"
	"noci/pkg/config"
	"noci/pkg/gc"
	"noci/pkg/log"
	"noci/pkg/oci"
	"time"

	"github.com/spf13/cobra"
)

var (
	gcFlags         CommonFlags
	gcDryRun        bool
	gcMaxSize       string
	gcGracePeriod   string
	gcPhysicalSweep bool
	gcKeepVersions  int
)

var gcCmd = &cobra.Command{
	Use:   "gc [paths, targets, or 32-char hashes...]",
	Short: "Garbage collect orphaned, quota-exceeded, or targeted packages",
	Long: `Remove cached packages from the OCI registry. Without arguments, performs
quota-based eviction using --max-size and --grace-period. With arguments,
explicitly targets specific hashes for removal.

Use --physical-sweep to physically delete evicted manifest tags from the
registry (required on GHCR which uses tag-overwriting). --keep-versions
retains N recent versions per package name.`,
	Args: cobra.ArbitraryArgs,
	RunE: runGC,
}

func init() {
	gcFlags.Register(gcCmd)
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", true, "Perform dry-run without writing back to OCI")
	gcCmd.Flags().StringVar(&gcMaxSize, "max-size", "", "Storage budget cap (e.g., '10GB', '500MB')")
	gcCmd.Flags().StringVar(&gcGracePeriod, "grace-period", "6h", "Safety grace period for newly uploaded files")
	gcCmd.Flags().BoolVar(&gcPhysicalSweep, "physical-sweep", false, "Physically prune evicted OCI manifests (supports tag-overwriting on GHCR)")
	gcCmd.Flags().IntVar(&gcKeepVersions, "keep-versions", 3, "Keep at most N recent versions per package name (0 = disabled)")
}

func runGC(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	cfg, err := gcFlags.Resolve()
	if err != nil {
		return err
	}

	maxBytes, err := config.ParseSize(gcMaxSize)
	if err != nil {
		return fmt.Errorf("parse max-size: %w", err)
	}

	dur, err := time.ParseDuration(gcGracePeriod)
	if err != nil {
		return fmt.Errorf("parse grace-period: %w", err)
	}

	hashes, err := app.NewTargetResolver(nil).ResolveHashes(ctx, args, false)
	if err != nil {
		return err
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	runner := gc.NewRunner(client, dur, gcKeepVersions, gcPhysicalSweep, gcDryRun)

	result, err := runner.Run(ctx, maxBytes, hashes)
	if err != nil {
		return err
	}

	log.Info("GC Summary:")
	log.Info("  Live:    %d (%s)", result.OriginalCount, oci.FormatSize(result.OriginalSize))
	log.Info("  Keep:    %d (%s)", result.RetainedCount, oci.FormatSize(result.RetainedSize))
	log.Info("  Evict:   %d (%s)", result.EvictedCount, oci.FormatSize(result.EvictedSize))

	if result.EvictedCount == 0 {
		log.Success("No packages to clean.")
		return nil
	}

	if gcDryRun {
		log.Warning("DRY RUN: Evicting hashes:")
		for _, key := range result.EvictedKeys {
			log.Info("  - %s", key)
		}
		return nil
	}

	log.Success("GC completed.")
	return nil
}
