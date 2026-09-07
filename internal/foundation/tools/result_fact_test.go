package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestExecutionFactValidationAndCancellation(t *testing.T) {
	for _, mode := range []string{"valid", "invalid", "canceled-after-execution"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fact := json.RawMessage(`{"completed":true}`)
			if mode == "invalid" {
				fact = json.RawMessage(`{invalid`)
			}
			registry := NewRegistry()
			registry.MustRegister(Tool{Name: "owned-operation", ResultHandler: func(context.Context, map[string]any) (Result, error) {
				if mode == "canceled-after-execution" {
					cancel()
				}
				return Result{Text: "executed", Fact: fact}, nil
			}})
			_, info, err := registry.CallWithInfo(ctx, "owned-operation", nil)
			if mode == "invalid" {
				if err == nil || len(info.Fact) != 0 {
					t.Fatalf("invalid fact accepted: %s %v", info.Fact, err)
				}
				return
			}
			if mode == "valid" && err != nil {
				t.Fatal(err)
			}
			if mode == "canceled-after-execution" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error=%v", err)
			}
			if string(info.Fact) != string(fact) {
				t.Fatalf("execution fact lost: %s", info.Fact)
			}
			fact[0] = 'x'
			if !json.Valid(info.Fact) {
				t.Fatal("handler retained a mutable reference to the recorded fact")
			}
		})
	}
}
