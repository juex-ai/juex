package fleetclient

import (
	"context"
	"fmt"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientResolvesOfflineAndChecksResponseIdentity(t *testing.T) {
	home := t.TempDir()
	client := New(home, ProfileSupervisor, "abcdef")
	if _, err := client.Agents(context.Background()); err == nil {
		t.Fatal("offline call succeeded")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(ProfileHeader) != ProfileSupervisor || r.Header.Get(AgentHeader) != "abcdef" {
			t.Error("boot profile/actor missing")
		}
		w.Header().Set(FleetHeader, "another-fleet")
		w.Header().Set(InstanceHeader, "instance")
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	fleetID, err := serviceendpoint.FleetID(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := Publish(home, Endpoint{FleetID: fleetID, InstanceID: "instance", URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Agents(context.Background()); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("wrong Fleet response=%v", err)
	}
}

func TestClientRediscoversFleetAfterRestart(t *testing.T) {
	home := t.TempDir()
	id, err := serviceendpoint.FleetID(home)
	if err != nil {
		t.Fatal(err)
	}
	client := New(home, ProfileSupervisor, "abcdef")
	for _, instance := range []string{"first", "replacement"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(InstanceHeader) != instance {
				t.Error("stale client instance")
			}
			w.Header().Set(FleetHeader, id)
			w.Header().Set(InstanceHeader, instance)
			fmt.Fprint(w, `[{"id":"abcdef","name":"Supervisor"}]`)
		}))
		if err := Publish(home, Endpoint{FleetID: id, InstanceID: instance, URL: server.URL}); err != nil {
			t.Fatal(err)
		}
		agents, err := client.Agents(context.Background())
		server.Close()
		if err != nil || len(agents) != 1 || agents[0].ID != "abcdef" {
			t.Fatalf("%s: %+v %v", instance, agents, err)
		}
	}
}
