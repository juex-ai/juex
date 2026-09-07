package mcp

import (
	"context"
	"sync"
)

// notificationGate retains discovery-time notifications until the runtime's
// recovery boundary is published, and drains callbacks before shutdown.
type notificationGate struct {
	mu       sync.Mutex
	active   bool
	draining bool
	closed   bool
	done     chan struct{}
	pending  []Notification
	deliver  func(Notification)
}

func newNotificationGate(deliver func(Notification)) *notificationGate {
	return &notificationGate{deliver: deliver}
}

func (g *notificationGate) Enqueue(notification Notification) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.pending = append(g.pending, notification)
	if !g.active || g.draining {
		g.mu.Unlock()
		return
	}
	g.draining = true
	g.done = make(chan struct{})
	g.mu.Unlock()
	_ = g.drain(context.Background())
}

func (g *notificationGate) Activate(ctx context.Context) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return context.Canceled
	}
	g.active = true
	if g.draining || len(g.pending) == 0 {
		g.mu.Unlock()
		return nil
	}
	g.draining = true
	g.done = make(chan struct{})
	g.mu.Unlock()
	return g.drain(ctx)
}

func (g *notificationGate) drain(ctx context.Context) error {
	for {
		g.mu.Lock()
		if g.closed || ctx.Err() != nil || len(g.pending) == 0 {
			g.draining = false
			close(g.done)
			g.mu.Unlock()
			return ctx.Err()
		}
		notification := g.pending[0]
		g.pending[0] = Notification{}
		g.pending = g.pending[1:]
		deliver := g.deliver
		g.mu.Unlock()
		if deliver != nil {
			deliver(notification)
		}
	}
}

type notificationDrain struct{ done <-chan struct{} }

func (*notificationDrain) Error() string { return "mcp: notification drain deferred" }
func (d *notificationDrain) Wait() error { <-d.done; return nil }

func (g *notificationGate) Quiesce() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	g.pending = nil
	if g.draining {
		return &notificationDrain{done: g.done}
	}
	return nil
}
