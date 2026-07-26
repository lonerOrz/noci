package oci

import (
	"strings"
	"sync"
	"time"
)

type CacheIndex struct {
	Version   int                  `json:"version"`
	Repo      string               `json:"repo"`
	Registry  string               `json:"registry"`
	Image     string               `json:"image"`
	Generated time.Time            `json:"generated"`
	PublicKey string               `json:"public_key"`
	GCRootsV1 []string             `json:"gc_roots,omitempty"` // deprecated v1 field
	Roots     map[string]GCRoot    `json:"roots,omitempty"`
	Entries   map[string]IndexItem `json:"entries"`
	Source    string               `json:"source,omitempty"`
	mu        sync.Mutex           `json:"-"`
}

type GCRoot struct {
	PinnedAt time.Time `json:"pinned_at"`
	TTL      int64     `json:"ttl"` // 0 = permanent
}

type IndexItem struct {
	Name       string    `json:"name"`
	NarInfo    string    `json:"narinfo"`
	NarDigest  string    `json:"nar_digest"`
	NarSize    int64     `json:"nar_size"`
	Added      time.Time `json:"added"`
	LastUsed   time.Time `json:"last_used"`
	UploadedAt time.Time `json:"uploaded_at"`
	References []string  `json:"references"`
	Source     string    `json:"source,omitempty"`
}

func NewIndex(registry, repo string) *CacheIndex {
	return &CacheIndex{
		Version:   2,
		Repo:      repo,
		Registry:  registry,
		Image:     registry + "/" + repo + "/nix-cache",
		Generated: time.Now(),
		Roots:     make(map[string]GCRoot),
		Entries:   make(map[string]IndexItem),
	}
}

// Upgrade migrates a V1 index to V2 format in-place.
func (idx *CacheIndex) Upgrade() {
	if idx.Version < 2 {
		idx.Version = 2
	}
	if idx.Roots == nil {
		idx.Roots = make(map[string]GCRoot)
	}

	// Migrate v1 root list to v2 map.
	if len(idx.GCRootsV1) > 0 {
		for _, r := range idx.GCRootsV1 {
			if _, exists := idx.Roots[r]; !exists {
				idx.Roots[r] = GCRoot{
					PinnedAt: time.Now(),
					TTL:      0,
				}
			}
		}
		idx.GCRootsV1 = nil
	}

	// Backfill fields missing from v1 entries.
	for k, entry := range idx.Entries {
		modified := false
		if entry.LastUsed.IsZero() {
			entry.LastUsed = entry.Added
			modified = true
		}
		if entry.UploadedAt.IsZero() {
			entry.UploadedAt = entry.Added
			modified = true
		}
		if len(entry.References) == 0 && entry.NarInfo != "" {
			entry.References = parseReferencesFromNarInfo(entry.NarInfo)
			modified = true
		}
		if modified {
			idx.Entries[k] = entry
		}
	}
}

func (idx *CacheIndex) AddEntry(hash, name, narinfo, digest string, size int64, refs []string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	now := time.Now()
	// Strip "sha256:" prefix so NarDigest stores bare hex.
	hex := strings.TrimPrefix(digest, "sha256:")
	idx.Entries[hash] = IndexItem{
		Name:       name,
		NarInfo:    narinfo,
		NarDigest:  hex,
		NarSize:    size,
		Added:      now,
		LastUsed:   now,
		UploadedAt: now,
		References: refs,
	}
	idx.Generated = now
}

func (idx *CacheIndex) PinRoot(hash string, ttlSeconds int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.Roots == nil {
		idx.Roots = make(map[string]GCRoot)
	}
	idx.Roots[hash] = GCRoot{
		PinnedAt: time.Now(),
		TTL:      ttlSeconds,
	}
}

func parseReferencesFromNarInfo(narinfo string) []string {
	var refs []string
	lines := strings.Split(narinfo, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "References: ") {
			fields := strings.Fields(strings.TrimPrefix(line, "References: "))
			for _, f := range fields {
				refs = append(refs, "/nix/store/"+f)
			}
			break
		}
	}
	return refs
}
