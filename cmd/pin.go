package cmd

import (
	"context"
	"fmt"
	"noci/pkg/nix"
	"noci/pkg/oci"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	pinRepo     string
	pinRegistry string
	pinTTL      string
)

var pinCmd = &cobra.Command{
	Use:   "pin [paths or targets...]",
	Short: "Pin specific packages/targets in the OCI cache to protect them from GC",
	RunE:  runPin,
}

func init() {
	pinCmd.Flags().StringVar(&pinRepo, "repo", "", "OCI repository (e.g. username/repo)")
	pinCmd.Flags().StringVar(&pinRegistry, "registry", "ghcr.io", "OCI registry endpoint")
	pinCmd.Flags().StringVar(&pinTTL, "ttl", "30d", "Time to keep the package pinned (e.g., '30d', '24h', '0' for permanent)")

	RootCmd.AddCommand(pinCmd)
}

func runPin(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no paths or targets specified to pin")
	}

	ctx := context.Background()
	cfg, err := resolveOCIConfig(pinRegistry, pinRepo)
	if err != nil {
		return err
	}

	// 兼容类似 "30d"、"24h" 以及 "0" 永久的弹性转换
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

	// 解析构建目标路径
	var inputPaths []string
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "/nix/store") {
			fmt.Printf(">>> Target %q is not a store path. Evaluating output via `nix build`...\n", arg)
			buildPaths, err := nix.BuildTarget(arg)
			if err != nil {
				return fmt.Errorf("failed to evaluate target %q: %w", arg, err)
			}
			inputPaths = append(inputPaths, buildPaths...)
		} else {
			inputPaths = append(inputPaths, arg)
		}
	}

	if len(inputPaths) == 0 {
		return fmt.Errorf("no valid store paths resolved to pin")
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch index: %w", err)
	}

	// 进行 Pin 分配
	for _, path := range inputPaths {
		hash := nix.GetPathHash(path)
		if _, exists := index.Entries[hash]; !exists {
			fmt.Printf("[Warning] Store path %s (hash: %s) is not currently in the OCI cache. Pinned as root anyway.\n", path, hash)
		}
		index.PinRoot(hash, ttlSeconds)
		fmt.Printf(">>> Successfully pinned root: %s (hash: %s) with TTL: %s\n", path, hash, pinTTL)
	}

	fmt.Println(">>> Saving updated index back to OCI...")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push index: %w", err)
	}

	return nil
}
