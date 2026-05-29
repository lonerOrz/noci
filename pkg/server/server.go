package server

import (
	"context"
	"fmt"
	"net/http"
	"noci/pkg/oci"
	"sync"
	"time"
)

type Server struct {
	addr           string
	upstream       string
	ttl            int
	client         *oci.Client
	index          *oci.CacheIndex
	lastIndexFetch time.Time
	mu             sync.RWMutex
	updateChan     chan string // 异步延迟聚合 LastUsed 通道
}

func NewServer(registry, repo, token, addr, upstream string, ttl int) *Server {
	return &Server{
		addr:       addr,
		upstream:   upstream,
		ttl:        ttl,
		client:     oci.NewClient(registry, repo, token),
		updateChan: make(chan string, 1000), // 1000 高吞吐缓冲区
	}
}

func (s *Server) Start() error {
	go s.RefreshIndex()
	go s.startUpdateWorker() // 后台高吞吐异步聚合批处理器

	mux := http.NewServeMux()
	mux.HandleFunc("/nix-cache-info", s.HandleNixCacheInfo)
	mux.HandleFunc("/", s.HandleRoutes)

	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) RefreshIndex() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshIndexLocked()
}

func (s *Server) refreshIndexLocked() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	idx, err := s.client.FetchIndex(ctx)
	if err == nil {
		s.index = idx
		s.lastIndexFetch = time.Now()
		fmt.Printf("[noci-proxy] Cache-index refreshed. Loaded %d entries.\n", len(idx.Entries))
	} else {
		fmt.Printf("[noci-proxy] Failed to refresh cache-index: %v\n", err)
	}
}

func (s *Server) GetIndex() *oci.CacheIndex {
	s.mu.RLock()
	isCacheValid := s.index != nil && time.Since(s.lastIndexFetch) <= time.Duration(s.ttl)*time.Second
	if isCacheValid {
		idx := s.index
		s.mu.RUnlock()
		return idx
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.index == nil || time.Since(s.lastIndexFetch) > time.Duration(s.ttl)*time.Second {
		s.refreshIndexLocked()
	}
	return s.index
}

// startUpdateWorker 后台聚合器：每5分钟（或每积攒100个活跃信号）触发一次批量乐观更新
func (s *Server) startUpdateWorker() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	pendingUpdates := make(map[string]time.Time)

	for {
		select {
		case hash, ok := <-s.updateChan:
			if !ok {
				return
			}
			pendingUpdates[hash] = time.Now()
			if len(pendingUpdates) >= 100 {
				s.flushLastUsedUpdates(pendingUpdates)
				pendingUpdates = make(map[string]time.Time)
			}
		case <-ticker.C:
			if len(pendingUpdates) > 0 {
				s.flushLastUsedUpdates(pendingUpdates)
				pendingUpdates = make(map[string]time.Time)
			}
		}
	}
}

func (s *Server) flushLastUsedUpdates(updates map[string]time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// 实时拉取最新索引以进行防冲突 CAS 状态合并
	idx, err := s.client.FetchIndex(ctx)
	if err != nil {
		fmt.Printf("[noci-proxy] Failed to fetch index for LastUsed flush: %v\n", err)
		return
	}

	modified := false
	for hash, lastUsed := range updates {
		if entry, exists := idx.Entries[hash]; exists {
			entry.LastUsed = lastUsed
			idx.Entries[hash] = entry
			modified = true
		}
	}

	if !modified {
		return
	}

	if err := s.client.PushIndex(ctx, idx); err == nil {
		s.index = idx
		s.lastIndexFetch = time.Now()
		fmt.Printf("[noci-proxy] Successfully flushed %d LastUsed updates to OCI.\n", len(updates))
	} else {
		fmt.Printf("[noci-proxy] Failed to flush LastUsed updates to OCI: %v\n", err)
	}
}
