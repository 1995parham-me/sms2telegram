package mifi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/infra/mifi"
)

// ussdModem fakes the two USSD endpoints; answers are the ussdback answers in
// order, the first being what the slot holds before the code is sent.
func ussdModem(t *testing.T, accepted string, answers ...string) (*mifi.Client, *string) {
	t.Helper()

	code, polls := "", 0

	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ussd":
			code = r.URL.Query().Get("ussdparam")
			_, _ = w.Write([]byte(`{"state":` + accepted + `}`))
		case "/ussdback":
			answer := answers[min(polls, len(answers)-1)]
			polls++
			_, _ = w.Write([]byte(`{"state":` + answer + `}`))
		default:
			http.NotFound(w, r)
		}
	}))

	c, err := mifi.NewWithClient("http://192.168.0.1", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	return c, &code
}

func TestUSSDReturnsTheDecodedAnswer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c, code := ussdModem(t, "1", `""`, `""`, `"Welcome\n1. Account ۱"`)

		answer, err := c.USSD(t.Context(), "*555#")
		if err != nil || answer != "Welcome\n1. Account ۱" {
			t.Fatalf("USSD = %q, %v", answer, err)
		}

		if *code != `{"ussdnumber":"*555#"}` {
			t.Fatalf("the modem got %q", *code)
		}
	})
}

// The answer slot keeps the previous answer for a moment after a new code.
func TestUSSDSkipsTheAnswerLeftOver(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c, _ := ussdModem(t, "1", `"old menu"`, `"old menu"`, `"new menu"`)

		if answer, err := c.USSD(t.Context(), "1"); err != nil || answer != "new menu" {
			t.Fatalf("USSD = %q, %v", answer, err)
		}
	})
}

// The same code asked twice answers the same: that is not a leftover forever.
func TestUSSDAcceptsARepeatedAnswer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c, _ := ussdModem(t, "1", `"menu"`)

		if answer, err := c.USSD(t.Context(), "*555#"); err != nil || answer != "menu" {
			t.Fatalf("USSD = %q, %v", answer, err)
		}
	})
}

func TestUSSDRejectedAndUnanswered(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		c, _ := ussdModem(t, "2", `""`)

		if _, err := c.USSD(t.Context(), "*1#"); !errors.Is(err, mifi.ErrRejected) {
			t.Fatalf("a rejected code = %v", err)
		}

		c, _ = ussdModem(t, "1", `""`)

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		if _, err := c.USSD(ctx, "*555#"); !errors.Is(err, mifi.ErrNoAnswer) {
			t.Fatalf("an unanswered code = %v", err)
		}
	})
}
