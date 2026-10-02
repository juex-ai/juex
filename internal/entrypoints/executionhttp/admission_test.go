package executionhttp

import (
	"fmt"
	"testing"
	"time"
)

func TestAdmissionReclaimsExpiredPeersAtCapacity(t *testing.T) {
	s := &Server{attempts: make(map[string]attempt)}
	for i := range 4096 {
		s.attempts[fmt.Sprintf("192.0.%d.%d", i/256, i%256)] = attempt{at: time.Now(), count: 1}
	}
	if s.admit("203.0.113.9:1234") {
		t.Fatal("admitted a new peer beyond the active table capacity")
	}
	for key, value := range s.attempts {
		value.at = time.Now().Add(-2 * time.Minute)
		s.attempts[key] = value
	}
	for range 240 {
		if !s.admit("203.0.113.9:1234") {
			t.Fatal("expired peers prevented a new peer from connecting")
		}
	}
	if s.admit("203.0.113.9:1234") {
		t.Fatal("reclaiming stale peers disabled the per-peer limit")
	}
}
