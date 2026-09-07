package mcp

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestMCPNotificationGateBuffersUntilActivation(t *testing.T) {
	var delivered []string
	gate := newNotificationGate(func(notification Notification) {
		delivered = append(delivered, notification.Content)
	})

	gate.Enqueue(Notification{Content: "first"})
	gate.Enqueue(Notification{Content: "second"})
	if len(delivered) != 0 {
		t.Fatalf("delivered before activation = %v", delivered)
	}

	if err := gate.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	gate.Enqueue(Notification{Content: "third"})
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(delivered, want) {
		t.Fatalf("delivered = %v, want %v", delivered, want)
	}
}

func TestMCPNotificationGatePreservesReentrantOrder(t *testing.T) {
	var gate *notificationGate
	var delivered []string
	gate = newNotificationGate(func(notification Notification) {
		delivered = append(delivered, notification.Content)
		if notification.Content == "first" {
			gate.Enqueue(Notification{Content: "third"})
		}
	})
	gate.Enqueue(Notification{Content: "first"})
	gate.Enqueue(Notification{Content: "second"})

	if err := gate.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(delivered, want) {
		t.Fatalf("delivered = %v, want %v", delivered, want)
	}
}

func TestNotificationGateQuiesceDefersCallbackAndDropsLateInput(t *testing.T) {
	var gate *notificationGate
	var deferred interface{ Wait() error }
	var delivered []string
	gate = newNotificationGate(func(notification Notification) {
		delivered = append(delivered, notification.Content)
		if err := gate.Quiesce(); !errors.As(err, &deferred) {
			t.Fatalf("quiesce in callback = %v, want deferred drain", err)
		}
		gate.Enqueue(Notification{Content: "late callback"})
	})
	gate.Enqueue(Notification{Content: "first"})
	gate.Enqueue(Notification{Content: "pending"})
	if err := gate.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := deferred.Wait(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := gate.Quiesce(); err != nil {
			t.Fatal(err)
		}
	}
	gate.Enqueue(Notification{Content: "after close"})
	if err := gate.Activate(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("activate after close = %v", err)
	}
	if !reflect.DeepEqual(delivered, []string{"first"}) {
		t.Fatalf("delivered = %v", delivered)
	}
}

func TestNotificationGateQuiesceWaitsForLiveDelivery(t *testing.T) {
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var delivered []string
	gate := newNotificationGate(func(notification Notification) {
		delivered = append(delivered, notification.Content)
		close(started)
		<-release
	})
	if err := gate.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() {
		gate.Enqueue(Notification{Content: "live"})
		close(finished)
	}()
	<-started
	var deferred interface{ Wait() error }
	if err := gate.Quiesce(); !errors.As(err, &deferred) {
		t.Fatalf("live quiesce = %v, want deferred drain", err)
	}
	gate.Enqueue(Notification{Content: "late"})
	close(release)
	_ = deferred.Wait()
	<-finished
	if err := gate.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(delivered, []string{"live"}) {
		t.Fatalf("delivered = %v", delivered)
	}
}
