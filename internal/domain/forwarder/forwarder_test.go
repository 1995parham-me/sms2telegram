package forwarder_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/1995parham-me/sms2telegram/internal/domain/forwarder"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

var errSend = errors.New("send failed")

// pageSize is the DWR-910M's six, cut to two so a few messages span pages.
const pageSize = 2

// inbox is a modem inbox, newest first, that counts the pages it serves.
type inbox struct {
	msgs  []sms.SMS
	reads int
}

func (i *inbox) Page(_ context.Context, index int) (sms.Page, error) {
	i.reads++

	total := max(1, (len(i.msgs)+pageSize-1)/pageSize)
	page := sms.Page{Messages: nil, Total: total}

	for pos := (index - 1) * pageSize; pos < min(index*pageSize, len(i.msgs)); pos++ {
		msg := i.msgs[pos]
		msg.Position = pos
		page.Messages = append(page.Messages, msg)
	}

	return page, nil
}

func (i *inbox) Delete(_ context.Context, _ int, positions []int) error {
	for _, pos := range slices.Backward(slices.Sorted(slices.Values(positions))) {
		i.msgs = slices.Delete(i.msgs, pos, pos+1)
	}

	return nil
}

// arrive puts msg on top of the inbox, as the modem does.
func (i *inbox) arrive(msg sms.SMS) { i.msgs = slices.Insert(i.msgs, 0, msg) }

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

func messages() *inbox {
	return &inbox{reads: 0, msgs: []sms.SMS{
		{Sender: "98100", Date: "2026-10-22 12:27:20", Content: "newest", Position: 0},
		{Sender: "", Date: "2026-10-22 12:26:39", Content: "middle", Position: 0},
		{Sender: "989981000000", Date: "2025-07-03 11:48:03", Content: "Code: 0789", Position: 0},
	}}
}

func TestForwardOldestFirstOnce(t *testing.T) {
	t.Parallel()

	n := &notifier{got: nil, failAt: 0}
	f := forwarder.New(messages(), n, store{}, false)

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
	f := forwarder.New(messages(), n, store{}, false)

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

func TestForwardStopsAtADeliveredPage(t *testing.T) {
	t.Parallel()

	in := &inbox{reads: 0, msgs: nil}
	for k := range 9 {
		in.arrive(sms.SMS{Sender: "98100", Date: fmt.Sprintf("2026-10-22 12:00:%02d", k), Content: "old", Position: 0})
	}

	f := forwarder.New(in, &notifier{got: nil, failAt: 0}, store{}, false)

	forward(t, f, in, 9, 5)
	forward(t, f, in, 0, 1)

	// Three arrivals fill page 1 and spill onto page 2, so page 3 is the first with nothing new.
	for k := range 3 {
		in.arrive(sms.SMS{Sender: "98100", Date: fmt.Sprintf("2026-10-22 13:00:%02d", k), Content: "new", Position: 0})
	}

	forward(t, f, in, 3, 3)
}

// forward runs one pass and checks how much it sent and how many pages it read.
func forward(t *testing.T, f *forwarder.Forwarder, in *inbox, wantSent, wantReads int) {
	t.Helper()

	in.reads = 0

	sent, err := f.Forward(t.Context())
	if err != nil || sent != wantSent || in.reads != wantReads {
		t.Fatalf("pass: sent=%d err=%v reads=%d, want sent=%d reads=%d", sent, err, in.reads, wantSent, wantReads)
	}
}

func TestForwardDeletesDeliveredBeyondFirstPage(t *testing.T) {
	t.Parallel()

	in := &inbox{reads: 0, msgs: nil}
	for k := range 5 {
		in.arrive(sms.SMS{Sender: "98100", Date: fmt.Sprintf("2026-10-22 12:00:%02d", k), Content: "m", Position: 0})
	}

	n := &notifier{got: nil, failAt: 0}
	f := forwarder.New(in, n, store{}, true)

	if sent, err := f.Forward(t.Context()); err != nil || sent != 5 {
		t.Fatalf("pass: sent=%d err=%v", sent, err)
	}

	if len(in.msgs) != pageSize || in.msgs[0].Date != "2026-10-22 12:00:04" || in.msgs[1].Date != "2026-10-22 12:00:03" {
		t.Fatalf("inbox after pass: %+v, want the two newest", in.msgs)
	}
}

func TestForwardKeepsUndeliveredOnFailure(t *testing.T) {
	t.Parallel()

	in := messages()
	n := &notifier{got: nil, failAt: 1}
	f := forwarder.New(in, n, store{}, true)

	if _, err := f.Forward(t.Context()); !errors.Is(err, errSend) {
		t.Fatalf("pass: err=%v, want the send error", err)
	}

	if len(in.msgs) != 3 {
		t.Fatalf("a failed pass deleted messages: %d left", len(in.msgs))
	}
}

func TestKeyDistinguishesFields(t *testing.T) {
	t.Parallel()

	a := sms.SMS{Sender: "ab", Date: "c", Content: "d", Position: 0}
	b := sms.SMS{Sender: "a", Date: "bc", Content: "d", Position: 0}

	if a.Key() == b.Key() {
		t.Fatal("keys collide across field boundaries")
	}
}
