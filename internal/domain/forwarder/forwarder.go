// Package forwarder moves new messages from an inbox to a notifier exactly once.
package forwarder

import (
	"context"
	"fmt"
	"slices"

	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

// Forwarder delivers every message it has not delivered before.
type Forwarder struct {
	inbox    sms.Inbox
	notifier sms.Notifier
	store    sms.Store
}

// New builds a Forwarder over the given ports.
func New(inbox sms.Inbox, notifier sms.Notifier, store sms.Store) *Forwarder {
	return &Forwarder{inbox: inbox, notifier: notifier, store: store}
}

// Forward runs one pass and returns how many messages it delivered.
//
// Messages go out oldest first and each is marked seen only after it is
// delivered, so a failed send stops the pass and the same message is retried
// on the next one: nothing is lost, nothing is reordered.
func (f *Forwarder) Forward(ctx context.Context) (int, error) {
	msgs, err := f.inbox.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading inbox: %w", err)
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
