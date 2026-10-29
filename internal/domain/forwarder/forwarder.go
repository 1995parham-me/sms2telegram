// Package forwarder moves new messages from an inbox to a notifier exactly once.
package forwarder

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

// ErrInbox marks a pass that failed because the modem could not be read, as
// opposed to a failed delivery.
var ErrInbox = errors.New("reading inbox")

// Forwarder delivers every message it has not delivered before.
type Forwarder struct {
	inbox    sms.Inbox
	notifier sms.Notifier
	store    sms.Store
	prune    bool
}

// New builds a Forwarder over the given ports. With prune, every pass that
// delivers everything also deletes the delivered messages beyond the first
// page, so the inbox never fills up.
func New(inbox sms.Inbox, notifier sms.Notifier, store sms.Store, prune bool) *Forwarder {
	return &Forwarder{inbox: inbox, notifier: notifier, store: store, prune: prune}
}

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

// deliver sends msgs in order, recording each one as soon as it is delivered.
func (f *Forwarder) deliver(ctx context.Context, msgs []sms.SMS) (int, error) {
	sent := 0

	for _, msg := range msgs {
		key := msg.Key()
		if f.store.Seen(key) {
			continue
		}

		if err := f.notifier.Notify(ctx, msg); err != nil {
			return sent, fmt.Errorf("delivering message from %q at %s: %w", msg.Sender, msg.Date, err)
		}

		if err := f.store.MarkSeen(key); err != nil {
			return sent, fmt.Errorf("recording delivery: %w", err)
		}

		sent++
	}

	return sent, nil
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
		}

		before := len(out)

		for _, msg := range page.Messages {
			if !f.store.Seen(msg.Key()) {
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
			if f.store.Seen(msg.Key()) {
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
