package webhook_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/infra/webhook"
)

var errModem = errors.New("modem did not confirm")

type outbox struct {
	mu       sync.Mutex
	sent     []string
	fail     bool
	inFlight int
	maxSeen  int
}

func (o *outbox) Send(_ context.Context, to, text string) error {
	o.mu.Lock()
	o.inFlight++
	o.maxSeen = max(o.maxSeen, o.inFlight)
	o.mu.Unlock()

	time.Sleep(5 * time.Millisecond)

	o.mu.Lock()
	defer o.mu.Unlock()

	o.inFlight--

	if o.fail {
		return errModem
	}

	o.sent = append(o.sent, to+": "+text)

	return nil
}

func newOutbox(fail bool) *outbox {
	return &outbox{mu: sync.Mutex{}, sent: nil, fail: fail, inFlight: 0, maxSeen: 0}
}

const body = `{"status":"firing","commonLabels":{"alertname":"JoghdDown"},
"alerts":[{"status":"firing","labels":{"alertname":"JoghdDown"},"annotations":{"summary":"Joghd is not running"}}]}`

func post(t *testing.T, h http.Handler, token, payload string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, webhook.Path, strings.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func TestSendsToEveryNumber(t *testing.T) {
	t.Parallel()

	o := newOutbox(false)
	h := webhook.New(o, []string{"+989121111111", "+989122222222"}, "s3cret", time.Second)

	if rec := post(t, h, "s3cret", body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body)
	}

	want := []string{
		"+989121111111: FIRING JoghdDown: Joghd is not running",
		"+989122222222: FIRING JoghdDown: Joghd is not running",
	}
	if strings.Join(o.sent, "|") != strings.Join(want, "|") {
		t.Errorf("sent %q, want %q", o.sent, want)
	}
}

func TestRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		token   string
		payload string
		want    int
	}{
		{name: "no token", token: "", payload: body, want: http.StatusUnauthorized},
		{name: "wrong token", token: "guess", payload: body, want: http.StatusUnauthorized},
		{name: "not JSON", token: "s3cret", payload: "{", want: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			o := newOutbox(false)
			h := webhook.New(o, []string{"+989121111111"}, "s3cret", time.Second)

			if rec := post(t, h, tc.token, tc.payload); rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}

			if len(o.sent) != 0 {
				t.Errorf("sent %q, want nothing", o.sent)
			}
		})
	}
}

func TestFailedSendAsksForARetry(t *testing.T) {
	t.Parallel()

	h := webhook.New(newOutbox(true), []string{"+989121111111"}, "s3cret", time.Second)

	if rec := post(t, h, "s3cret", body); rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d so Alertmanager retries", rec.Code, http.StatusBadGateway)
	}
}

func TestSendsOneAtATime(t *testing.T) {
	t.Parallel()

	o := newOutbox(false)
	h := webhook.New(o, []string{"+989121111111"}, "s3cret", time.Second)

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { post(t, h, "s3cret", body) })
	}

	wg.Wait()

	if o.maxSeen != 1 || len(o.sent) != 5 {
		t.Errorf("%d sends, at most %d at once; want 5, one at a time", len(o.sent), o.maxSeen)
	}
}
