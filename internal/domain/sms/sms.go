// Package sms holds the message model and the ports the forwarder talks to.
package sms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// partMissing is what Unisoc MiFi firmware shows in place of a part of a
// multipart SMS that has not arrived yet. The message is listed as soon as
// its first part lands and is completed in place when the rest follow.
const partMissing = "(......)"

// SMS is one message as read from the modem inbox.
type SMS struct {
	// Sender is the originating address. Operator broadcasts often have none.
	Sender string `json:"sender"`
	// Date is the receive time exactly as the modem reports it.
	Date string `json:"date"`
	// Content is the decoded message text.
	Content string `json:"content"`
	// Position is where the modem holds the message right now, 0 being the
	// newest. It changes with every arrival, so it only addresses a delete
	// made straight after the read.
	Position int `json:"-"`
}

// Key identifies a message across polls. Modems number their inbox by
// position, so the index shifts whenever a message arrives or is deleted;
// the receive time and the content are the only stable identity.
//
// The sender is left out on purpose. The DWR-910M reports a different
// sender for the same stored message over time (a bank SMS listed as from
// "+9810000010111001" came back as "PARSIANBANK" after the dongle
// rebooted), and every such change used to forward the message again.
func (s SMS) Key() string {
	return hash(s.Date, s.Content)
}

// LegacyKey is the key releases before 0.4.0 stored, sender included, so a
// state file written by them still marks its messages as delivered.
func (s SMS) LegacyKey() string {
	return hash(s.Sender, s.Date, s.Content)
}

// Incomplete reports a multipart message that is still missing a part.
func (s SMS) Incomplete() bool {
	return strings.Contains(s.Content, partMissing)
}

func hash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Page is one page of the inbox.
type Page struct {
	// Messages are newest first.
	Messages []SMS
	// Total is how many pages the inbox has.
	Total int
	// Records is how many messages the inbox holds. The DWR-910M stops
	// storing new SMS, silently, once it holds about 94.
	Records int
}

// Inbox reads and deletes the messages stored on a modem.
type Inbox interface {
	// Page returns page index of the inbox, counting from 1, newest first.
	Page(ctx context.Context, index int) (Page, error)
	// Delete removes the messages at positions, all read from page index.
	Delete(ctx context.Context, index int, positions []int) error
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
