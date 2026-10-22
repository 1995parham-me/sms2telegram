package health_test

import (
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/health"
)

func TestTracker(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }

	tr := health.NewTracker(10 * time.Minute)

	steps := []struct {
		minute int
		ok     bool
		want   health.Event
	}{
		{0, true, health.None},
		{1, false, health.None}, // a blip
		{2, true, health.None},  // over before the threshold: never reported
		{3, false, health.None},
		{12, false, health.None},
		{13, false, health.Down}, // 10 minutes after the outage began
		{14, false, health.None}, // reported once
		{20, true, health.Up},
		{21, true, health.None},
	}

	for _, s := range steps {
		got, _ := tr.Observe(at(s.minute), s.ok)
		if got != s.want {
			t.Fatalf("minute %d ok=%v: got %v, want %v", s.minute, s.ok, got, s.want)
		}
	}
}
