package statusstream

import (
	"context"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

type testSnapshot struct {
	Cursor string
	State  string
	Nested []string
}

func testStore(historyLimit int) *Store[testSnapshot] {
	return New(testSnapshot{State: "initial"}, Options[testSnapshot]{
		Clone: func(snapshot testSnapshot) testSnapshot {
			snapshot.Nested = append([]string(nil), snapshot.Nested...)
			return snapshot
		},
		Cursor: func(snapshot testSnapshot) string {
			return snapshot.Cursor
		},
		Equal: func(left, right testSnapshot) bool {
			return reflect.DeepEqual(left, right)
		},
		HistoryLimit: historyLimit,
	})
}

func TestStoreOpenReplaysFromCursorAndCurrentPresentation(t *testing.T) {
	store := testStore(8)
	store.Publish(testSnapshot{Cursor: "1", State: "one"}, true)
	store.Publish(testSnapshot{Cursor: "2", State: "two"}, true)
	store.Publish(testSnapshot{Cursor: "2", State: "transient"}, false)

	tests := []struct {
		name  string
		after string
		want  []string
	}{
		{name: "empty cursor", want: []string{"transient"}},
		{name: "known cursor", after: "1", want: []string{"two", "transient"}},
		{name: "same durable cursor", after: "2", want: []string{"transient"}},
		{name: "unknown cursor", after: "missing", want: []string{"transient"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := store.Open(OpenOptions{After: test.after})
			defer stream.Close()
			var got []string
			for {
				snapshot, ok := stream.Next(context.Background())
				if !ok {
					break
				}
				got = append(got, snapshot.State)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("states = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStoreOpenUsesLastMatchingCursor(t *testing.T) {
	store := testStore(8)
	store.Publish(testSnapshot{Cursor: "repeat", State: "first"}, true)
	store.Publish(testSnapshot{Cursor: "middle", State: "middle"}, true)
	store.Publish(testSnapshot{Cursor: "repeat", State: "second"}, true)
	store.Publish(testSnapshot{Cursor: "latest", State: "latest"}, true)

	stream := store.Open(OpenOptions{After: "repeat"})
	defer stream.Close()
	snapshot, ok := stream.Next(context.Background())
	if !ok || snapshot.State != "latest" {
		t.Fatalf("first replay = %+v, %t; want latest", snapshot, ok)
	}
	if _, ok := stream.Next(context.Background()); ok {
		t.Fatal("stream replayed from the first duplicate cursor")
	}
}

func TestStoreReplayDoesNotDuplicateIdenticalUnrecordedCurrent(t *testing.T) {
	store := testStore(8)
	store.Publish(testSnapshot{Cursor: "1", State: "one"}, true)
	current := testSnapshot{Cursor: "2", State: "two"}
	store.Publish(current, true)
	store.Publish(current, false)

	stream := store.Open(OpenOptions{After: "1"})
	defer stream.Close()
	snapshot, ok := stream.Next(context.Background())
	if !ok || snapshot.State != "two" {
		t.Fatalf("first replay = %+v, %t; want two", snapshot, ok)
	}
	if _, ok := stream.Next(context.Background()); ok {
		t.Fatal("stream duplicated an unchanged unrecorded current value")
	}
}

func TestCurrentOnlyStoreIgnoresCursorHistory(t *testing.T) {
	store := testStore(0)
	store.Publish(testSnapshot{Cursor: "same", State: "session-one"}, true)
	store.Publish(testSnapshot{Cursor: "other", State: "session-two"}, true)
	store.Publish(testSnapshot{Cursor: "same", State: "session-three"}, true)

	stream := store.Open(OpenOptions{After: "same"})
	defer stream.Close()
	snapshot, ok := stream.Next(context.Background())
	if !ok || snapshot.State != "session-three" {
		t.Fatalf("current snapshot = %+v, %t", snapshot, ok)
	}
	if _, ok := stream.Next(context.Background()); ok {
		t.Fatal("current-only store returned historical values")
	}
}

func TestStoreReplaceDropsPendingOldValues(t *testing.T) {
	store := testStore(8)
	stream := store.Open(OpenOptions{Follow: true})
	defer stream.Close()
	if _, ok := stream.Next(context.Background()); !ok {
		t.Fatal("stream omitted initial snapshot")
	}

	store.Publish(testSnapshot{Cursor: "old-1", State: "old-one"}, true)
	store.Publish(testSnapshot{Cursor: "old-2", State: "old-two"}, true)
	replacement := testSnapshot{Cursor: "new-1", State: "replacement"}
	store.Replace(replacement, []testSnapshot{replacement})

	snapshot, ok := stream.Next(context.Background())
	if !ok || snapshot.State != "replacement" {
		t.Fatalf("first value after Replace = %+v, %t; want replacement", snapshot, ok)
	}
}

func TestNonFollowingStreamEndsWithoutSubscriber(t *testing.T) {
	store := testStore(8)
	store.Publish(testSnapshot{Cursor: "1", State: "one"}, true)
	stream := store.Open(OpenOptions{Follow: false})

	if len(store.subscribers) != 0 {
		t.Fatalf("subscribers = %d, want 0", len(store.subscribers))
	}
	if _, ok := stream.Next(context.Background()); !ok {
		t.Fatal("stream omitted its replay")
	}
	if _, ok := stream.Next(context.Background()); ok {
		t.Fatal("non-following stream did not end after replay")
	}
	stream.Close()
	stream.Close()
}

func TestStreamCloseWakesBlockedNext(t *testing.T) {
	store := testStore(8)
	stream := store.Open(OpenOptions{Follow: true})
	if _, ok := stream.Next(context.Background()); !ok {
		t.Fatal("stream omitted its initial snapshot")
	}

	result := make(chan bool, 1)
	go func() {
		_, ok := stream.Next(context.Background())
		result <- ok
	}()
	stream.Close()

	select {
	case ok := <-result:
		if ok {
			t.Fatal("Next returned a snapshot after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake blocked Next")
	}
	stream.Close()
}

func TestStoreClonesValuesForEachConsumer(t *testing.T) {
	store := testStore(8)
	first := store.Open(OpenOptions{Follow: true})
	defer first.Close()
	second := store.Open(OpenOptions{Follow: true})
	defer second.Close()
	if _, ok := first.Next(context.Background()); !ok {
		t.Fatal("first stream omitted initial snapshot")
	}
	if _, ok := second.Next(context.Background()); !ok {
		t.Fatal("second stream omitted initial snapshot")
	}

	store.Publish(testSnapshot{Cursor: "1", State: "published", Nested: []string{"original"}}, true)
	firstSnapshot, ok := first.Next(context.Background())
	if !ok {
		t.Fatal("first stream omitted update")
	}
	firstSnapshot.Nested[0] = "mutated"
	secondSnapshot, ok := second.Next(context.Background())
	if !ok {
		t.Fatal("second stream omitted update")
	}
	if secondSnapshot.Nested[0] != "original" {
		t.Fatalf("second nested value = %q", secondSnapshot.Nested[0])
	}
	if snapshot := store.Snapshot(); snapshot.Nested[0] != "original" {
		t.Fatalf("stored nested value = %q", snapshot.Nested[0])
	}
}

func TestSlowSubscriberObservesLatestPublication(t *testing.T) {
	store := testStore(8)
	stream := store.Open(OpenOptions{Follow: true})
	defer stream.Close()
	if _, ok := stream.Next(context.Background()); !ok {
		t.Fatal("stream omitted initial snapshot")
	}

	const publishCount = 256
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(publishCount)
	for i := 1; i <= publishCount; i++ {
		go func(value int) {
			defer wait.Done()
			<-start
			cursor := strconv.Itoa(value)
			store.Publish(testSnapshot{Cursor: cursor, State: cursor}, true)
		}(i)
	}
	close(start)
	wait.Wait()

	want := store.Snapshot()
	var last testSnapshot
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		snapshot, ok := stream.Next(ctx)
		cancel()
		if !ok {
			break
		}
		last = snapshot
	}
	if !reflect.DeepEqual(last, want) {
		t.Fatalf("last snapshot = %+v, want %+v", last, want)
	}
}
