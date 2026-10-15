// Package processmetrics samples current process resource usage across the
// operating systems supported by JueX.
package processmetrics

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// Usage is one point-in-time process resource projection. CPUPercent is absent
// until a previous sample exists for the same process identity.
type Usage struct {
	RSSBytes   uint64   `json:"rss_bytes"`
	CPUPercent *float64 `json:"cpu_percent,omitempty"`
}

// Provider supplies process usage for a caller-owned stable key.
type Provider interface {
	Sample(context.Context, string, int) (Usage, error)
}

type snapshot struct {
	rssBytes        uint64
	cpuSeconds      float64
	createTimeMilli int64
}

type snapshotReader func(context.Context, int) (snapshot, error)

type baseline struct {
	pid             int
	createTimeMilli int64
	cpuSeconds      float64
	sampledAt       time.Time
}

// Sampler derives interval CPU usage from cumulative process times.
type Sampler struct {
	mu       sync.Mutex
	read     snapshotReader
	now      func() time.Time
	previous map[string]baseline
}

// New returns a cross-platform process usage sampler.
func New() *Sampler {
	return newSampler(readProcessSnapshot, time.Now)
}

func newSampler(read snapshotReader, now func() time.Time) *Sampler {
	return &Sampler{
		read:     read,
		now:      now,
		previous: make(map[string]baseline),
	}
}

// Sample returns RSS immediately and CPU after a valid prior sample. One fully
// occupied core is 100%, so multi-threaded processes may exceed 100%.
func (s *Sampler) Sample(ctx context.Context, key string, pid int) (Usage, error) {
	if key == "" {
		return Usage{}, fmt.Errorf("process metrics: sample key is required")
	}
	if pid <= 0 {
		return Usage{}, fmt.Errorf("process metrics: invalid pid %d", pid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.read(ctx, pid)
	if err != nil {
		delete(s.previous, key)
		return Usage{}, err
	}
	sampledAt := s.now()
	usage := Usage{RSSBytes: current.rssBytes}
	previous, exists := s.previous[key]
	if exists &&
		previous.pid == pid &&
		previous.createTimeMilli == current.createTimeMilli &&
		current.cpuSeconds >= previous.cpuSeconds {
		elapsed := sampledAt.Sub(previous.sampledAt).Seconds()
		if elapsed > 0 {
			cpuPercent := (current.cpuSeconds - previous.cpuSeconds) / elapsed * 100
			usage.CPUPercent = &cpuPercent
		}
	}
	s.previous[key] = baseline{
		pid:             pid,
		createTimeMilli: current.createTimeMilli,
		cpuSeconds:      current.cpuSeconds,
		sampledAt:       sampledAt,
	}
	return usage, nil
}

// Forget removes one caller-owned CPU baseline.
func (s *Sampler) Forget(key string) {
	s.mu.Lock()
	delete(s.previous, key)
	s.mu.Unlock()
}

// Retain removes baselines that are no longer owned by the caller.
func (s *Sampler) Retain(keys []string) {
	keep := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		keep[key] = struct{}{}
	}
	s.mu.Lock()
	for key := range s.previous {
		if _, exists := keep[key]; !exists {
			delete(s.previous, key)
		}
	}
	s.mu.Unlock()
}

func readProcessSnapshot(ctx context.Context, pid int) (snapshot, error) {
	const maxInt32 = int64(1<<31 - 1)
	if int64(pid) > maxInt32 {
		return snapshot{}, fmt.Errorf("process metrics: pid %d exceeds supported range", pid)
	}
	if err := ctx.Err(); err != nil {
		return snapshot{}, err
	}
	current, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return snapshot{}, fmt.Errorf("process metrics: open pid %d: %w", pid, err)
	}
	createTimeMilli, err := current.CreateTimeWithContext(ctx)
	if err != nil {
		return snapshot{}, fmt.Errorf("process metrics: read pid %d start time: %w", pid, err)
	}
	times, err := current.TimesWithContext(ctx)
	if err != nil {
		return snapshot{}, fmt.Errorf("process metrics: read pid %d cpu time: %w", pid, err)
	}
	memory, err := current.MemoryInfoWithContext(ctx)
	if err != nil {
		return snapshot{}, fmt.Errorf("process metrics: read pid %d memory: %w", pid, err)
	}
	if err := ctx.Err(); err != nil {
		return snapshot{}, err
	}
	cpuSeconds := times.User + times.System
	if createTimeMilli <= 0 ||
		math.IsNaN(cpuSeconds) ||
		math.IsInf(cpuSeconds, 0) ||
		cpuSeconds < 0 {
		return snapshot{}, fmt.Errorf("process metrics: invalid counters for pid %d", pid)
	}
	return snapshot{
		rssBytes:        memory.RSS,
		cpuSeconds:      cpuSeconds,
		createTimeMilli: createTimeMilli,
	}, nil
}
