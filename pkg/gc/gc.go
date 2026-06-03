package gc

import (
	"noci/pkg/nix"
	"noci/pkg/oci"
	"path/filepath"
	"sort"
	"time"
)

type Engine struct {
	index       *oci.CacheIndex
	gracePeriod time.Duration
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
	ExpiredRoots  []string // 👈 在计算阶段收集过期 roots，不在 Sweep 阶段原地修改 index 指针
}

func (e *Engine) Sweep(now time.Time, maxSize int64) *Result {
	markedSet := make(map[string]bool)
	var originalSize int64
	for _, entry := range e.index.Entries {
		originalSize += entry.NarSize
	}

	// 1. 扫描 GC Roots（仅打标收集，保证 Dry-Run 的只读纯粹性）
	activeRoots := make([]string, 0)
	expiredRoots := make([]string, 0)
	for hash, root := range e.index.Roots {
		if root.TTL > 0 && now.Unix() > root.PinnedAt.Unix()+root.TTL {
			expiredRoots = append(expiredRoots, hash)
			continue
		}
		activeRoots = append(activeRoots, hash)
	}

	// 2. 迭代式工作队列染色
	e.scanClosure(activeRoots, markedSet)

	// 3. 安全宽限期（Grace Period）及临时未染色包分类
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

	sort.Slice(candidates, func(i, j int) bool {
		return e.index.Entries[candidates[i]].LastUsed.Before(e.index.Entries[candidates[j]].LastUsed)
	})

	evictedKeys := make([]string, 0)
	var evictedSize int64

	// 4. 配额限制约束收缩
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

// Apply 执行真正的 Entries 与 Roots 物理删除（仅在此物理写入阶段触发）
func (e *Engine) Apply(result *Result) {
	for _, hash := range result.ExpiredRoots {
		delete(e.index.Roots, hash)
	}
	for _, hash := range result.EvictedKeys {
		delete(e.index.Entries, hash)
	}
}

// scanClosure 使用显式本地切片工作栈替代递归，杜绝深层拓扑引起的协程栈扩张与溢出
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

// CascadeEvict 执行定向级联驱逐：驱逐指定目标及其所有间接依赖的上层包
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

	return &Result{
		OriginalCount: len(e.index.Entries),
		OriginalSize:  originalSize,
		RetainedCount: len(e.index.Entries) - len(evictedKeys),
		RetainedSize:  originalSize - evictedSize,
		EvictedCount:  len(evictedKeys),
		EvictedSize:   evictedSize,
		EvictedKeys:   evictedKeys,
		ExpiredRoots:  targets,
	}
}

func getHashFromPath(storePath string) string {
	base := filepath.Base(storePath)
	if len(base) < 32 {
		return ""
	}
	hash := base[:32]
	if !nix.IsValidNixHash(hash) {
		return ""
	}
	return hash
}
