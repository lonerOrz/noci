package oci

import (
	"strings"
	"time"
)

type CacheIndex struct {
	Version   int                  `json:"version"`
	Repo      string               `json:"repo"`
	Registry  string               `json:"registry"`
	Image     string               `json:"image"`
	Generated time.Time            `json:"generated"`
	PublicKey string               `json:"public_key"`
	GCRootsV1 []string             `json:"gc_roots,omitempty"` // 向前兼容 v1 废弃字段
	Roots     map[string]GCRoot    `json:"roots,omitempty"`    // v2 弹性的根结构
	Entries   map[string]IndexItem `json:"entries"`
}

type GCRoot struct {
	PinnedAt time.Time `json:"pinned_at"`
	TTL      int64     `json:"ttl"` // TTL 周期，0 表示永久固定
}

type IndexItem struct {
	Name       string    `json:"name"`
	NarInfo    string    `json:"narinfo"`
	NarDigest  string    `json:"nar_digest"`
	NarSize    int64     `json:"nar_size"`
	Added      time.Time `json:"added"`
	LastUsed   time.Time `json:"last_used"`   // 活跃时间戳（LRU 支撑）
	UploadedAt time.Time `json:"uploaded_at"` // 物理上传时间（安全宽限期支撑）
	References []string  `json:"references"`  // 预析出的哈希依赖图，实现零网络开销图搜索
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

// Upgrade 将从 OCI 中拉取到的原有结构安全平滑地迁移至 V2 规范，避免历史污染
func (idx *CacheIndex) Upgrade() {
	if idx.Version < 2 {
		idx.Version = 2
	}
	if idx.Roots == nil {
		idx.Roots = make(map[string]GCRoot)
	}

	// 转换 V1 历史 GC roots 为 V2 Roots Map
	if len(idx.GCRootsV1) > 0 {
		for _, r := range idx.GCRootsV1 {
			if _, exists := idx.Roots[r]; !exists {
				idx.Roots[r] = GCRoot{
					PinnedAt: time.Now(),
					TTL:      0,
				}
			}
		}
		idx.GCRootsV1 = nil // 彻底抹除旧属性
	}

	// 补全由于 V1 没有写入的时间和依赖字段
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
	now := time.Now()
	// Normalize: strip "sha256:" prefix from digest so NarDigest always stores bare hex.
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
