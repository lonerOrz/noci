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
	fetchMu        sync.Mutex
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

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.startUpdateWorker(ctx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/nix-cache-info", s.HandleNixCacheInfo)
	mux.HandleFunc("/", s.HandleRoutes)

	srv := &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	// 关闭监听器
	go func() {
		<-ctx.Done()
		log.Info("Shutting down HTTP listeners gracefully...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}

	log.Info("Waiting for pending background workers to flush...")
	wg.Wait()
	log.Success("All entries flushed. Proxy stopped safely.")
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
	s.lastIndexFetch = time.Now()
	if err == nil {
		s.index = idx
		log.Success("Cache-index refreshed. Loaded %d entries.", len(idx.Entries))
	} else {
		log.Warning("Failed to refresh cache-index: %v", err)
	}
}

func (s *Server) GetIndex() *oci.CacheIndex {
	s.mu.RLock()
	isCacheValid := time.Since(s.lastIndexFetch) <= time.Duration(s.ttl)*time.Second
	if isCacheValid {
		idx := s.index
		s.mu.RUnlock()
		return idx
	}
	s.mu.RUnlock()

	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	s.mu.RLock()
	if time.Since(s.lastIndexFetch) <= time.Duration(s.ttl)*time.Second {
		idx := s.index
		s.mu.RUnlock()
		return idx
	}
	s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	idx, err := s.client.FetchIndex(ctx)

	s.mu.Lock()
	s.lastIndexFetch = time.Now()
	if err == nil {
		s.index = idx
		log.Success("Cache-index refreshed. Loaded %d entries.", len(idx.Entries))
	} else {
		log.Warning("Failed to refresh cache-index: %v", err)
	}
	s.mu.Unlock()

	return s.index
}

func (s *Server) startUpdateWorker(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute) // 🌟 从 5m 延长到 15m，显著降低 OCI 更新载荷
	defer ticker.Stop()

	pendingUpdates := make(map[string]time.Time)

	for {
		select {
		case <-ctx.Done():
			if len(pendingUpdates) > 0 {
				log.Info("Flushing remaining LastUsed updates before shutdown...")
				s.flushLastUsedUpdates(context.Background(), pendingUpdates)
			}
			return
		case hash, ok := <-s.updateChan:
			if !ok {
				return
			}
			pendingUpdates[hash] = time.Now()
			if len(pendingUpdates) >= 500 {
				s.flushLastUsedUpdates(ctx, pendingUpdates)
				pendingUpdates = make(map[string]time.Time)
			}
		case <-ticker.C:
			if len(pendingUpdates) > 0 {
				s.flushLastUsedUpdates(ctx, pendingUpdates)
				pendingUpdates = make(map[string]time.Time)
			}
		}
	}
}

func (s *Server) flushLastUsedUpdates(ctx context.Context, updates map[string]time.Time) {
	ioCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	idx, err := s.client.FetchIndex(ioCtx)
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

	if err := s.client.PushIndex(ioCtx, idx); err != nil {
		log.Warning("Failed to flush LastUsed updates to OCI: %v", err)
		return
	}

	s.mu.Lock()
	s.index = idx
	s.lastIndexFetch = time.Now()
	s.mu.Unlock()

	log.Success("Successfully flushed %d LastUsed updates to OCI.", len(updates))
}
