package gc

import (
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
}

// Sweep 在全内存中不产生网络请求地计算依赖关系拓扑
func (e *Engine) Sweep(now time.Time, maxSize int64) *Result {
	markedSet := make(map[string]bool)
	var originalSize int64
	for _, entry := range e.index.Entries {
		originalSize += entry.NarSize
	}

	// 1. 扫描并自动滤除过期 GC Roots
	activeRoots := make([]string, 0)
	for hash, root := range e.index.Roots {
		if root.TTL > 0 && now.Unix() > root.PinnedAt.Unix()+root.TTL {
			delete(e.index.Roots, hash) // 淘汰过期根
			continue
		}
		activeRoots = append(activeRoots, hash)
	}

	// 2. 深度优先搜索（DFS）有向染色
	for _, rootHash := range activeRoots {
		e.dfs(rootHash, markedSet)
	}

	// 3. 安全宽限期（Grace Period）及临时未染色包分类
	candidates := make([]string, 0)
	var retainedSize int64

	for hash, entry := range e.index.Entries {
		if markedSet[hash] {
			retainedSize += entry.NarSize
			continue
		}

		// 若尚在安全宽限期（新上传的临时悬空依赖），强制拉回豁免保留
		if now.Sub(entry.UploadedAt) < e.gracePeriod {
			markedSet[hash] = true
			retainedSize += entry.NarSize
			continue
		}

		candidates = append(candidates, hash)
	}

	// 按最后拉取使用时间 LastUsed 从小到大排序（最冷的数据排在最前面）
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
				// 削减达标，保留剩余较热的候选包
				retainedSize += e.index.Entries[hash].NarSize
				markedSet[hash] = true
				continue
			}
			// 否则，物理移除最冷包
			evictedKeys = append(evictedKeys, hash)
			evictedSize += e.index.Entries[hash].NarSize
			currentSize -= e.index.Entries[hash].NarSize
		}
	} else {
		// 未设定最大空间上限，代表直接清理全部未标记的悬空孤立包
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
	}
}

// Apply 逻辑修改，更新内部结构映射
func (e *Engine) Apply(result *Result) {
	for _, hash := range result.EvictedKeys {
		delete(e.index.Entries, hash)
	}
}

func (e *Engine) dfs(hash string, markedSet map[string]bool) {
	if markedSet[hash] {
		return
	}
	entry, exists := e.index.Entries[hash]
	if !exists {
		return
	}
	markedSet[hash] = true
	for _, ref := range entry.References {
		refHash := getHashFromPath(ref)
		if refHash != "" {
			e.dfs(refHash, markedSet)
		}
	}
}

func getHashFromPath(storePath string) string {
	base := filepath.Base(storePath)
	if len(base) < 32 {
		return ""
	}
	return base[:32]
}
