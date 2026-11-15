// Package console is what a person at the web UI or an assistant over MCP can
// do with the SIM: read the message history, send an SMS, run a USSD code and
// see how the modem is doing. Both front ends go through it, so the limits
// and the history are the same whichever one is used.
package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/contacts"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
	"github.com/1995parham-me/sms2telegram/internal/infra/archive"
)

// SourceAlert marks the SMS the Alertmanager webhook sends. They are exempt
// from the daily limit: an alert must not be refused because a person or an
// assistant used the day's allowance.
const SourceAlert = "alertmanager"

// DateLayout is how the modem writes a message's receive time, in its own
// time zone.
const DateLayout = "2006-01-02 15:04:05"

const (
	day = 24 * time.Hour
	// Phone numbers have at most 15 digits (E.164); the shortest short codes have 3.
	minDigits, maxDigits = 3, 15
	idBytes              = 12
	maxUSSD              = 64
)

// What a request can be refused for.
var (
	ErrNumber   = errors.New("not a phone number")
	ErrEmpty    = errors.New("the message is empty")
	ErrTooLong  = errors.New("the message is too long")
	ErrLimit    = errors.New("the daily sending limit is reached")
	ErrUSSDCode = errors.New("a USSD code has only digits, * and #")
	ErrNoUSSD   = errors.New("this modem has no USSD")
)

// USSD runs USSD codes on the modem.
type USSD interface {
	USSD(ctx context.Context, code string) (string, error)
}

// Names is the address book, as far as the console needs it.
type Names interface {
	Name(number string) string
	Search(query string, limit int) []contacts.Entry
}

// Options are the console's settings.
type Options struct {
	// Country is the calling code a national number (0912...) is read in.
	Country string
	// DailyLimit is how many SMS people and assistants may send in any 24
	// hours; 0 is no limit.
	DailyLimit int
	// MaxParts is the longest message accepted, in SMS parts; 0 is no limit.
	MaxParts int
	// Timeout bounds one send or one USSD exchange.
	Timeout time.Duration
	// Location is the modem's time zone, which its message dates are in.
	Location *time.Location
}

// Console ties the archive, the modem and the address book together.
type Console struct {
	archive *archive.Store
	outbox  sms.Outbox
	ussd    USSD
	names   Names
	opts    Options
	status  func() Status
	now     func() time.Time
	// sent is told about every send, for the metrics.
	sent func(source string, err error)
}

// New returns a console. ussd and names may be nil.
func New(store *archive.Store, outbox sms.Outbox, ussd USSD, names Names, opts Options) *Console {
	if opts.Location == nil {
		opts.Location = time.Local //nolint:gosmopolitan // the host's zone is the documented default.
	}

	return &Console{
		archive: store,
		outbox:  outbox,
		ussd:    ussd,
		names:   names,
		opts:    opts,
		status:  func() Status { return Status{} }, //nolint:exhaustruct_v5 // nothing is known yet.
		now:     time.Now,
		sent:    func(string, error) {},
	}
}

// SetStatus installs the source of the modem's status.
func (c *Console) SetStatus(fn func() Status) { c.status = fn }

// OnSent installs fn to be told about every send.
func (c *Console) OnSent(fn func(source string, err error)) { c.sent = fn }

// Archive is the message history.
func (c *Console) Archive() *archive.Store { return c.archive }

// Peer is the conversation a number belongs to: the number in E.164 where it
// has one, the sender's text in lower case otherwise.
func (c *Console) Peer(number string) string {
	if n := contacts.Normalize(number, c.opts.Country); n != "" {
		return n
	}

	if s := strings.ToLower(strings.TrimSpace(number)); s != "" {
		return s
	}

	return "unknown"
}

// Name is the address book's name for a number, or "".
func (c *Console) Name(number string) string {
	if c.names == nil {
		return ""
	}

	return c.names.Name(number)
}

// Contacts searches the address book by name or number.
func (c *Console) Contacts(query string, limit int) []contacts.Entry {
	if c.names == nil {
		return nil
	}

	return c.names.Search(query, limit)
}

// Received stores a delivered incoming message in the history. A message
// already there is left alone.
func (c *Console) Received(msg sms.SMS) error {
	id := msg.Key()
	if c.archive.Has(id) {
		return nil
	}

	at, err := time.ParseInLocation(DateLayout, msg.Date, c.opts.Location)
	if err != nil {
		at = c.now()
	}

	_, err = c.archive.Add(archive.Message{
		ID:        id,
		Direction: archive.In,
		Peer:      c.Peer(msg.Sender),
		Number:    msg.Sender,
		Time:      at,
		Content:   msg.Content,
		Source:    "",
		Status:    "",
		Error:     "",
	})
	if err != nil {
		return fmt.Errorf("archiving message: %w", err)
	}

	return nil
}

// Preview is what sending a message would do, before it is sent.
type Preview struct {
	sms.Size

	// To is the number that will be dialled.
	To string `json:"to"`
	// Name is the address book's name for it, when it has one.
	Name string `json:"name,omitempty"`
	Text string `json:"text"`
	// Remaining is how many more SMS the daily limit allows, -1 without a limit.
	Remaining int `json:"remaining"`
}

