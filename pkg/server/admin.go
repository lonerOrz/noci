package server

import (
	"context"
	"fmt"
	"noci/pkg/log"
	"noci/pkg/oci"
	"sort"
	"strings"
	"time"
)

// AdminService handles administrative operations: public key, package deletion, index query.
type AdminService struct {
	server *Server
}

func newAdminService(s *Server) *AdminService {
	return &AdminService{server: s}
}

// FetchPublicKey retrieves the signing public key from the OCI registry.
func (as *AdminService) FetchPublicKey(ctx context.Context) (string, error) {
	manifest, err := as.server.client.FetchManifest(ctx, "public-key")
	if err != nil {
		return "", err
	}
	if manifest != nil && manifest.Annotations != nil {
		if pubKey := manifest.Annotations["org.nix.public_key"]; pubKey != "" {
			return pubKey, nil
		}
	}
	return "", fmt.Errorf("public key annotation not found")
}

// DeletePackage removes a package from the cache index and schedules background OCI cleanup.
func (as *AdminService) DeletePackage(ctx context.Context, hash string) error {
	log.Action("[noci-proxy] Received web request to delete package hash: %s", hash)

	index, err := as.server.client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch index from OCI: %w", err)
	}

	if _, exists := index.Entries[hash]; !exists {
		return fmt.Errorf("package not found in cache")
	}

	delete(index.Entries, hash)
	if index.Roots != nil {
		delete(index.Roots, hash)
	}

	log.Action("[noci-proxy] Saving updated index back to OCI...")
	if err := as.server.client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to update OCI index (verify write permissions): %w", err)
	}

	newDigest := fmt.Sprintf("%s-dirty-%d", as.server.lastDigest, time.Now().UnixNano())
	as.server.indexMu.Lock()
	as.server.index = index
	as.server.lastDigest = newDigest
	as.server.indexMu.Unlock()

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		log.Action("[noci-proxy][bg] Deleting physical manifest from OCI: %s", hash)
		if err := as.server.client.DeleteManifest(bgCtx, hash); err != nil {
			log.Warning("[noci-proxy][bg] Optional: Failed to physically delete OCI manifest %s: %v", hash, err)
		}

		log.Action("[noci-proxy][bg] Finalizing local index refresh...")
		if err := as.server.RefreshIndex(bgCtx); err != nil {
			log.Warning("[noci-proxy][bg] Failed to refresh local proxy memory cache: %v", err)
		}
	}()

	return nil
}

// PaginatedResponse is the JSON response for paginated index queries.
type PaginatedResponse struct {
	Total       int              `json:"total"`
	Page        int              `json:"page"`
	Limit       int              `json:"limit"`
	CanDelete   bool             `json:"canDelete"`
	Repo        string           `json:"repo"`
	Registry    string           `json:"registry"`
	GlobalCount int64            `json:"globalCount"`
	GlobalSize  int64            `json:"globalSize"`
	Entries     []PaginatedEntry `json:"entries"`
}

// PaginatedEntry is a single entry in the paginated index response.
type PaginatedEntry struct {
	Hash string `json:"hash"`
	oci.IndexItem
}

// GetPaginatedIndex returns a filtered, sorted, paginated view of the cache index.
func (as *AdminService) GetPaginatedIndex(page, limit int, search string) (*PaginatedResponse, error) {
	as.server.indexMu.RLock()
	defer as.server.indexMu.RUnlock()

	if as.server.index == nil {
		return nil, fmt.Errorf("index not ready")
	}

	var globalSize int64
	for _, entry := range as.server.index.Entries {
		globalSize += entry.NarSize
	}

	search = strings.ToLower(strings.TrimSpace(search))
	var filtered []PaginatedEntry
	for hash, entry := range as.server.index.Entries {
		if search != "" {
			nameMatch := strings.Contains(strings.ToLower(entry.Name), search)
			hashMatch := strings.Contains(strings.ToLower(hash), search)
			if !nameMatch && !hashMatch {
				continue
			}
		}
		filtered = append(filtered, PaginatedEntry{
			Hash:      hash,
			IndexItem: entry,
		})
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Added.After(filtered[j].Added)
	})

	total := len(filtered)
	startIndex := (page - 1) * limit
	endIndex := startIndex + limit
	if startIndex > total {
		startIndex = total
	}
	if endIndex > total {
		endIndex = total
	}

	return &PaginatedResponse{
		Total:       total,
		Page:        page,
		Limit:       limit,
		CanDelete:   as.server.canDelete,
		Repo:        as.server.index.Repo,
		Registry:    as.server.index.Registry,
		GlobalCount: int64(len(as.server.index.Entries)),
		GlobalSize:  globalSize,
		Entries:     filtered[startIndex:endIndex],
	}, nil
}
