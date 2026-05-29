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
}

func NewServer(registry, repo, token, addr, upstream string, ttl int) *Server {
	return &Server{
		addr:     addr,
		upstream: upstream,
		ttl:      ttl,
		client:   oci.NewClient(registry, repo, token),
	}
}

// Start 启动本地代理 HTTP 服务器
func (s *Server) Start() error {
	// 启动时在后台先拉取一次索引
	go s.RefreshIndex()

	mux := http.NewServeMux()
	mux.HandleFunc("/nix-cache-info", s.HandleNixCacheInfo)
	mux.HandleFunc("/", s.HandleRoutes)

	return http.ListenAndServe(s.addr, mux)
}

// RefreshIndex 供外部、后台定时器或初始化调用的同步刷新方法
func (s *Server) RefreshIndex() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshIndexLocked()
}

// refreshIndexLocked 内部调用的核心刷新方法（调用者必须持有写锁）
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

// GetIndex 安全获取远程缓存索引（带有更直观的多重检查锁定路径）
func (s *Server) GetIndex() *oci.CacheIndex {
	s.mu.RLock()
	isCacheValid := s.index != nil && time.Since(s.lastIndexFetch) <= time.Duration(s.ttl)*time.Second
	if isCacheValid {
		idx := s.index
		s.mu.RUnlock()
		return idx
	}
	s.mu.RUnlock()

	// 缓存过期或未加载，升级至写锁状态
	s.mu.Lock()
	defer s.mu.Unlock()

	// 双重检查锁定 (Double-Checked Locking)
	if s.index == nil || time.Since(s.lastIndexFetch) > time.Duration(s.ttl)*time.Second {
		s.refreshIndexLocked()
	}
	return s.index
}
