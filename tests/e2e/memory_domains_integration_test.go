//go:build integration

package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/memory"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/framework/agentstate"
)

// Fixed evidence exercises model maintenance semantics, not merely valid JSON.
// Deterministic domain/transaction failures are tested without a live provider.
func TestLiveConfigs_MemoryDomainMaintenanceEvaluation(t *testing.T) {
	selected := loadLiveConfigs(t)[0]
	home := t.TempDir()
	api, user := startMemoryFixture(t, home, mc.Basic)
	cfg := selected.cfg
	cfg.Preset = config.PresetMinimal
	cfg.Modules = config.ModulePolicy{memory.ModuleID: {Enabled: true}, "worker-threads": {Enabled: true}}
	cfg.HomeJuexDir = home
	cfg.AgentID = "supervisor"
	cfg.MemoryProfile = mc.ProfileSupervisor
	cfg.AgentStateDir = filepath.Join(home, "agents", cfg.AgentID)
	cfg.AgentAddress = agentstate.AgentAddress{}
	supervisor := memoryApp(t, cfg, nil)
	has := func(page mc.FactPage, predicate, value string) bool {
		for _, v := range page.Facts {
			object := v.Fact.Value
			if v.Object != nil {
				object = v.Object.Name
			}
			if v.Fact.Predicate == predicate && strings.Contains(strings.ToLower(object), strings.ToLower(value)) {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name, text string
		check      func(mc.FactPage, mc.FactPage) bool
	}{
		{"initial", "Explicitly remember: I currently reside in Shanghai. I like climbing.", func(current, history mc.FactPage) bool {
			return has(current, "resides_in", "Shanghai") && has(current, "prefers", "climbing")
		}},
		{"confirmation", "Please remember my confirmation: I still like climbing. This repeats the same preference and does not change its effective time.", func(current, history mc.FactPage) bool {
			count := 0
			confirmed := false
			for _, v := range current.Facts {
				if v.Fact.Predicate == "prefers" && strings.Contains(strings.ToLower(v.Fact.Value), "climbing") {
					count++
					confirmed = len(v.Fact.Sources) >= 2
				}
			}
			return count == 1 && confirmed
		}},
		{"addition-and-intention", "Also remember that I like photography. Keep climbing too. I am considering moving to Hangzhou, but I have NOT moved and still reside in Shanghai.", func(current, history mc.FactPage) bool {
			return has(current, "prefers", "climbing") && has(current, "prefers", "photography") && has(current, "resides_in", "Shanghai") && has(current, "intends_residence", "Hangzhou")
		}},
		{"relocation", "Update my memory: I completed my move from Shanghai to Hangzhou on 2026-09-01T00:00:00Z. I currently reside in Hangzhou; my earlier Shanghai residence was true before that date.", func(current, history mc.FactPage) bool {
			return has(current, "resides_in", "Hangzhou") && !has(current, "resides_in", "Shanghai") && has(history, "resides_in", "Shanghai")
		}},
		{"correction", "Correct my earlier statement: photography was a mistaken preference claim. I actually like drawing. Keep climbing. Retain the mistaken statement as corrected audit evidence, not a historical preference that was once true.", func(current, history mc.FactPage) bool {
			corrected := false
			for _, v := range history.Facts {
				if v.Fact.Status == "corrected" && strings.Contains(strings.ToLower(v.Fact.Value), "photography") {
					corrected = true
				}
			}
			return corrected && has(current, "prefers", "drawing") && !has(current, "prefers", "photography") && has(current, "prefers", "climbing")
		}},
		{"retraction", "I withdraw support for my earlier drawing preference claim. Mark it retracted and retain the audit evidence; this is not a privacy deletion. Keep climbing.", func(current, history mc.FactPage) bool {
			retracted := false
			for _, v := range history.Facts {
				if v.Fact.Status == "retracted" && strings.Contains(strings.ToLower(v.Fact.Value), "drawing") {
					retracted = true
				}
			}
			return retracted && !has(current, "prefers", "drawing") && has(current, "prefers", "climbing")
		}},
		{"different-people", "Remember two different people: my friend Alex from the chess club and my friend Alex from the cycling club. These are distinct people who happen to share a name. Do not merge them.", func(current, history mc.FactPage) bool {
			ids := map[string]bool{}
			for _, v := range current.Facts {
				if v.Fact.Predicate == "friend_of" && v.Object != nil && strings.Contains(strings.ToLower(v.Object.Name), "alex") {
					ids[v.Object.ID] = true
				}
			}
			return len(ids) == 2
		}},
		{"separate-projects", "Remember: project Alpha uses Go; project Beta uses Python. These are separate projects and neither technology replaces the other project's choice.", func(current, history mc.FactPage) bool {
			projects := map[string]string{}
			identities := map[string]string{}
			for _, v := range current.Facts {
				if v.Fact.Predicate == "uses_technology" && v.Object != nil {
					for _, name := range []string{"alpha", "beta"} {
						if strings.Contains(strings.ToLower(v.Subject.Name), name) {
							projects[name] = strings.ToLower(v.Object.Name)
							identities[name] = v.Subject.ID
						}
					}
				}
			}
			return projects["alpha"] == "go" && projects["beta"] == "python" && identities["alpha"] != identities["beta"]
		}},
		{"overdue-obligation", "Remember my still-unfulfilled commitment to send the report, due 2026-01-01T00:00:00Z. It remains overdue; it is not completed or cancelled. Also remember that I was temporarily in Beijing only during [2026-01-01T00:00:00Z, 2026-01-03T00:00:00Z). That trip has ended and does not change my residence.", func(current, history mc.FactPage) bool {
			overdue := false
			for _, v := range current.Facts {
				if v.Fact.Domain == "obligations" && v.Lifecycle == "overdue" {
					overdue = true
				}
			}
			return overdue && !has(current, "temporarily_at", "Beijing") && has(history, "temporarily_at", "Beijing")
		}},
	}
	personID := ""
	for i, tc := range cases {
		if !t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 210*time.Second)
			defer cancel()
			recordedAt := time.Date(2026, 8, 1+i, 12, 0, 0, 0, time.UTC)
			if i >= 3 {
				recordedAt = time.Date(2026, 9, i-1, 12, 0, 0, 0, time.UTC)
			}
			source := mc.Source{FleetID: user.FleetID, AgentID: user.AgentID, ThreadID: user.ThreadID, GenerationID: "g000001", From: uint64(i + 1), Through: uint64(i + 1)}
			receipt, err := api.Propose(ctx, user, mc.Proposal{Key: fmt.Sprintf("eval-%02d", i), Text: tc.text, Reason: "Explicit user maintenance", Sources: []mc.Source{source}, Evidence: []mc.Evidence{{Source: source, Kind: "user", Text: tc.text, RecordedAt: recordedAt}}})
			if err != nil {
				t.Fatal(err)
			}
			for !receipt.Committed {
				receipt, err = api.Result(ctx, user, receipt.ID)
				if err != nil {
					t.Fatal(err)
				}
				if receipt.State == "failed" || receipt.State == "rejected" || receipt.State == "no_change" {
					kind := "model_semantic_failure"
					if receipt.State == "failed" {
						kind = "worker_execution_failure"
					}
					t.Fatalf("%s provider=%s: %+v", kind, selected.name, receipt)
				}
				select {
				case <-ctx.Done():
					t.Fatalf("worker_execution_timeout provider=%s: %+v", selected.name, receipt)
				case <-time.After(250 * time.Millisecond):
				}
			}
			current, err := api.Facts(ctx, user, mc.Query{Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			history, err := api.Facts(ctx, user, mc.Query{View: "history", Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				for _, v := range current.Facts {
					if v.Fact.Predicate == "resides_in" {
						personID = v.Subject.ID
					}
				}
			}
			for _, v := range current.Facts {
				switch v.Fact.Predicate {
				case "resides_in", "intends_residence", "prefers", "committed_to", "temporarily_at":
					if v.Subject.ID != personID {
						t.Fatalf("model semantic failure: same user's identity split across domains (%s vs %s)", personID, v.Subject.ID)
					}
				}
			}
			if !tc.check(current, history) {
				t.Fatalf("model semantic failure provider=%s case=%s current=%+v history=%+v", selected.name, tc.name, current, history)
			}
			t.Logf("semantic outcome PASS provider=%s case=%s receipt=%s current=%d history=%d", selected.name, tc.name, receipt.ID, len(current.Facts), len(history.Facts))
		}) {
			return // Later evidence depends on the preceding committed state.
		}
	}
	// Persisted Worker history proves templates were actually used by the model.
	workers, err := supervisor.Workers().List()
	if err != nil {
		t.Fatal(err)
	}
	used := false
	for _, worker := range workers {
		loaded, err := supervisor.ThreadStore.OpenActive(worker.ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range loaded.History {
			for _, block := range message.Blocks {
				if block.ToolName == memory.ToolDomains {
					used = true
				}
			}
		}
	}
	if !used {
		t.Fatal("live Worker never read a domain template")
	}
}
