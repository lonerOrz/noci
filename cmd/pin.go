package cmd

import (
	"fmt"
	"noci/pkg/log"
	"noci/pkg/nix"
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
	Use:   "pin [paths or targets...]",
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
		return fmt.Errorf("no paths or targets specified to pin")
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
		return fmt.Errorf("no valid store paths resolved to pin")
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch index: %w", err)
	}

	for _, path := range inputPaths {
		hash := nix.GetPathHash(path)
		if _, exists := index.Entries[hash]; !exists {
			log.Warning("Store path %s (hash: %s) is not currently in the OCI cache. Pinned as root anyway.", path, hash)
		}
		index.PinRoot(hash, ttlSeconds)
		log.Success("Successfully pinned root: %s (hash: %s) with TTL: %s", path, hash, pinTTL)
	}

	log.Action("Saving updated index back to OCI...")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push index: %w", err)
	}

	return nil
}
