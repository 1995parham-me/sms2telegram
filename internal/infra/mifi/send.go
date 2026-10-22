package mifi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// pollInterval matches the web UI, which checks the send result every 3 s.
const pollInterval = 3 * time.Second

var (
	// ErrRejected reports a message the modem refused to queue, usually a malformed number.
	ErrRejected = errors.New("modem rejected the message")
	// ErrNotSent reports a message the modem never confirmed as sent before the deadline.
	ErrNotSent = errors.New("modem did not confirm the message as sent")
)

// Send implements sms.Outbox. It waits for the modem's confirmation until ctx
// is done, so callers should give ctx a deadline.
//
// The modem reports "0" (failed) both for a real failure and for a send it
// has not finished yet, so only "1" ends the wait early.
func (c *Client) Send(ctx context.Context, to, text string) error {
	payload, err := json.Marshal(struct {
		Number  string `json:"pnumber"`
		Content string `json:"content"`
	}{Number: to, Content: text})
	if err != nil {
		return fmt.Errorf("encoding message: %w", err)
	}

	queued, err := c.state(ctx, "PostMessageList", url.Values{"MessageList": {string(payload)}})
	if err != nil {
		return fmt.Errorf("submitting message: %w", err)
	}

	if queued != "1" {
		return fmt.Errorf("%w (state %q)", ErrRejected, queued)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	last := ""

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last state %q): %w", ErrNotSent, last, context.Cause(ctx))
		case <-ticker.C:
		}

		sent, err := c.state(ctx, "messageback", nil)
		if err != nil {
			// A dropped poll is not a failed send; the next tick asks again.
			continue
		}

		if sent == "1" {
			return nil
		}

		last = sent
	}
}

// state calls an action endpoint and returns its "state" field, which the
// firmware answers as a number or a string depending on the endpoint.
func (c *Client) state(ctx context.Context, endpoint string, query url.Values) (string, error) {
	u := c.base.JoinPath(endpoint)
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("building %s request: %w", endpoint, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", endpoint, err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("calling %s: %w: %s", endpoint, ErrStatus, resp.Status)
	}

	var r struct {
		State json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("decoding %s: %w", endpoint, err)
	}

	return strings.Trim(string(r.State), `"`), nil
}