// Check validates a message without sending it.
func (c *Console) Check(to, text, source string) (Preview, error) {
	number := contacts.Normalize(to, c.opts.Country)
	if digits := strings.TrimPrefix(number, "+"); len(digits) < minDigits || len(digits) > maxDigits {
		return Preview{}, fmt.Errorf("%w: %q", ErrNumber, to)
	}

	if strings.TrimSpace(text) == "" {
		return Preview{}, ErrEmpty
	}

	size := sms.Measure(text)
	if c.opts.MaxParts > 0 && size.Parts > c.opts.MaxParts {
		return Preview{}, fmt.Errorf("%w: %d parts in %s, at most %d are allowed",
			ErrTooLong, size.Parts, size.Encoding, c.opts.MaxParts)
	}

	remaining := c.Remaining()
	if source != SourceAlert && remaining == 0 {
		return Preview{}, fmt.Errorf("%w (%d in 24 hours)", ErrLimit, c.opts.DailyLimit)
	}

	return Preview{Size: size, To: number, Name: c.Name(number), Text: text, Remaining: remaining}, nil
}

// Remaining is how many more SMS the daily limit allows now, -1 without a limit.
func (c *Console) Remaining() int {
	if c.opts.DailyLimit <= 0 {
		return -1
	}

	used := 0

	for _, m := range c.archive.Messages(archive.Filter{ //nolint:exhaustruct_v5 // the other fields select everything.
		Direction: archive.Out,
		After:     c.now().Add(-day),
	}) {
		// A failed send counts too: the modem reports a send it has not
		// confirmed as failed, and it may still have gone out.
		if m.Source != SourceAlert {
			used++
		}
	}

	return max(c.opts.DailyLimit-used, 0)
}

// Send texts to and records the outcome in the history. source says who is
// sending: "ui", "mcp" or SourceAlert.
//
// The send does not stop when ctx is cancelled: the modem has the message by
// then, and a caller that gives up (a browser tab closed, Alertmanager timing
// out) must not turn a sent message into a "failed" one that is sent again.
func (c *Console) Send(ctx context.Context, to, text, source string) (archive.Message, error) {
	preview, err := c.Check(to, text, source)
	if err != nil {
		return archive.Message{}, err
	}

	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.opts.Timeout)
	defer cancel()

	sendErr := c.outbox.Send(sendCtx, preview.To, text)
	c.sent(source, sendErr)

	msg := archive.Message{
		ID:        newID(),
		Direction: archive.Out,
		Peer:      c.Peer(preview.To),
		Number:    preview.To,
		Time:      c.now(),
		Content:   text,
		Source:    source,
		Status:    archive.StatusSent,
		Error:     "",
	}

	if sendErr != nil {
		msg.Status = archive.StatusFailed
		msg.Error = sendErr.Error()
	}

	if _, err := c.archive.Add(msg); err != nil && sendErr == nil {
		return msg, fmt.Errorf("sent, but not recorded: %w", err)
	}

	if sendErr != nil {
		return msg, fmt.Errorf("sending to %s: %w", preview.To, sendErr)
	}

	return msg, nil
}

// RunUSSD sends a USSD code, or a menu answer, and returns the network's reply.
func (c *Console) RunUSSD(ctx context.Context, code string) (string, error) {
	if c.ussd == nil {
		return "", ErrNoUSSD
	}

	code = strings.TrimSpace(code)
	if code == "" || len(code) > maxUSSD || strings.Trim(code, "0123456789*#") != "" {
		return "", fmt.Errorf("%w: %q", ErrUSSDCode, code)
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.opts.Timeout)
	defer cancel()

	answer, err := c.ussd.USSD(ctx, code)
	if err != nil {
		return "", fmt.Errorf("running %s: %w", code, err)
	}

	return answer, nil
}

// Wait returns the first incoming message that match accepts and that arrives
// after the call, or an error once ctx is done.
func (c *Console) Wait(ctx context.Context, match func(archive.Message) bool) (archive.Message, error) {
	ch, stop := c.archive.Subscribe()
	defer stop()

	for {
		select {
		case <-ctx.Done():
			return archive.Message{}, fmt.Errorf("waiting for a message: %w", context.Cause(ctx))
		case m := <-ch:
			if m.Direction == archive.In && match(m) {
				return m, nil
			}
		}
	}
}

// Status is the current state of the service.
func (c *Console) Status() Status {
	st := c.status()
	st.Archive = c.archive.Len()
	st.DailyLimit = c.opts.DailyLimit
	st.Remaining = c.Remaining()
	st.USSD = c.ussd != nil

	return st
}

// Status is how the modem and the service are doing.
type Status struct {
	Version string `json:"version"`

	// ModemOK is whether the modem answered on the last pass.
	ModemOK    bool   `json:"modem_ok"`
	SIM        bool   `json:"sim"`
	Registered bool   `json:"registered"`
	Network    string `json:"network"`
	SignalDBm  int    `json:"signal_dbm"`
	// ModemUptime is seconds since the modem last rebooted.
	ModemUptime int64 `json:"modem_uptime_seconds"`

	// InboxRead is when the modem's inbox was last read; Delivered when every
	// message was last delivered to Telegram. Inbox is how many messages the
	// modem holds.
	InboxRead time.Time `json:"inbox_read"`
	Delivered time.Time `json:"delivered"`
	Inbox     int       `json:"inbox"`
	Forwarded int       `json:"forwarded"`

	Heartbeat         bool      `json:"heartbeat"`
	HeartbeatSent     time.Time `json:"heartbeat_sent"`
	HeartbeatReceived time.Time `json:"heartbeat_received"`
	HeartbeatMissing  bool      `json:"heartbeat_missing"`

	// Notices is how many of the service's own alerts wait for Telegram.
	Notices int `json:"notices"`
	// Contacts is how many numbers have a name.
	Contacts int `json:"contacts"`

	Archive    int  `json:"archive"`
	DailyLimit int  `json:"daily_limit"`
	Remaining  int  `json:"remaining"`
	USSD       bool `json:"ussd"`
}

func newID() string {
	b := make([]byte, idBytes)
	_, _ = rand.Read(b)

	return "out-" + hex.EncodeToString(b)
}
