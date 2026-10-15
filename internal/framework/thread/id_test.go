package thread

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestThreadIDValidation(t *testing.T) {
	t.Parallel()
	for _, id := range []string{MainID, "012345", "abcdef", "zzzzzz"} {
		if !ValidID(id) {
			t.Fatalf("ValidID(%q) = false", id)
		}
	}
	for _, id := range []string{"", "00", "12345", "1234567", "ABCDEF", "oooooo", "llllll", "uuuuuu", "../../"} {
		if ValidID(id) {
			t.Fatalf("ValidID(%q) = true", id)
		}
	}
	if ValidWorkerID(MainID) {
		t.Fatal("Main id must not be a Worker id")
	}
}

func TestNewWorkerIDUsesSixLowercaseCrockfordCharacters(t *testing.T) {
	t.Parallel()
	id, err := newWorkerID(bytes.NewReader([]byte{0, 1, 10, 18, 31, 255}))
	if err != nil {
		t.Fatal(err)
	}
	if id != "01ajzz" {
		t.Fatalf("id = %q", id)
	}
	if !ValidWorkerID(id) {
		t.Fatalf("generated id %q is invalid", id)
	}
	if alias := DefaultWorkerAlias(id); alias != "worker_#01ajzz" {
		t.Fatalf("default alias = %q", alias)
	}
}

func TestTimestampRequiresCanonicalUTCMilliseconds(t *testing.T) {
	t.Parallel()
	value := NewTimestamp(time.Date(2026, 9, 1, 8, 12, 34, 567890123, time.FixedZone("x", 8*60*60)))
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"2026-09-01T00:12:34.567Z"` {
		t.Fatalf("encoded = %s", data)
	}
	for _, invalid := range []string{
		`"2026-09-01T00:12:34Z"`,
		`"2026-09-01T00:12:34.567890Z"`,
		`"2026-09-01T08:12:34.567+08:00"`,
		`null`,
	} {
		var decoded Timestamp
		if err := json.Unmarshal([]byte(invalid), &decoded); err == nil {
			t.Fatalf("json.Unmarshal(%s) succeeded", invalid)
		}
	}
}
