// Package heartbeat proves end to end that the SIM still receives SMS: it
// texts the SIM's own number on a schedule and watches for the message to
// come back into the inbox.
//
// Every other check can pass while SMS silently stop: the modem answers, the
// inbox reads, Telegram accepts, and yet the operator delivers nothing (the
// line was suspended, the SIM fell off the network, SMS over LTE broke). Only
// a message that actually arrives tells these apart from a quiet day.
package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

// Prefix starts every heartbeat text. A message starting with it is consumed
// and never forwarded.
const Prefix = "sms2telegram heartbeat "

// retryAfter is how soon a heartbeat whose send failed is tried again.
const retryAfter = time.Hour

// fileMode keeps the record private, like the delivery state beside it.
const fileMode = 0o600

// record is what survives a restart, so a restart neither resends at once
// nor forgets an outstanding heartbeat.
type record struct {
	// Sent is when the last heartbeat was submitted (or tried).
	Sent time.Time `json:"sent"`
	// SendFailed is whether the modem refused or never confirmed it.
	SendFailed bool `json:"send_failed"`
	// Received is when the last heartbeat came back into the inbox.
	Received time.Time `json:"received"`
}

// Status is a snapshot for metrics and alerts.
type Status struct {
	Sent       time.Time
	SendFailed bool
	Received   time.Time
	// Missing is true once a heartbeat has been out longer than the wait
	// without coming back, or the last send failed.
	Missing bool
}

// Heartbeat schedules self-addressed SMS and tracks their return.
type Heartbeat struct {
	outbox   sms.Outbox
	number   string
	interval time.Duration
	wait     time.Duration
	timeout  time.Duration
	path     string

	mu  sync.Mutex
	rec record
}

// New loads the record at path and returns a heartbeat that texts number
// every interval and expects it back within wait. timeout bounds each send.
func New(outbox sms.Outbox, number string, interval, wait, timeout time.Duration, path string) (*Heartbeat, error) {
	h := &Heartbeat{
		outbox:   outbox,
		number:   number,
		interval: interval,
		wait:     wait,
		timeout:  timeout,
		path:     path,
		mu:       sync.Mutex{},
		rec:      record{Sent: time.Time{}, SendFailed: false, Received: time.Time{}},
	}

	data, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's own config.
	if errors.Is(err, fs.ErrNotExist) {
		return h, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading heartbeat record: %w", err)
	}

	if err := json.Unmarshal(data, &h.rec); err != nil {
		return nil, fmt.Errorf("decoding heartbeat record %s: %w", path, err)
	}

	return h, nil
}

// Consume reports whether msg is a heartbeat, recording its return if so.
// The forwarder skips consumed messages.
func (h *Heartbeat) Consume(msg sms.SMS, now time.Time) bool {
	if !strings.HasPrefix(strings.TrimSpace(msg.Content), Prefix) {
		return false
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if now.After(h.rec.Received) {
		h.rec.Received = now
		_ = h.save() // a lost write only means one extra heartbeat after a restart.
	}

	return true
}

// Due reports whether the next heartbeat should go out at now.
func (h *Heartbeat) Due(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.rec.Sent.IsZero() {
		return true
	}

	if h.rec.SendFailed {
		return now.Sub(h.rec.Sent) >= min(retryAfter, h.interval)
	}

	return now.Sub(h.rec.Sent) >= h.interval
}

// Send texts the heartbeat at now. A failed send is recorded too, so it shows
// as missing and is retried within the hour.
func (h *Heartbeat) Send(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	err := h.outbox.Send(ctx, h.number, Prefix+strconv.FormatInt(now.Unix(), 10))

	h.mu.Lock()
	defer h.mu.Unlock()

	h.rec.Sent = now
	h.rec.SendFailed = err != nil

	if saveErr := h.save(); saveErr != nil && err == nil {
		err = saveErr
	}

	if err != nil {
		return fmt.Errorf("sending heartbeat: %w", err)
	}

	return nil
}

// Status returns the current state at now.
func (h *Heartbeat) Status(now time.Time) Status {
	h.mu.Lock()
	defer h.mu.Unlock()

	outstanding := !h.rec.Sent.IsZero() && h.rec.Received.Before(h.rec.Sent)

	return Status{
		Sent:       h.rec.Sent,
		SendFailed: h.rec.SendFailed,
		Received:   h.rec.Received,
		Missing:    h.rec.SendFailed || (outstanding && now.Sub(h.rec.Sent) >= h.wait),
	}
}

func (h *Heartbeat) save() error {
	data, err := json.Marshal(h.rec)
	if err != nil {
		return fmt.Errorf("encoding heartbeat record: %w", err)
	}

	tmp := filepath.Join(filepath.Dir(h.path), ".heartbeat.tmp")
	if err := os.WriteFile(tmp, data, fileMode); err != nil {
		return fmt.Errorf("writing heartbeat record: %w", err)
	}

	if err := os.Rename(tmp, h.path); err != nil {
		return fmt.Errorf("writing heartbeat record: %w", err)
	}

	return nil
}
