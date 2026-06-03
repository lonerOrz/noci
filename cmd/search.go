package cmd

import (
	"fmt"
	"noci/pkg/log"
	"noci/pkg/oci"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var searchFlags CommonFlags

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search or list packages cached in the OCI registry",
	RunE:  runSearch,
}

func init() {
	searchFlags.Register(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cfg, err := searchFlags.Resolve()
	if err != nil {
		return err
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	index, err := client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch index: %w", err)
	}

	type match struct {
		hash string
		item oci.IndexItem
	}
	var matched []match

	query := ""
	if len(args) > 0 {
		query = strings.ToLower(strings.TrimSpace(args[0]))
	}

	if query == "" {
		for hash, entry := range index.Entries {
			matched = append(matched, match{hash: hash, item: entry})
		}
	} else {
                resolved, err := resolveHashes(ctx, args, false)
		if err == nil && len(resolved) > 0 {
			seen := make(map[string]bool)
			for _, rh := range resolved {
				if seen[rh] {
					continue
				}
				seen[rh] = true
				if entry, exists := index.Entries[rh]; exists {
					matched = append(matched, match{hash: rh, item: entry})
				}
			}
		} else {
			for hash, entry := range index.Entries {
				if strings.Contains(strings.ToLower(entry.Name), query) ||
					strings.Contains(strings.ToLower(hash), query) {
					matched = append(matched, match{hash: hash, item: entry})
				}
			}
		}
	}

	if len(matched) == 0 {
		if query != "" {
			log.Warning("No packages matched the query: %q", query)
		} else {
			log.Warning("The OCI cache is empty.")
		}
		return nil
	}

	sort.Slice(matched, func(i, j int) bool {
		return matched[i].item.Name < matched[j].item.Name
	})

	if query != "" {
		log.Success("Found %d matching packages:", len(matched))
	} else {
		log.Success("Found %d cached packages in registry:", len(matched))
	}

	for _, m := range matched {
		fmt.Printf("  - %-32s (%s) [Size: %s, Added: %s]\n",
			m.item.Name,
			m.hash,
			oci.FormatSize(m.item.NarSize),
			m.item.Added.Local().Format("2006-01-02 15:04:05"),
		)
	}

	return nil
}
