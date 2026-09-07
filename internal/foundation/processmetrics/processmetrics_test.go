package processmetrics

import (
	"context"
	"errors"
	"math"
	"os"
	"sync"
	"testing"
	"time"
)

func TestSamplerReadsCurrentProcess(t *testing.T) {
	sampler := New()
	first, err := sampler.Sample(context.Background(), "self", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if first.RSSBytes == 0 || first.CPUPercent != nil {
		t.Fatalf("first sample = %+v", first)
	}
	time.Sleep(10 * time.Millisecond)
	second, err := sampler.Sample(context.Background(), "self", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if second.RSSBytes == 0 || second.CPUPercent == nil {
		t.Fatalf("second sample = %+v", second)
	}
}

func TestSamplerReportsRSSAndIntervalCPU(t *testing.T) {
	snapshots := []snapshot{
		{rssBytes: 64_000_000, cpuSeconds: 3, createTimeMilli: 1000},
		{rssBytes: 80_000_000, cpuSeconds: 4.5, createTimeMilli: 1000},
		{rssBytes: 81_000_000, cpuSeconds: 4.5, createTimeMilli: 1000},
	}
	times := []time.Time{
		time.Unix(100, 0),
		time.Unix(100, 500_000_000),
		time.Unix(101, 500_000_000),
	}
	sampler := newSampler(
		func(context.Context, int) (snapshot, error) {
			next := snapshots[0]
			snapshots = snapshots[1:]
			return next, nil
		},
		func() time.Time {
			next := times[0]
			times = times[1:]
			return next
		},
	)

	first, err := sampler.Sample(context.Background(), "agent-a", 42)
	if err != nil {
		t.Fatal(err)
	}
	if first.RSSBytes != 64_000_000 {
		t.Fatalf("first rss = %d", first.RSSBytes)
	}
	if first.CPUPercent != nil {
		t.Fatalf("first cpu = %v, want unavailable", *first.CPUPercent)
	}

	second, err := sampler.Sample(context.Background(), "agent-a", 42)
	if err != nil {
		t.Fatal(err)
	}
	if second.RSSBytes != 80_000_000 {
		t.Fatalf("second rss = %d", second.RSSBytes)
	}
	if second.CPUPercent == nil || math.Abs(*second.CPUPercent-300) > 0.0001 {
		t.Fatalf("second cpu = %v, want 300", second.CPUPercent)
	}

	idle, err := sampler.Sample(context.Background(), "agent-a", 42)
	if err != nil {
		t.Fatal(err)
	}
	if idle.CPUPercent == nil || *idle.CPUPercent != 0 {
		t.Fatalf("idle cpu = %v, want zero", idle.CPUPercent)
	}
}

func TestSamplerResetsCPUForPIDReuseCounterRegressionAndReadFailure(t *testing.T) {
	readErr := errors.New("process disappeared")
	snapshots := []struct {
		snapshot snapshot
		err      error
	}{
		{snapshot: snapshot{rssBytes: 10, cpuSeconds: 1, createTimeMilli: 1000}},
		{snapshot: snapshot{rssBytes: 20, cpuSeconds: 2, createTimeMilli: 2000}},
		{snapshot: snapshot{rssBytes: 21, cpuSeconds: 1, createTimeMilli: 2000}},
		{err: readErr},
		{snapshot: snapshot{rssBytes: 30, cpuSeconds: 3, createTimeMilli: 2000}},
	}
	now := time.Unix(100, 0)
	sampler := newSampler(
		func(context.Context, int) (snapshot, error) {
			next := snapshots[0]
			snapshots = snapshots[1:]
			return next.snapshot, next.err
		},
		func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	)

	for index := 0; index < 3; index++ {
		usage, err := sampler.Sample(context.Background(), "agent-a", 42)
		if err != nil || usage.CPUPercent != nil {
			t.Fatalf("reset sample %d = %+v, %v", index, usage, err)
		}
	}
	if _, err := sampler.Sample(context.Background(), "agent-a", 42); !errors.Is(err, readErr) {
		t.Fatalf("read error = %v, want %v", err, readErr)
	}
	afterFailure, err := sampler.Sample(context.Background(), "agent-a", 42)
	if err != nil || afterFailure.CPUPercent != nil {
		t.Fatalf("sample after failure = %+v, %v", afterFailure, err)
	}
}

func TestSamplerForgetsAndRetainsBaselines(t *testing.T) {
	now := time.Unix(100, 0)
	cpu := 0.0
	sampler := newSampler(
		func(context.Context, int) (snapshot, error) {
			cpu++
			return snapshot{rssBytes: 10, cpuSeconds: cpu, createTimeMilli: 1000}, nil
		},
		func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	)

	_, _ = sampler.Sample(context.Background(), "agent-a", 1)
	_, _ = sampler.Sample(context.Background(), "agent-b", 2)
	sampler.Forget("agent-a")
	if usage, _ := sampler.Sample(context.Background(), "agent-a", 1); usage.CPUPercent != nil {
		t.Fatalf("forgotten baseline produced cpu = %v", usage.CPUPercent)
	}

	sampler.Retain([]string{"agent-a"})
	if usage, _ := sampler.Sample(context.Background(), "agent-b", 2); usage.CPUPercent != nil {
		t.Fatalf("pruned baseline produced cpu = %v", usage.CPUPercent)
	}
}

func TestSamplerSerializesConcurrentSamples(t *testing.T) {
	now := time.Unix(100, 0)
	cpu := 0.0
	sampler := newSampler(
		func(context.Context, int) (snapshot, error) {
			cpu += 0.25
			return snapshot{rssBytes: 10, cpuSeconds: cpu, createTimeMilli: 1000}, nil
		},
		func() time.Time {
			now = now.Add(250 * time.Millisecond)
			return now
		},
	)

	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := sampler.Sample(context.Background(), "agent-a", 42); err != nil {
				t.Errorf("sample: %v", err)
			}
		}()
	}
	wait.Wait()
}
