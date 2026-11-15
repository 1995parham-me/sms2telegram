//nolint:exhaustruct_v5 // a filter names only the fields a test is about.
package console_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/console"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
	"github.com/1995parham-me/sms2telegram/internal/infra/archive"
)

var errModem = errors.New("modem did not confirm")

type modem struct {
	sent []string
	err  error
	ussd []string
}

func (m *modem) Send(ctx context.Context, to, text string) error {
	if err := ctx.Err(); err != nil {
		return err //nolint:wrapcheck // the test wants the bare error.
	}

	m.sent = append(m.sent, to+": "+text)

	return m.err
}

func (m *modem) USSD(_ context.Context, code string) (string, error) {
	m.ussd = append(m.ussd, code)

	return "balance 12,500", nil
}

func open(t *testing.T, limit int) (*console.Console, *modem) {
	t.Helper()

	store, err := archive.Open("")
	if err != nil {
		t.Fatal(err)
	}

	tehran, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		t.Fatal(err)
	}

	m := &modem{sent: nil, err: nil, ussd: nil}

	return console.New(store, m, m, nil, console.Options{
		Country: "98", DailyLimit: limit, MaxParts: 2, Timeout: time.Second, Location: tehran,
	}), m
}

func TestCheck(t *testing.T) {
	t.Parallel()

	c, _ := open(t, 5)

	p, err := c.Check("0912 111 2222", "سلام", "ui")
	if err != nil || p.To != "+989121112222" || p.Encoding != sms.UCS2 || p.Parts != 1 || p.Remaining != 5 {
		t.Fatalf("Check = %+v, %v", p, err)
	}

	for name, tc := range map[string]struct {
		to, text string
		want     error
	}{
		"letters":   {"PARSIANBANK", "hi", console.ErrNumber},
		"too short": {"12", "hi", console.ErrNumber},
		"too long":  {"+9891211122223333", "hi", console.ErrNumber},
		"empty":     {"+989121112222", "  \n", console.ErrEmpty},
		"3 parts":   {"+989121112222", strings.Repeat("a", 400), console.ErrTooLong},
	} {
		if _, err := c.Check(tc.to, tc.text, "ui"); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}

	if p, err := c.Check("700", "balance", "ui"); err != nil || p.To != "700" {
		t.Fatalf("a short code must be dialled as it is: %+v, %v", p, err)
	}
}

//nolint:cyclop // two sends, checked from every side.
func TestSendRecordsBothOutcomes(t *testing.T) {
	t.Parallel()

	c, m := open(t, 0)

	var told []string

	c.OnSent(func(source string, err error) { told = append(told, source+":"+errText(err)) })

	sent, err := c.Send(t.Context(), "09121112222", "on my way", "mcp")
	if err != nil || sent.Status != archive.StatusSent || sent.Peer != "+989121112222" || sent.Source != "mcp" {
		t.Fatalf("Send = %+v, %v", sent, err)
	}

	m.err = errModem

	failed, err := c.Send(t.Context(), "09121112222", "again", "ui")
	if !errors.Is(err, errModem) || failed.Status != archive.StatusFailed || failed.Error == "" {
		t.Fatalf("a failed send = %+v, %v", failed, err)
	}

	if got := c.Archive().Messages(archive.Filter{Peer: "+989121112222"}); len(got) != 2 {
		t.Fatalf("history has %d messages, want both attempts", len(got))
	}

	if len(told) != 2 || told[0] != "mcp:" || !strings.HasPrefix(told[1], "ui:modem") {
		t.Fatalf("OnSent saw %v", told)
	}

	if c.Remaining() != -1 {
		t.Fatal("no limit must report -1")
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func TestSendOutlivesTheCaller(t *testing.T) {
	t.Parallel()

	c, m := open(t, 0)

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // the browser tab closed, Alertmanager gave up

	if _, err := c.Send(ctx, "+989121112222", "still goes", "ui"); err != nil || len(m.sent) != 1 {
		t.Fatalf("a cancelled caller stopped the send: %v, %v", err, m.sent)
	}
}

func TestDailyLimitSparesAlerts(t *testing.T) {
	t.Parallel()

	c, m := open(t, 2)

	m.err = errModem // an unconfirmed send may still have gone out: it counts

	_, _ = c.Send(t.Context(), "+989121112222", "one", "ui")

	m.err = nil

	if _, err := c.Send(t.Context(), "+989121112222", "two", "mcp"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Send(t.Context(), "+989121112222", "three", "ui"); !errors.Is(err, console.ErrLimit) {
		t.Fatalf("third send = %v, want the limit", err)
	}

	if _, err := c.Send(t.Context(), "+989124493153", "FIRING", console.SourceAlert); err != nil {
		t.Fatalf("an alert was refused by the limit: %v", err)
	}

	if c.Remaining() != 0 || len(m.sent) != 3 {
		t.Fatalf("remaining %d, modem saw %d", c.Remaining(), len(m.sent))
	}
}

func TestReceived(t *testing.T) {
	t.Parallel()

	c, _ := open(t, 0)

	in := sms.SMS{Sender: "09121112222", Name: "", Date: "2026-11-15 20:05:06", Content: "hello", Position: 0}

	for range 2 {
		if err := c.Received(in); err != nil {
			t.Fatal(err)
		}
	}

	got := c.Archive().Messages(archive.Filter{})
	if len(got) != 1 || got[0].Peer != "+989121112222" || got[0].Direction != archive.In {
		t.Fatalf("history = %+v", got)
	}

	// The modem's clock is in Tehran: 20:05:06 there is 16:35:06 UTC.
	if want := time.Date(2026, 11, 15, 16, 35, 6, 0, time.UTC); !got[0].Time.Equal(want) {
		t.Fatalf("time = %v, want %v", got[0].Time.UTC(), want)
	}

	if peer := c.Peer("PARSIANBANK"); peer != "parsianbank" {
		t.Fatalf("a named sender's peer = %q", peer)
	}
}

func TestUSSD(t *testing.T) {
	t.Parallel()

	c, m := open(t, 0)

	if answer, err := c.RunUSSD(t.Context(), " *555# "); err != nil || answer == "" || m.ussd[0] != "*555#" {
		t.Fatalf("RunUSSD = %q, %v", answer, err)
	}

	for _, code := range []string{"", "*555#; reboot", "ATD123"} {
		if _, err := c.RunUSSD(t.Context(), code); !errors.Is(err, console.ErrUSSDCode) {
			t.Errorf("code %q: %v", code, err)
		}
	}
}

func TestWait(t *testing.T) {
	t.Parallel()

	c, _ := open(t, 0)

	go func() {
		time.Sleep(20 * time.Millisecond)

		_, _ = c.Send(context.Background(), "+989121112222", "code please", "mcp")
		_ = c.Received(sms.SMS{Sender: "BANK", Name: "", Date: "2026-11-15 20:05:06", Content: "noise", Position: 0})
		_ = c.Received(sms.SMS{Sender: "SNAPP", Name: "", Date: "2026-11-15 20:05:07", Content: "Code 7719", Position: 0})
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	got, err := c.Wait(ctx, func(m archive.Message) bool { return strings.Contains(m.Content, "7719") })
	if err != nil || got.Number != "SNAPP" {
		t.Fatalf("Wait = %+v, %v", got, err)
	}

	short, stop := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer stop()

	if _, err := c.Wait(short, func(archive.Message) bool { return true }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a wait with nothing arriving = %v", err)
	}
}
