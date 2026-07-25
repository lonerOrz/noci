package server

import (
	"noci/pkg/oci"
	"strings"
	"time"
)

// CacheService handles narinfo lookups and NAR blob resolution against the local cache index.
type CacheService struct {
	server *Server
}

func newCacheService(s *Server) *CacheService {
	return &CacheService{server: s}
}

// GetNarInfo returns the rewritten narinfo content if the hash is cached locally.
// Returns empty string and false if not found.
func (cs *CacheService) GetNarInfo(hash string) (string, bool) {
	if len(hash) != 32 {
		return "", false
	}

	if val, exists := cs.server.negCache.Load(hash); exists {
		if time.Since(val.(time.Time)) <= 5*time.Second {
			return "", false
		}
		cs.server.negCache.Delete(hash)
	}

	cs.server.indexMu.RLock()
	var entry oci.IndexItem
	var found bool
	if cs.server.index != nil && cs.server.index.Entries != nil {
		entry, found = cs.server.index.Entries[hash]
	}
	cs.server.indexMu.RUnlock()

	if !found {
		cs.server.negCache.Store(hash, time.Now())
		return "", false
	}

	content := cs.rewriteNarInfoURL(&entry)
	if content == "" {
		return "", false
	}
	return content, true
}

func (cs *CacheService) rewriteNarInfoURL(entry *oci.IndexItem) string {
	narinfo := entry.NarInfo
	if narinfo == "" {
		return ""
	}
	digest := strings.TrimPrefix(entry.NarDigest, "sha256:")
	lines := strings.Split(narinfo, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "URL: ") {
			ext := ".nar.gz"
			if strings.HasSuffix(line, ".nar.zst") {
				ext = ".nar.zst"
			}
			lines[i] = "URL: nar/" + digest + ext
			break
		}
	}
	return strings.Join(lines, "\n")
}

// ResolveNarDigest resolves a nar filename to a "sha256:..." digest.
func (cs *CacheService) ResolveNarDigest(filename string) (string, bool) {
	digest := filename
	if idx := strings.Index(filename, "."); idx != -1 {
		digest = filename[:idx]
	}

	if len(digest) == 64 {
		return "sha256:" + digest, true
	}

	if len(digest) == 32 {
		cs.server.indexMu.RLock()
		var entry oci.IndexItem
		var found bool
		if cs.server.index != nil && cs.server.index.Entries != nil {
			entry, found = cs.server.index.Entries[digest]
		}
		cs.server.indexMu.RUnlock()

		if found {
			return "sha256:" + entry.NarDigest, true
		}
	}

	return "", false
}
