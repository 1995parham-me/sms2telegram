package mifi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"
)

// staleAfter is how many polls an answer equal to the one already on the
// modem is treated as left over from the previous code. After that it is
// taken as the new answer: the same menu, asked for twice, answers the same.
const staleAfter = 3

// ErrNoAnswer reports a USSD code the network did not answer before the deadline.
var ErrNoAnswer = errors.New("the network did not answer the USSD code")

// USSD sends code (such as "*555#", or a single digit to answer the menu of
// the session that is open) and returns the network's answer. It waits until
// ctx is done, so callers should give ctx a deadline.
func (c *Client) USSD(ctx context.Context, code string) (string, error) {
	c.ussd.Lock()
	defer c.ussd.Unlock()

	// The answer slot keeps the previous answer until the new one lands.
	before, _ := c.state(ctx, "ussdback", nil)

	payload, err := json.Marshal(struct {
		Number string `json:"ussdnumber"`
	}{Number: code})
	if err != nil {
		return "", fmt.Errorf("encoding USSD code: %w", err)
	}

	accepted, err := c.state(ctx, "ussd", url.Values{"ussdparam": {string(payload)}})
	if err != nil {
		return "", fmt.Errorf("submitting USSD code: %w", err)
	}

	if accepted != "1" {
		return "", fmt.Errorf("%w (state %q)", ErrRejected, accepted)
	}

	return c.answer(ctx, before)
}

// answer polls the answer slot until it holds something other than before,
// the answer it held when the code was sent.
func (c *Client) answer(ctx context.Context, before string) (string, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for polls := 1; ; polls++ {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("%w: %w", ErrNoAnswer, context.Cause(ctx))
		case <-ticker.C:
		}

		answer, err := c.state(ctx, "ussdback", nil)
		if err != nil || answer == "" {
			continue
		}

		if answer != before || polls >= staleAfter {
			return answer, nil
		}
	}
}
