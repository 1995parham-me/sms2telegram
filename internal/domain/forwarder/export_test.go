package forwarder

import "time"

// SetClock replaces the forwarder's clock, so tests can move time past the hold.
func (f *Forwarder) SetClock(now func() time.Time) { f.now = now }
