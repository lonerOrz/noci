package cmd

import (
	"fmt"
	"noci/pkg/log"
	"noci/pkg/oci"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	pinFlags    CommonFlags
	pinTTL      string
)

var pinCmd = &cobra.Command{
	Use:   "pin [paths, targets, or 32-char hashes...]",
	Short: "Pin specific packages/targets in the OCI cache to protect them from GC",
	RunE:  runPin,
}

func init() {
	pinFlags.Register(pinCmd)
	pinCmd.Flags().StringVar(&pinTTL, "ttl", "30d", "Time to keep the package pinned (e.g., '30d', '24h', '0' for permanent)")

	RootCmd.AddCommand(pinCmd)
}

func runPin(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no paths, targets, or hashes specified to pin")
	}

	ctx := cmd.Context()

	cfg, err := pinFlags.Resolve()
	if err != nil {
		return err
	}

	var ttlSeconds int64
	if pinTTL != "0" {
		cleanedTTL := strings.ToLower(strings.TrimSpace(pinTTL))
		if strings.HasSuffix(cleanedTTL, "d") {
			daysStr := strings.TrimSuffix(cleanedTTL, "d")
			days, err := strconv.ParseInt(daysStr, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid day format for TTL: %s", pinTTL)
			}
			ttlSeconds = days * 24 * 3600
		} else {
			dur, err := time.ParseDuration(pinTTL)
			if err != nil {
				return fmt.Errorf("failed to parse TTL duration: %w", err)
			}
			ttlSeconds = int64(dur.Seconds())
		}
	}

	// 统一利用 resolveHashes 解析（策略上允许降级调用本地 `nix build` 获取新构建的 hash）
	inputHashes, err := resolveHashes(ctx, args, true)
	if err != nil {
		return err
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch index: %w", err)
	}

	for _, hash := range inputHashes {
		if _, exists := index.Entries[hash]; !exists {
			log.Warning("Hash %s is not currently in the OCI cache entries. Pinned as root anyway.", hash)
		}
		index.PinRoot(hash, ttlSeconds)
		log.Success("Successfully pinned root: %s with TTL: %s", hash, pinTTL)
	}

	log.Action("Saving updated index back to OCI...")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push index: %w", err)
	}

	return nil
}
