package server

import (
	"context"
	"net/http"
	"noci/pkg/log"
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
	updateChan     chan string
}

func NewServer(registry, repo, token, addr, upstream string, ttl int) *Server {
	return &Server{
		addr:       addr,
		upstream:   upstream,
		ttl:        ttl,
		client:     oci.NewClient(registry, repo, token),
		updateChan: make(chan string, 1000),
	}
}

// Start 启动本地代理 HTTP 服务器
func (s *Server) Start(ctx context.Context) error {
	go s.RefreshIndex()
	go s.startUpdateWorker(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/nix-cache-info", s.HandleNixCacheInfo)
	mux.HandleFunc("/", s.HandleRoutes)

	srv := &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	// 监听上下文关闭信号
	go func() {
		<-ctx.Done()
		log.Info("Shutting down proxy server gracefully...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
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
		log.Success("Cache-index refreshed. Loaded %d entries.", len(idx.Entries))
	} else {
		log.Warning("Failed to refresh cache-index: %v", err)
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

// startUpdateWorker 增加 Context 检测，在 Proxy 退出前强制将缓存回写到 OCI
func (s *Server) startUpdateWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	pendingUpdates := make(map[string]time.Time)

	for {
		select {
		case <-ctx.Done():
			// 如果收到优雅退出信号，立即强行 Flush 回写所有积攒的时间戳
			if len(pendingUpdates) > 0 {
				log.Info("Flushing remaining LastUsed updates before shutdown...")
				s.flushLastUsedUpdates(pendingUpdates)
			}
			return
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

// flushLastUsedUpdates 在写锁保护下通过重新 Fetch 的乐观锁设计，防范 CI 端的 Lost Update 灾难
func (s *Server) flushLastUsedUpdates(updates map[string]time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// 重新 Fetch 最新远端索引，进行单向的时间戳乐观合并
	idx, err := s.client.FetchIndex(ctx)
	if err != nil {
		log.Warning("Failed to fetch index for LastUsed flush: %v", err)
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

	// 推送回写
	if err := s.client.PushIndex(ctx, idx); err == nil {
		s.index = idx
		s.lastIndexFetch = time.Now()
		log.Success("Successfully flushed %d LastUsed updates to OCI.", len(updates))
	} else {
		log.Warning("Failed to flush LastUsed updates to OCI: %v", err)
	}
}
