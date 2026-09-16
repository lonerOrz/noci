package server

import (
	"encoding/json"
	"fmt"
	"noci/pkg/oci"
	"os"
	"path/filepath"
	"strings"
)

// DiskIndexCache manages disk persistence of the cache index.
type DiskIndexCache struct {
	filePath   string
	digestPath string
}

// NewDiskIndexCache resolves the appropriate cache directory according to:
// 1. explicit cacheDir
// 2. NOCI_CACHE_DIR env var
// 3. CACHE_DIRECTORY env var (systemd CacheDirectory= directive)
// 4. ~/.cache/noci/...
func NewDiskIndexCache(explicitDir, registry, repo string) *DiskIndexCache {
	dir := resolveCacheDir(explicitDir)
	if dir == "" {
		return nil
	}

	// Sanitize registry/repo for safe filesystem paths; strip leading slashes
	// and collapse any path separators so "../" or "\windows\" cannot escape dir.
	safeRepo := strings.ReplaceAll(strings.ReplaceAll(filepath.Clean(repo), "/", "--"), "\\", "--")
	safeRegistry := strings.ReplaceAll(strings.ReplaceAll(filepath.Clean(registry), "/", "--"), "\\", "--")

	// Join first, then resolve to absolute — this catches any ".." that survived
	// sanitization by comparing against the absolute cache root.
	repoDir := filepath.Join(dir, safeRegistry, safeRepo)
	if absDir, err := filepath.Abs(dir); err == nil {
		if absRepo, err := filepath.Abs(repoDir); err == nil {
			prefix := absDir + string(filepath.Separator)
			if absRepo != absDir && !strings.HasPrefix(absRepo, prefix) {
				return nil
			}
		}
	}

	return &DiskIndexCache{
		filePath:   filepath.Join(repoDir, "cache-index.json"),
		digestPath: filepath.Join(repoDir, "cache-index.digest"),
	}
}

func resolveCacheDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("NOCI_CACHE_DIR"); env != "" {
		return env
	}
	if systemdCache := os.Getenv("CACHE_DIRECTORY"); systemdCache != "" {
		return systemdCache
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "noci")
	}
	return ""
}

// Load reads the cached index from disk if present.
func (d *DiskIndexCache) Load() (*oci.CacheIndex, string, error) {
	if d == nil || d.filePath == "" {
		return nil, "", nil
	}

	data, err := os.ReadFile(d.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}

	var idx oci.CacheIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, "", fmt.Errorf("corrupted disk cache index: %w", err)
	}
	idx.Upgrade()

	digest := ""
	if dData, err := os.ReadFile(d.digestPath); err == nil {
		digest = strings.TrimSpace(string(dData))
	}

	return &idx, digest, nil
}

// Save atomically writes the index and its manifest digest to disk.
func (d *DiskIndexCache) Save(idx *oci.CacheIndex, digest string) error {
	if d == nil || d.filePath == "" || idx == nil {
		return nil
	}

	dir := filepath.Dir(d.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to temp file then rename
	tmpFile := d.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, d.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}

	if digest != "" {
		tmpDigest := d.digestPath + ".tmp"
		if err := os.WriteFile(tmpDigest, []byte(digest), 0644); err == nil {
			_ = os.Rename(tmpDigest, d.digestPath)
		}
	}

	return nil
}
