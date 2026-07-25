package gc

import (
	"noci/pkg/oci"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var nixHashRe = regexp.MustCompile(`^[0-9abcdfghijklmnpqrsvwxyz]{32}$`)

type Engine struct {
	index        *oci.CacheIndex
	gracePeriod  time.Duration
	keepVersions int
}

func (e *Engine) SetKeepVersions(n int) {
	e.keepVersions = n
}

func NewEngine(index *oci.CacheIndex, gracePeriod time.Duration) *Engine {
	if index == nil {
		panic("gc: nil index passed to NewEngine")
	}
	return &Engine{
		index:       index,
		gracePeriod: gracePeriod,
	}
}

type Result struct {
	OriginalCount int
	OriginalSize  int64
	RetainedCount int
	RetainedSize  int64
	EvictedCount  int
	EvictedSize   int64
	EvictedKeys   []string
	ExpiredRoots  []string // Collected during sweep phase, not mutated in-place
}

func (e *Engine) Sweep(now time.Time, maxSize int64) *Result {
	markedSet := make(map[string]bool)
	var originalSize int64
	for _, entry := range e.index.Entries {
		originalSize += entry.NarSize
	}

	// 1. Scan GC Roots (mark-only for dry-run purity)
	activeRoots := make([]string, 0)
	expiredRoots := make([]string, 0)
	for hash, root := range e.index.Roots {
		if root.TTL > 0 && now.Unix() > root.PinnedAt.Unix()+root.TTL {
			expiredRoots = append(expiredRoots, hash)
			continue
		}
		activeRoots = append(activeRoots, hash)
	}

	// 2. Iterative work-queue coloring
	e.scanClosure(activeRoots, markedSet)

	// 3. Grace period: protect recently uploaded packages
	candidates := make([]string, 0)
	var retainedSize int64

	for hash, entry := range e.index.Entries {
		if markedSet[hash] {
			retainedSize += entry.NarSize
			continue
		}

		if now.Sub(entry.UploadedAt) < e.gracePeriod {
			markedSet[hash] = true
			retainedSize += entry.NarSize
			continue
		}

		candidates = append(candidates, hash)
	}

	// 3.5 Keep N versions per package
	if e.keepVersions > 0 {
		versionGroups := make(map[string][]string)
		for hash, entry := range e.index.Entries {
			if markedSet[hash] {
				continue
			}
			base := basePackageName(entry.Name)
			versionGroups[base] = append(versionGroups[base], hash)
		}
		for _, group := range versionGroups {
			if len(group) <= e.keepVersions {
				continue
			}
			sort.Slice(group, func(i, j int) bool {
				return e.index.Entries[group[i]].UploadedAt.After(e.index.Entries[group[j]].UploadedAt)
			})
			for _, hash := range group[:e.keepVersions] {
				markedSet[hash] = true
				retainedSize += e.index.Entries[hash].NarSize
			}
		}
		candidates = make([]string, 0)
		for hash := range e.index.Entries {
			if !markedSet[hash] {
				candidates = append(candidates, hash)
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return e.index.Entries[candidates[i]].LastUsed.Before(e.index.Entries[candidates[j]].LastUsed)
	})

	evictedKeys := make([]string, 0)
	var evictedSize int64

	// 4. Quota-constrained shrink
	if maxSize > 0 {
		currentSize := retainedSize
		for _, hash := range candidates {
			currentSize += e.index.Entries[hash].NarSize
		}

		for _, hash := range candidates {
			if currentSize <= maxSize {
				retainedSize += e.index.Entries[hash].NarSize
				markedSet[hash] = true
				continue
			}
			evictedKeys = append(evictedKeys, hash)
			evictedSize += e.index.Entries[hash].NarSize
			currentSize -= e.index.Entries[hash].NarSize
		}
	} else {
		evictedKeys = candidates
		for _, hash := range candidates {
			evictedSize += e.index.Entries[hash].NarSize
		}
	}

	return &Result{
		OriginalCount: len(e.index.Entries),
		OriginalSize:  originalSize,
		RetainedCount: len(e.index.Entries) - len(evictedKeys),
		RetainedSize:  retainedSize,
		EvictedCount:  len(evictedKeys),
		EvictedSize:   evictedSize,
		EvictedKeys:   evictedKeys,
		ExpiredRoots:  expiredRoots,
	}
}

// Apply permanently deletes entries and roots from the index.
func (e *Engine) Apply(result *Result) {
	for _, hash := range result.ExpiredRoots {
		delete(e.index.Roots, hash)
	}
	for _, hash := range result.EvictedKeys {
		delete(e.index.Entries, hash)
	}
}

// scanClosure uses an explicit stack to avoid recursion overflow on deep dependency chains.
func (e *Engine) scanClosure(activeRoots []string, markedSet map[string]bool) {
	queue := append([]string{}, activeRoots...)

	for len(queue) > 0 {
		curr := queue[len(queue)-1]
		queue = queue[:len(queue)-1]

		if markedSet[curr] {
			continue
		}

		entry, exists := e.index.Entries[curr]
		if !exists {
			continue
		}

		markedSet[curr] = true

		for _, ref := range entry.References {
			refHash := getHashFromPath(ref)
			if refHash != "" && !markedSet[refHash] {
				queue = append(queue, refHash)
			}
		}
	}
}

// CascadeEvict evicts targets and all packages that depend on them.
func (e *Engine) CascadeEvict(targets []string) *Result {
	var originalSize int64
	for _, entry := range e.index.Entries {
		originalSize += entry.NarSize
	}

	revDeps := make(map[string][]string)
	for hash, entry := range e.index.Entries {
		for _, ref := range entry.References {
			refHash := getHashFromPath(ref)
			if refHash != "" {
				revDeps[refHash] = append(revDeps[refHash], hash)
			}
		}
	}

	evictedSet := make(map[string]bool)
	queue := append([]string{}, targets...)

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if evictedSet[curr] {
			continue
		}
		if _, exists := e.index.Entries[curr]; !exists {
			continue
		}
		evictedSet[curr] = true

		if deps, ok := revDeps[curr]; ok {
			queue = append(queue, deps...)
		}
	}

	evictedKeys := make([]string, 0, len(evictedSet))
	var evictedSize int64
	for hash := range evictedSet {
		evictedKeys = append(evictedKeys, hash)
		evictedSize += e.index.Entries[hash].NarSize
	}

	expiredSet := make(map[string]bool)
	for _, t := range targets {
		expiredSet[t] = true
	}

	for hash := range evictedSet {
		if _, isRoot := e.index.Roots[hash]; isRoot {
			expiredSet[hash] = true
		}
	}

	expiredRoots := make([]string, 0, len(expiredSet))
	for hash := range expiredSet {
		expiredRoots = append(expiredRoots, hash)
	}

	return &Result{
		OriginalCount: len(e.index.Entries),
		OriginalSize:  originalSize,
		RetainedCount: len(e.index.Entries) - len(evictedKeys),
		RetainedSize:  originalSize - evictedSize,
		EvictedCount:  len(evictedKeys),
		EvictedSize:   evictedSize,
		EvictedKeys:   evictedKeys,
		ExpiredRoots:  expiredRoots,
	}
}

func basePackageName(name string) string {
	parts := strings.Split(name, "-")

	// First pass: require "." or length >= 4 to identify version segments.
	for i := len(parts) - 1; i > 0; i-- {
		p := parts[i]
		if len(p) > 0 && p[0] >= '0' && p[0] <= '9' && (strings.Contains(p, ".") || len(p) >= 4) {
			return strings.Join(parts[:i], "-")
		}
	}

	// Fallback: simple numeric version segments (e.g. "13", "3").
	for i := len(parts) - 1; i > 0; i-- {
		p := parts[i]
		if len(p) > 0 && p[0] >= '0' && p[0] <= '9' {
			return strings.Join(parts[:i], "-")
		}
	}

	return name
}

func getHashFromPath(storePath string) string {
	base := filepath.Base(storePath)
	if len(base) < 32 {
		return ""
	}
	hash := base[:32]
	if !nixHashRe.MatchString(hash) {
		return ""
	}
	return hash
}
