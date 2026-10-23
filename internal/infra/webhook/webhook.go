// Package webhook receives Alertmanager notifications over HTTP and texts each
// one from the modem's SIM, as a fallback channel that needs neither Telegram
// nor the internet: only the LAN and the mobile network.
package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/alert"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

// Path is where Alertmanager posts.
const Path = "/alertmanager"

// maxBody bounds a notification; Alertmanager's are a few KiB.
const maxBody = 1 << 20

// Handler texts every notification it receives to a fixed list of numbers.
type Handler struct {
	outbox  sms.Outbox
	to      []string
	token   string
	timeout time.Duration

	// The modem reports the result of "the last" send, so two sends in
	// flight would read each other's result. One at a time.
	mu sync.Mutex
}

// New returns a handler that sends through outbox to every number in to,
// waiting up to timeout for each. Requests must carry "Authorization: Bearer
// <token>".
func New(outbox sms.Outbox, to []string, token string, timeout time.Duration) *Handler {
	return &Handler{outbox: outbox, to: to, token: token, timeout: timeout, mu: sync.Mutex{}}
}

// ServeHTTP answers 200 once every number was sent the text, and 502 if any
// send failed, so that Alertmanager retries the notification.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)

		return
	}

	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)

		return
	}

	var n alert.Notification
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&n); err != nil {
		http.Error(w, "bad notification: "+err.Error(), http.StatusBadRequest)

		return
	}

	text := alert.Text(n)

	if err := h.send(r.Context(), text); err != nil {
		slog.WarnContext(r.Context(), "alert SMS not sent", "text", text, "error", err)
		http.Error(w, err.Error(), http.StatusBadGateway)

		return
	}

	slog.InfoContext(r.Context(), "alert SMS sent", "text", text, "to", len(h.to))
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

func (h *Handler) send(ctx context.Context, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var errs []error

	for _, to := range h.to {
		ctx, cancel := context.WithTimeout(ctx, h.timeout)
		err := h.outbox.Send(ctx, to, text)

		cancel()

		if err != nil {
			errs = append(errs, fmt.Errorf("sending to %s: %w", to, err))
		}
	}

	return errors.Join(errs...)
}
