// Package telegram delivers messages through the Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

// ErrAPI reports a request the Bot API refused.
var ErrAPI = errors.New("telegram refused the request")

// maxContent keeps a message under Telegram's 4096-character limit once the
// header and HTML escaping are added.
const maxContent = 3500

// Bot sends to one chat.
type Bot struct {
	endpoint string
	chatID   string
	http     *http.Client
	names    func(sender string) string
}

// New returns a bot that posts to chatID. The client honours HTTP_PROXY and
// HTTPS_PROXY, for hosts where api.telegram.org is filtered.
func New(apiURL, token, chatID string, timeout time.Duration) *Bot {
	return &Bot{
		endpoint: strings.TrimRight(apiURL, "/") + "/bot" + token + "/sendMessage",
		chatID:   chatID,
		http:     &http.Client{Timeout: timeout}, //nolint:exhaustruct_v5 // only the set fields matter.
		names:    nil,
	}
}

// SetNames names senders with names, which returns "" for an unknown number.
func (b *Bot) SetNames(names func(sender string) string) { b.names = names }

// Format renders msg as Telegram HTML.
func Format(msg sms.SMS) string {
	return fmt.Sprintf("📩 <b>%s</b>\n<i>%s</i>\n\n%s",
		html.EscapeString(senderOf(msg)), html.EscapeString(msg.Date), html.EscapeString(truncate(msg.Content)))
}

func senderOf(msg sms.SMS) string {
	switch {
	case msg.Sender == "":
		return "unknown sender"
	case msg.Name != "":
		// The number stays, so a wrong match is visible.
		return msg.Name + " (" + msg.Sender + ")"
	default:
		return msg.Sender
	}
}

func truncate(content string) string {
	if utf8.RuneCountInString(content) > maxContent {
		return string([]rune(content)[:maxContent]) + "…"
	}

	return content
}

type request struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

type response struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Notify implements sms.Notifier.
//
// A message Telegram refuses outright (400) would be retried on every pass
// and hold back every SMS behind it, so it is sent again as plain text, and
// failing that with its text escaped to ASCII: degraded, but delivered.
func (b *Bot) Notify(ctx context.Context, msg sms.SMS) error {
	if b.names != nil && msg.Name == "" && msg.Sender != "" {
		msg.Name = b.names(msg.Sender)
	}

	err := b.send(ctx, Format(msg), "HTML")
	if !badRequest(err) {
		return err
	}

	err = b.send(ctx, Plain(msg), "")
	if !badRequest(err) {
		return err
	}

	return b.send(ctx, Escaped(msg), "")
}

// Plain renders msg without markup.
func Plain(msg sms.SMS) string {
	return fmt.Sprintf("📩 %s\n%s\n\n%s", senderOf(msg), msg.Date, truncate(msg.Content))
}

// Escaped renders msg in ASCII, every other character as a Go escape.
func Escaped(msg sms.SMS) string {
	return fmt.Sprintf("📩 %+q\n%+q\n\n%+q", senderOf(msg), msg.Date, truncate(msg.Content))
}

func badRequest(err error) bool {
	var api apiError

	return errors.As(err, &api) && api.status == http.StatusBadRequest
}

// apiError is a request the Bot API answered with ok=false.
type apiError struct {
	status int
	text   string
}

func (e apiError) Error() string { return fmt.Sprintf("%v: %s", ErrAPI, e.text) }

func (e apiError) Unwrap() error { return ErrAPI }

// Text sends a plain-text message, such as an alert about the modem.
func (b *Bot) Text(ctx context.Context, text string) error {
	return b.send(ctx, text, "")
}

// send posts text; an empty mode sends it without markup.
func (b *Bot) send(ctx context.Context, text, mode string) error {
	payload, err := json.Marshal(request{ChatID: b.chatID, Text: text, ParseMode: mode})
	if err != nil {
		return fmt.Errorf("encoding telegram request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building telegram request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		// The URL embeds the token; never let it reach the logs.
		return fmt.Errorf("calling telegram: %w", redact(err, b.endpoint))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading telegram response: %w", err)
	}

	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("telegram answered %s: %w", resp.Status, err)
	}

	if !r.OK {
		return apiError{status: resp.StatusCode, text: resp.Status + ": " + r.Description}
	}

	return nil
}

type redactedError struct{ msg string }

func (e redactedError) Error() string { return e.msg }

func redact(err error, endpoint string) error {
	return redactedError{msg: strings.ReplaceAll(err.Error(), endpoint, "https://api.telegram.org/bot<token>/sendMessage")}
}
