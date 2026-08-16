package app

import (
	"reflect"
	"testing"

	"github.com/juex-ai/juex/internal/mcp"
)

func TestMCPNotificationGateBuffersUntilActivation(t *testing.T) {
	var delivered []string
	gate := newMCPNotificationGate(func(notification mcp.Notification) {
		delivered = append(delivered, notification.Content)
	})

	gate.Enqueue(mcp.Notification{Content: "first"})
	gate.Enqueue(mcp.Notification{Content: "second"})
	if len(delivered) != 0 {
		t.Fatalf("delivered before activation = %v", delivered)
	}

	gate.Activate()
	gate.Enqueue(mcp.Notification{Content: "third"})
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(delivered, want) {
		t.Fatalf("delivered = %v, want %v", delivered, want)
	}
}

func TestMCPNotificationGatePreservesReentrantOrder(t *testing.T) {
	var gate *mcpNotificationGate
	var delivered []string
	gate = newMCPNotificationGate(func(notification mcp.Notification) {
		delivered = append(delivered, notification.Content)
		if notification.Content == "first" {
			gate.Enqueue(mcp.Notification{Content: "third"})
		}
	})
	gate.Enqueue(mcp.Notification{Content: "first"})
	gate.Enqueue(mcp.Notification{Content: "second"})

	gate.Activate()
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(delivered, want) {
		t.Fatalf("delivered = %v, want %v", delivered, want)
	}
}
