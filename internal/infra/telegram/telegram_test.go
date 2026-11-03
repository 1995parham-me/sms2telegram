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

const htmlMode = "HTML"

func TestFormatEscapes(t *testing.T) {
	t.Parallel()

	got := telegram.Format(sms.SMS{Sender: "", Name: "", Date: "2026-10-22 12:27:20", Content: "a <b> & c", Position: 0})

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

	msg := sms.SMS{Sender: "98100", Name: "", Date: "d", Content: "hi", Position: 0}
	if err := bot.Notify(t.Context(), msg); err != nil {
		t.Fatal(err)
	}

	if got["chat_id"] != "42" || got["parse_mode"] != htmlMode || !strings.Contains(got["text"], "hi") {
		t.Fatalf("unexpected request: %v", got)
	}
}

func TestNotifyNamesTheSender(t *testing.T) {
	t.Parallel()

	var got map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	bot := telegram.New(srv.URL, "TOKEN", "42", time.Second)
	bot.SetNames(func(sender string) string {
		if sender == "+989121112222" {
			return "Ali <A>"
		}

		return ""
	})

	msg := sms.SMS{Sender: "+989121112222", Name: "", Date: "d", Content: "hi", Position: 0}
	if err := bot.Notify(t.Context(), msg); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got["text"], "Ali &lt;A&gt; (+989121112222)") {
		t.Fatalf("sender not named: %q", got["text"])
	}

	msg = sms.SMS{Sender: "PARSIANBANK", Name: "", Date: "d", Content: "hi", Position: 0}
	if err := bot.Notify(t.Context(), msg); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got["text"], "<b>PARSIANBANK</b>") {
		t.Fatalf("unknown sender changed: %q", got["text"])
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

	err := bot.Notify(t.Context(), sms.SMS{Sender: "", Name: "", Date: "", Content: "", Position: 0})
	if !errors.Is(err, telegram.ErrAPI) || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("got %v, want the API description", err)
	}
}

// A message Telegram cannot parse as HTML still gets through, as plain text,
// instead of holding back every SMS behind it.
func TestNotifyFallsBackToPlainText(t *testing.T) {
	t.Parallel()

	var modes []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]string

		_ = json.NewDecoder(r.Body).Decode(&got)
		modes = append(modes, got["parse_mode"])

		if got["parse_mode"] == htmlMode {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: can't parse entities"}`))

			return
		}

		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	bot := telegram.New(srv.URL, "TOKEN", "42", time.Second)

	msg := sms.SMS{Sender: "98100", Name: "", Date: "d", Content: "hi", Position: 0}
	if err := bot.Notify(t.Context(), msg); err != nil {
		t.Fatal(err)
	}

	if len(modes) != 2 || modes[0] != htmlMode || modes[1] != "" {
		t.Fatalf("parse modes tried: %q, want HTML then plain", modes)
	}
}
