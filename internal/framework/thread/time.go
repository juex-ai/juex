package thread

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

const timestampLayout = "2006-01-02T15:04:05.000Z"

// Timestamp is the sole persisted absolute-time representation in Thread
// state. It deliberately rejects fractional precision other than milliseconds.
type Timestamp struct {
	time.Time
}

func NewTimestamp(value time.Time) Timestamp {
	return Timestamp{Time: value.UTC().Truncate(time.Millisecond)}
}

func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return nil, fmt.Errorf("thread: zero timestamp")
	}
	return json.Marshal(t.UTC().Format(timestampLayout))
}

func (t *Timestamp) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("thread: nil timestamp receiver")
	}
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("thread: null timestamp")
	}
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return fmt.Errorf("thread: decode timestamp: %w", err)
	}
	parsed, err := time.Parse(timestampLayout, encoded)
	if err != nil || parsed.Format(timestampLayout) != encoded {
		return fmt.Errorf("thread: timestamp %q must be UTC RFC3339 milliseconds", encoded)
	}
	t.Time = parsed
	return nil
}
