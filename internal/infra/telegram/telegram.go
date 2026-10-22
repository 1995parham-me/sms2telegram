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
}

// New returns a bot that posts to chatID. The client honours HTTP_PROXY and
// HTTPS_PROXY, for hosts where api.telegram.org is filtered.
func New(apiURL, token, chatID string, timeout time.Duration) *Bot {
	return &Bot{
		endpoint: strings.TrimRight(apiURL, "/") + "/bot" + token + "/sendMessage",
		chatID:   chatID,
		http:     &http.Client{Timeout: timeout}, //nolint:exhaustruct_v5 // only the set fields matter.
	}
}

// Format renders msg as Telegram HTML.
func Format(msg sms.SMS) string {
	sender := msg.Sender
	if sender == "" {
		sender = "unknown sender"
	}

	content := msg.Content
	if utf8.RuneCountInString(content) > maxContent {
		content = string([]rune(content)[:maxContent]) + "…"
	}

	return fmt.Sprintf("📩 <b>%s</b>\n<i>%s</i>\n\n%s",
		html.EscapeString(sender), html.EscapeString(msg.Date), html.EscapeString(content))
}

type request struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

type response struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Notify implements sms.Notifier.
func (b *Bot) Notify(ctx context.Context, msg sms.SMS) error {
	payload, err := json.Marshal(request{ChatID: b.chatID, Text: Format(msg), ParseMode: "HTML"})
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
		return fmt.Errorf("%w: %s: %s", ErrAPI, resp.Status, r.Description)
	}

	return nil
}

type redactedError struct{ msg string }

func (e redactedError) Error() string { return e.msg }

func redact(err error, endpoint string) error {
	return redactedError{msg: strings.ReplaceAll(err.Error(), endpoint, "https://api.telegram.org/bot<token>/sendMessage")}
}
