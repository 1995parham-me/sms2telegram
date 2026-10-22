package forwarder_test

import (
	"context"
	"errors"
	"testing"

	"github.com/1995parham-me/sms2telegram/internal/domain/forwarder"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

var errSend = errors.New("send failed")

type inbox []sms.SMS

func (i inbox) List(context.Context) ([]sms.SMS, error) { return append([]sms.SMS(nil), i...), nil }

type notifier struct {
	got    []sms.SMS
	failAt int
}

func (n *notifier) Notify(_ context.Context, msg sms.SMS) error {
	if n.failAt > 0 && len(n.got)+1 == n.failAt {
		n.failAt = 0

		return errSend
	}

	n.got = append(n.got, msg)

	return nil
}

type store map[string]bool

func (s store) Seen(key string) bool { return s[key] }

func (s store) MarkSeen(key string) error {
	s[key] = true

	return nil
}

func messages() inbox {
	return inbox{
		{Sender: "98100", Date: "2026-10-22 12:27:20", Content: "newest"},
		{Sender: "", Date: "2026-10-22 12:26:39", Content: "middle"},
		{Sender: "989981000000", Date: "2025-07-03 11:48:03", Content: "Code: 0789"},
	}
}

func TestForwardOldestFirstOnce(t *testing.T) {
	t.Parallel()

	n := &notifier{got: nil, failAt: 0}
	f := forwarder.New(messages(), n, store{})

	sent, err := f.Forward(t.Context())
	if err != nil || sent != 3 {
		t.Fatalf("first pass: sent=%d err=%v", sent, err)
	}

	want := []string{"Code: 0789", "middle", "newest"}
	for i, msg := range n.got {
		if msg.Content != want[i] {
			t.Fatalf("message %d: got %q, want %q", i, msg.Content, want[i])
		}
	}

	sent, err = f.Forward(t.Context())
	if err != nil || sent != 0 {
		t.Fatalf("second pass: sent=%d err=%v", sent, err)
	}
}

func TestForwardRetriesAfterFailure(t *testing.T) {
	t.Parallel()

	n := &notifier{got: nil, failAt: 2}
	f := forwarder.New(messages(), n, store{})

	sent, err := f.Forward(t.Context())
	if !errors.Is(err, errSend) || sent != 1 {
		t.Fatalf("failing pass: sent=%d err=%v", sent, err)
	}

	sent, err = f.Forward(t.Context())
	if err != nil || sent != 2 {
		t.Fatalf("retry pass: sent=%d err=%v", sent, err)
	}

	if len(n.got) != 3 {
		t.Fatalf("delivered %d messages, want 3", len(n.got))
	}
}

func TestKeyDistinguishesFields(t *testing.T) {
	t.Parallel()

	a := sms.SMS{Sender: "ab", Date: "c", Content: "d"}
	b := sms.SMS{Sender: "a", Date: "bc", Content: "d"}

	if a.Key() == b.Key() {
		t.Fatal("keys collide across field boundaries")
	}
}
