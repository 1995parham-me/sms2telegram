package mifi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/infra/mifi"
)

// modem fakes the two send endpoints; results are the messageback answers in order.
func modem(t *testing.T, queued string, results ...string) (*mifi.Client, *atomic.Value) {
	t.Helper()

	var submitted atomic.Value

	polls := 0
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/PostMessageList":
			submitted.Store(r.URL.Query().Get("MessageList"))
			_, _ = w.Write([]byte(`{"state":` + queued + `}`))
		case "/messageback":
			result := results[min(polls, len(results)-1)]
			polls++
			_, _ = w.Write([]byte(`{"state":` + result + `}`))
		default:
			http.NotFound(w, r)
		}
	}))

	// The in-memory network routes every request to srv, whatever the host.
	c, err := mifi.NewWithClient("http://192.168.0.1", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	return c, &submitted
}

func TestSendWaitsPastEarlyFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		// The DWR-910M answers "0" right after a submit, then "1" once it is out.
		c, submitted := modem(t, "1", "0", `"1"`)

		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()

		if err := c.Send(ctx, "+989121234567", "سلام <3"); err != nil {
			t.Fatal(err)
		}

		raw, _ := submitted.Load().(string)

		var msg map[string]string
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatal(err)
		}

		if msg["pnumber"] != "+989121234567" || msg["content"] != "سلام <3" {
			t.Fatalf("unexpected submit: %v", msg)
		}
	})
}

func TestSendTimesOut(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c, _ := modem(t, "1", "0")

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		if err := c.Send(ctx, "+989121234567", "hi"); !errors.Is(err, mifi.ErrNotSent) {
			t.Fatalf("got %v, want ErrNotSent", err)
		}
	})
}

func TestSendRejected(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c, _ := modem(t, "0", "1")

		if err := c.Send(t.Context(), "nope", "hi"); !errors.Is(err, mifi.ErrRejected) {
			t.Fatalf("got %v, want ErrRejected", err)
		}
	})
}
