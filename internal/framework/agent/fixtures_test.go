package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type stubProvider struct {
	replies   []llm.Response
	calls     int
	systems   []string
	histories [][]llm.Message
}

type failOnceEventCommitter struct {
	delegate  events.Committer
	eventType string
	err       error
	failed    bool
}

func (c *failOnceEventCommitter) Commit(event events.Event) (events.Event, error) {
	if event.Type == c.eventType && !c.failed {
		c.failed = true
		return events.Event{}, c.err
	}
	return c.delegate.Commit(event)
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Complete(_ context.Context, system string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	if s.calls >= len(s.replies) {
		return llm.Response{}, errors.New("stub exhausted")
	}
	s.systems = append(s.systems, system)
	s.histories = append(s.histories, append([]llm.Message(nil), history...))
	response := s.replies[s.calls]
	s.calls++
	return response, nil
}

type recoveryProvider struct {
	mu        sync.Mutex
	calls     int
	histories [][]llm.Message
	called    chan struct{}
	err       error
}

type retryUntilReleasedEventCommitter struct {
	delegate  events.Committer
	eventType string
	err       error
	release   <-chan struct{}
	failed    chan struct{}
	once      sync.Once
	retrying  chan struct{}
	retryOnce sync.Once
	mu        sync.Mutex
	failures  int
}

func (c *retryUntilReleasedEventCommitter) Commit(event events.Event) (events.Event, error) {
	if event.Type == c.eventType {
		select {
		case <-c.release:
		default:
			c.mu.Lock()
			c.failures++
			failures := c.failures
			c.mu.Unlock()
			c.once.Do(func() { close(c.failed) })
			if failures >= 2 && c.retrying != nil {
				c.retryOnce.Do(func() { close(c.retrying) })
			}
			return events.Event{}, c.err
		}
	}
	return c.delegate.Commit(event)
}

func (p *recoveryProvider) Name() string { return "recovery" }

func (p *recoveryProvider) Complete(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	p.calls++
	p.histories = append(p.histories, append([]llm.Message(nil), history...))
	if p.calls == 1 && p.called != nil {
		close(p.called)
	}
	err := p.err
	p.mu.Unlock()
	if err != nil {
		return llm.Response{}, err
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn}, nil
}

func (p *recoveryProvider) snapshot() (int, [][]llm.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	histories := make([][]llm.Message, len(p.histories))
	for i := range p.histories {
		histories[i] = append([]llm.Message(nil), p.histories[i]...)
	}
	return p.calls, histories
}
