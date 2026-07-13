package notification

import (
	"testing"
	"time"
)

// TestParseOsloTimestamp is the regression for the prod bug where every ceilometer
// notification returned 400: oslo emits a space-separated, timezone-less timestamp
// ("2026-07-11 10:08:51.622578") that a plain *time.Time field cannot decode. ParseOsloBody
// must accept it (and RFC3339) and preserve the value, and must not reject a message whose
// timestamp is malformed/absent.
func TestParseOsloTimestamp(t *testing.T) {
	want := time.Date(2026, 7, 11, 10, 8, 51, 622578000, time.UTC)
	cases := []struct {
		name, body string
		wantZero   bool
		wantTime   time.Time
	}{
		{"oslo-space-frac", `{"event_type":"e","timestamp":"2026-07-11 10:08:51.622578","payload":{}}`, false, want},
		{"oslo-space-no-frac", `{"event_type":"e","timestamp":"2026-07-11 10:08:51","payload":{}}`, false, time.Date(2026, 7, 11, 10, 8, 51, 0, time.UTC)},
		{"rfc3339", `{"event_type":"e","timestamp":"2026-07-11T10:08:51.622578Z","payload":{}}`, false, want},
		{"oslo-envelope", `{"oslo.version":"2.0","oslo.message":"{\"event_type\":\"e\",\"timestamp\":\"2026-07-11 10:08:51.622578\",\"payload\":{}}"}`, false, want},
		{"absent", `{"event_type":"e","payload":{}}`, true, time.Time{}},
		{"null", `{"event_type":"e","timestamp":null,"payload":{}}`, true, time.Time{}},
		{"garbage-ts-still-parses", `{"event_type":"e","timestamp":"not-a-time","payload":{}}`, true, time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, err := ParseOsloBody([]byte(c.body))
			if err != nil {
				t.Fatalf("ParseOsloBody errored (would 400 the notification): %v", err)
			}
			if msg.EventType != "e" {
				t.Fatalf("event_type lost: %q", msg.EventType)
			}
			if c.wantZero {
				if !msg.Timestamp.IsZero() {
					t.Fatalf("expected zero timestamp, got %v", msg.Timestamp.Time)
				}
				return
			}
			if !msg.Timestamp.UTC().Equal(c.wantTime) {
				t.Fatalf("timestamp = %v, want %v", msg.Timestamp.UTC(), c.wantTime)
			}
		})
	}
}
