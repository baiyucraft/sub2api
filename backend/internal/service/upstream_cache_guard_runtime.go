package service

import "github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"

// Construction has no goroutine or production side effect. The application
// owns settings refresh, lifecycle and the gateway request bridge.
func NewUpstreamCacheGuardRuntime(store upstreamcacheguard.Store) *upstreamcacheguard.Engine {
	return upstreamcacheguard.NewEngine(store)
}
