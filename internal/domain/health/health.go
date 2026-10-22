// Package health decides when a failing modem is worth telling someone about.
package health

import "time"

// Event is what a Tracker concluded from one observation.
type Event int

const (
	// None means nothing changed worth reporting.
	None Event = iota
	// Down means the modem has now been failing for longer than the threshold.
	Down
	// Up means the modem answers again after a Down was reported.
	Up
)

// Tracker reports a modem outage once it has lasted longer than After, and
// its end, but never a blip shorter than After: the dongle drops off USB for
// a few seconds on every reboot.
type Tracker struct {
	after     time.Duration
	failSince time.Time
	reported  bool
}

// NewTracker returns a Tracker with the given threshold.
func NewTracker(after time.Duration) *Tracker {
	return &Tracker{after: after, failSince: time.Time{}, reported: false}
}

// Observe records one poll at now and whether it reached the modem.
// It returns the outage start along with the event.
func (t *Tracker) Observe(now time.Time, ok bool) (Event, time.Time) {
	since := t.failSince

	if ok {
		t.failSince = time.Time{}

		if t.reported {
			t.reported = false

			return Up, since
		}

		return None, since
	}

	if t.failSince.IsZero() {
		t.failSince = now
		since = now
	}

	if !t.reported && now.Sub(t.failSince) >= t.after {
		t.reported = true

		return Down, since
	}

	return None, since
}
