package oci

import "time"

const (
	DefaultHTTPTimeout      = 30 * time.Second
	DefaultStreamTimeout    = 10 * time.Minute
	DefaultTokenCacheTTL    = 4 * time.Minute
	DefaultActiveSyncPeriod = 30 * time.Second
)
