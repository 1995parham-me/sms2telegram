package heartbeat_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/heartbeat"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

var errModem = errors.New("modem did not confirm")

type outbox struct {
	texts []string
	err   error
}

func (o *outbox) Send(_ context.Context, _, text string) error {
	o.texts = append(o.texts, text)

	return o.err
}

const (
	interval = 12 * time.Hour
	wait     = 15 * time.Minute
)

func begin() time.Time { return time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC) }

func open(t *testing.T, out *outbox, path string) *heartbeat.Heartbeat {
	t.Helper()

	h, err := heartbeat.New(out, "+989390909540", interval, wait, time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}

	return h
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	start := begin()

	out := &outbox{texts: nil, err: nil}
	h := open(t, out, filepath.Join(t.TempDir(), "hb.json"))

	if !h.Due(start) {
		t.Fatal("a fresh heartbeat is due at once")
	}

	if err := h.Send(t.Context(), start); err != nil {
		t.Fatal(err)
	}

	if h.Status(start.Add(wait - time.Second)).Missing {
		t.Fatal("missing before the wait is over")
	}

	if !h.Status(start.Add(wait)).Missing {
		t.Fatal("not missing after the wait")
	}

	msg := sms.SMS{Sender: "+989390909540", Name: "", Date: "d", Content: out.texts[0], Position: 0}
	if !h.Consume(msg, start.Add(wait+time.Minute)) {
		t.Fatal("the heartbeat text was not consumed")
	}

	if h.Status(start.Add(wait + time.Minute)).Missing {
		t.Fatal("still missing after it came back")
	}

	if h.Due(start.Add(interval-time.Second)) || !h.Due(start.Add(interval)) {
		t.Fatal("the next heartbeat is due one interval after the last")
	}
}

func TestIgnoresOtherMessages(t *testing.T) {
	t.Parallel()

	start := begin()

	h := open(t, &outbox{texts: nil, err: nil}, filepath.Join(t.TempDir(), "hb.json"))

	if h.Consume(sms.SMS{Sender: "PARSIANBANK", Name: "", Date: "d", Content: "Code: 1234", Position: 0}, start) {
		t.Fatal("an ordinary message was consumed")
	}
}

func TestFailedSendIsMissingAndRetried(t *testing.T) {
	t.Parallel()

	start := begin()

	h := open(t, &outbox{texts: nil, err: errModem}, filepath.Join(t.TempDir(), "hb.json"))

	if err := h.Send(t.Context(), start); !errors.Is(err, errModem) {
		t.Fatalf("err=%v, want the modem error", err)
	}

	if !h.Status(start).Missing {
		t.Fatal("a failed send counts as missing at once")
	}

	if !h.Due(start.Add(time.Hour)) {
		t.Fatal("a failed send is retried within the hour")
	}
}

func TestSurvivesRestart(t *testing.T) {
	t.Parallel()

	start := begin()

	path := filepath.Join(t.TempDir(), "hb.json")
	out := &outbox{texts: nil, err: nil}

	if err := open(t, out, path).Send(t.Context(), start); err != nil {
		t.Fatal(err)
	}

	h := open(t, out, path)
	if h.Due(start.Add(time.Hour)) {
		t.Fatal("a restart resent the heartbeat")
	}

	if !h.Status(start.Add(wait)).Missing {
		t.Fatal("a restart forgot the outstanding heartbeat")
	}

	if !strings.HasPrefix(out.texts[0], heartbeat.Prefix) {
		t.Fatalf("text %q lacks the prefix", out.texts[0])
	}
}
