// Package sms holds the message model and the ports the forwarder talks to.
package sms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// SMS is one message as read from the modem inbox.
type SMS struct {
	// Sender is the originating address. Operator broadcasts often have none.
	Sender string `json:"sender"`
	// Date is the receive time exactly as the modem reports it.
	Date string `json:"date"`
	// Content is the decoded message text.
	Content string `json:"content"`
}

// Key identifies a message across polls. Modems number their inbox by
// position, so the index shifts whenever a message arrives or is deleted;
// the content itself is the only stable identity.
func (s SMS) Key() string {
	h := sha256.New()
	for _, part := range []string{s.Sender, s.Date, s.Content} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Inbox reads the messages stored on a modem.
type Inbox interface {
	// List returns every message in the inbox, in any order.
	List(ctx context.Context) ([]SMS, error)
}

// Notifier delivers a message to its destination.
type Notifier interface {
	Notify(ctx context.Context, msg SMS) error
}

// Store remembers which messages have already been delivered.
type Store interface {
	Seen(key string) bool
	MarkSeen(key string) error
}

// Outbox sends messages from the modem's SIM.
type Outbox interface {
	// Send submits text to the number to and waits until the modem reports
	// whether it went out.
	Send(ctx context.Context, to, text string) error
}
