package telegram_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
	"github.com/1995parham-me/sms2telegram/internal/infra/telegram"
)

func TestFormatEscapes(t *testing.T) {
	t.Parallel()

	got := telegram.Format(sms.SMS{Sender: "", Date: "2026-10-22 12:27:20", Content: "a <b> & c"})

	if !strings.Contains(got, "unknown sender") || !strings.Contains(got, "a &lt;b&gt; &amp; c") {
		t.Fatalf("unexpected format: %q", got)
	}
}

func TestNotify(t *testing.T) {
	t.Parallel()

	var got map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTOKEN/sendMessage" {
			http.NotFound(w, r)

			return
		}

		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	bot := telegram.New(srv.URL, "TOKEN", "42", time.Second)

	if err := bot.Notify(t.Context(), sms.SMS{Sender: "98100", Date: "d", Content: "hi"}); err != nil {
		t.Fatal(err)
	}

	if got["chat_id"] != "42" || got["parse_mode"] != "HTML" || !strings.Contains(got["text"], "hi") {
		t.Fatalf("unexpected request: %v", got)
	}
}

func TestNotifyReportsAPIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()

	bot := telegram.New(srv.URL, "TOKEN", "42", time.Second)

	err := bot.Notify(t.Context(), sms.SMS{Sender: "", Date: "", Content: ""})
	if !errors.Is(err, telegram.ErrAPI) || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("got %v, want the API description", err)
	}
}
