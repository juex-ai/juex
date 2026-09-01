package app

import (
	"sync"

	"github.com/juex-ai/juex/internal/mcp"
)

// mcpNotificationGate preserves startup notifications until the App has
// published its complete Runtime and Thread Module bundle.
type mcpNotificationGate struct {
	mu       sync.Mutex
	active   bool
	draining bool
	pending  []mcp.Notification
	deliver  func(mcp.Notification)
}

func newMCPNotificationGate(deliver func(mcp.Notification)) *mcpNotificationGate {
	return &mcpNotificationGate{deliver: deliver}
}

func (g *mcpNotificationGate) Enqueue(notification mcp.Notification) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.pending = append(g.pending, notification)
	if !g.active || g.draining {
		g.mu.Unlock()
		return
	}
	g.draining = true
	g.mu.Unlock()
	g.drain()
}

func (g *mcpNotificationGate) Activate() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.active = true
	if g.draining || len(g.pending) == 0 {
		g.mu.Unlock()
		return
	}
	g.draining = true
	g.mu.Unlock()
	g.drain()
}

func (g *mcpNotificationGate) drain() {
	for {
		g.mu.Lock()
		if !g.active || len(g.pending) == 0 {
			g.draining = false
			g.mu.Unlock()
			return
		}
		notification := g.pending[0]
		g.pending[0] = mcp.Notification{}
		g.pending = g.pending[1:]
		deliver := g.deliver
		g.mu.Unlock()
		if deliver != nil {
			deliver(notification)
		}
	}
}
