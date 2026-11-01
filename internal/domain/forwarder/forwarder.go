// Package forwarder moves new messages from an inbox to a notifier exactly once.
package forwarder

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

// ErrInbox marks a pass that failed because the modem could not be read, as
// opposed to a failed delivery.
var ErrInbox = errors.New("reading inbox")

// DefaultHold is how long a multipart message missing a part waits for the
// rest before it is forwarded as it stands. Parts usually follow within
// seconds; a part the network never delivers must not hold the message back
// for good.
const DefaultHold = 5 * time.Minute

// Forwarder delivers every message it has not delivered before.
type Forwarder struct {
	inbox    sms.Inbox
	notifier sms.Notifier
	store    sms.Store
	prune    bool
	hold     time.Duration
	now      func() time.Time
	// waiting maps an incomplete message's key to when it was first read.
	waiting map[string]time.Time
	// consume, when set, claims messages that are not for Telegram, such as
	// heartbeats; they are recorded as delivered without being sent.
	consume func(sms.SMS) bool
	// records is how many messages the inbox held at the last read.
	records int
}

// New builds a Forwarder over the given ports. With prune, every pass that
// delivers everything also deletes the delivered messages beyond the first
// page, so the inbox never fills up.
func New(inbox sms.Inbox, notifier sms.Notifier, store sms.Store, prune bool) *Forwarder {
	return &Forwarder{
		inbox:    inbox,
		notifier: notifier,
		store:    store,
		prune:    prune,
		hold:     DefaultHold,
		now:      time.Now,
		waiting:  map[string]time.Time{},
		consume:  nil,
		records:  0,
	}
}

// SetConsumer installs fn to claim messages that must not be forwarded.
func (f *Forwarder) SetConsumer(fn func(sms.SMS) bool) { f.consume = fn }

// Records returns how many messages the inbox held at the last read.
func (f *Forwarder) Records() int { return f.records }

// Forward runs one pass and returns how many messages it delivered.
//
// Messages go out oldest first and each is marked seen only after it is
// delivered, so a failed send stops the pass and the same message is retried
// on the next one: nothing is lost, nothing is reordered.
func (f *Forwarder) Forward(ctx context.Context) (int, error) {
	msgs, pages, err := f.unseen(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInbox, err)
	}

	// The modem's date format ("2006-01-02 15:04:05") sorts lexically.
	slices.SortStableFunc(msgs, func(a, b sms.SMS) int {
		switch {
		case a.Date < b.Date:
			return -1
		case a.Date > b.Date:
			return 1
		default:
			return 0
		}
	})

	sent, err := f.deliver(ctx, msgs)
	if err != nil {
		return sent, err
	}

	if f.prune && pages > 1 {
		if err := f.deleteDelivered(ctx, pages); err != nil {
			return sent, fmt.Errorf("%w: %w", ErrInbox, err)
		}
	}

	return sent, nil
}

// seen reports whether msg was delivered, under either key format.
func (f *Forwarder) seen(msg sms.SMS) bool {
	return f.store.Seen(msg.Key()) || f.store.Seen(msg.LegacyKey())
}

// holding reports whether msg, a multipart message still missing a part,
// should wait for the rest. The complete message has different content and
// so a different key: holding the partial one is what keeps both from being
// forwarded.
func (f *Forwarder) holding(msg sms.SMS) bool {
	key := msg.Key()

	first, ok := f.waiting[key]
	if !ok {
		first = f.now()
		f.waiting[key] = first
	}

	return f.now().Sub(first) < f.hold
}

// deliver sends msgs in order, recording each one as soon as it is
// delivered. A multipart message missing a part is skipped until the part
// arrives or it has waited out the hold.
func (f *Forwarder) deliver(ctx context.Context, msgs []sms.SMS) (int, error) {
	sent := 0

	f.forgetGone(msgs)

	for _, msg := range msgs {
		if f.seen(msg) {
			continue
		}

		if claimed, err := f.claim(msg); claimed || err != nil {
			if err != nil {
				return sent, err
			}

			continue
		}

		if msg.Incomplete() && f.holding(msg) {
			continue
		}

		if err := f.notifier.Notify(ctx, msg); err != nil {
			return sent, fmt.Errorf("delivering message from %q at %s: %w", msg.Sender, msg.Date, err)
		}

		delete(f.waiting, msg.Key())

		if err := f.store.MarkSeen(msg.Key()); err != nil {
			return sent, fmt.Errorf("recording delivery: %w", err)
		}

		sent++
	}

	return sent, nil
}

// forgetGone drops the wait-list entries of messages no longer pending.
func (f *Forwarder) forgetGone(msgs []sms.SMS) {
	keep := map[string]bool{}
	for _, msg := range msgs {
		keep[msg.Key()] = true
	}

	maps.DeleteFunc(f.waiting, func(key string, _ time.Time) bool { return !keep[key] })
}

// claim hands msg to the consumer and, when it takes it, records it as
// delivered without sending it.
func (f *Forwarder) claim(msg sms.SMS) (bool, error) {
	if f.consume == nil || !f.consume(msg) {
		return false, nil
	}

	if err := f.store.MarkSeen(msg.Key()); err != nil {
		return true, fmt.Errorf("recording delivery: %w", err)
	}

	return true, nil
}

// unseen reads the inbox newest first and returns the messages not delivered
// yet, with the inbox's page count. Delivery goes oldest first, so a page
// with nothing new means every older page was delivered too, and the walk
// stops there: usually after one page, however full the inbox is.
//
// A message arriving mid-walk pushes the rest down a position, so one may be
// read twice; it is delivered once, since delivery checks the store again.
func (f *Forwarder) unseen(ctx context.Context) ([]sms.SMS, int, error) {
	var out []sms.SMS

	pages := 1

	for index := 1; index <= pages; index++ {
		page, err := f.inbox.Page(ctx, index)
		if err != nil {
			return nil, 0, err //nolint:wrapcheck // the adapter's error already names the page.
		}

		if index == 1 {
			pages = page.Total
			f.records = page.Records
		}

		before := len(out)

		for _, msg := range page.Messages {
			if !f.seen(msg) {
				out = append(out, msg)
			}
		}

		if len(out) == before {
			break
		}
	}

	return out, pages, nil
}

// deleteDelivered deletes the delivered messages from the last page back to
// the second. Each page is read again right before its delete, and deleting
// from the end never moves the positions of the pages still to come. Only an
// arrival between that read and the delete shifts them, by one each, so
// leaving the first page alone means it would take a page's worth of arrivals
// in that instant to hit a message that has not been delivered.
func (f *Forwarder) deleteDelivered(ctx context.Context, pages int) error {
	for index := pages; index > 1; index-- {
		page, err := f.inbox.Page(ctx, index)
		if err != nil {
			return err //nolint:wrapcheck // the adapter's error already names the page.
		}

		var positions []int

		for _, msg := range page.Messages {
			if f.seen(msg) {
				positions = append(positions, msg.Position)
			}
		}

		if len(positions) == 0 {
			continue
		}

		if err := f.inbox.Delete(ctx, index, positions); err != nil {
			return err //nolint:wrapcheck // the adapter's error already names the page.
		}
	}

	return nil
}
