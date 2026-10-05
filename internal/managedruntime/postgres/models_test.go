package postgres

import (
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func TestModelPlanRequiresExplicitValidOutputReservation(t *testing.T) {
	for _, tc := range []struct {
		cap, reserve int
		protocol     llm.Protocol
		valid        bool
	}{
		{0, 4096, llm.ProtocolOpenAIChat, true}, {512, 4096, llm.ProtocolOpenAIChat, true},
		{512, 0, llm.ProtocolOpenAIChat, false}, {0, 0, llm.ProtocolOpenAIChat, false},
		{-1, 4096, llm.ProtocolOpenAIChat, false}, {4096, 512, llm.ProtocolOpenAIChat, false},
		{0, 32768, llm.ProtocolOpenAIChat, false}, {0, 512, llm.ProtocolAnthropicMessages, false},
		{0, 4096, llm.ProtocolAnthropicMessages, true}, {512, 512, llm.ProtocolAnthropicMessages, true},
	} {
		plan := managedruntime.TurnConfig{Models: []managedruntime.ModelConfig{{ModelID: "model", Model: "test", Provider: "fixture", Protocol: tc.protocol, ContextWindow: 32768, MaxOutput: tc.cap, OutputReserve: tc.reserve}}}
		if validModelPlan(plan) != tc.valid {
			t.Fatal("unexpected frozen model budget admission", tc)
		}
	}
}
