package managedruntime

import (
	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"strings"
	"testing"
)

func TestInputReminderBoundsPreviewsWithoutForgettingIdentities(t *testing.T) {
	items := make([]InputReminder, MaxTrackedInputs)
	for i := range items {
		m := llm.TextMessage(llm.RoleUser, strings.Repeat("必须继续验证真实行为。", 10000))
		m.ID = uuid.NewString()
		items[i] = InputReminder{InputID: uuid.NewString(), Message: m}
	}
	text := InputReminderContext(items, ModelConfig{ContextWindow: 8192})
	if len(text) > 100000 {
		t.Fatal("unbounded reminder", len(text))
	}
	for _, item := range items {
		if !strings.Contains(text, item.InputID) || !strings.Contains(text, ContextReference(item.Message.ID, 0, "text")) {
			t.Fatal("lost identity/reference", item.InputID)
		}
	}
	if llm.EstimateTextTokens(text) <= 1024 {
		t.Fatal("impossible minimum budget must be visible to request admission")
	}
}
