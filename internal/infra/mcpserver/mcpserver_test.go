package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/1995parham-me/sms2telegram/internal/domain/console"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
	"github.com/1995parham-me/sms2telegram/internal/infra/archive"
	"github.com/1995parham-me/sms2telegram/internal/infra/mcpserver"
)

type modem struct{ sent, ussd []string }

func (m *modem) Send(_ context.Context, to, text string) error {
	m.sent = append(m.sent, to+": "+text)

	return nil
}

func (m *modem) USSD(_ context.Context, code string) (string, error) {
	m.ussd = append(m.ussd, code)

	return "balance 12,500", nil
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)

	return http.DefaultTransport.RoundTrip(r) //nolint:wrapcheck // a test transport.
}

func connect(t *testing.T) (*mcp.ClientSession, *modem, *console.Console, string) {
	t.Helper()

	store, err := archive.Open("")
	if err != nil {
		t.Fatal(err)
	}

	m := &modem{sent: nil, ussd: nil}
	con := console.New(store, m, m, nil, console.Options{
		Country: "98", DailyLimit: 5, MaxParts: 2, Timeout: time.Second, Location: time.UTC,
	})

	token, sum := mcpserver.NewToken()

	srv := httptest.NewServer(mcpserver.Handler(con, map[string]string{"claude": sum}, "test", time.UTC))
	t.Cleanup(srv.Close)

	//nolint:exhaustruct_v5 // the defaults are right for the rest.
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Title: "", Version: "0"}, nil)

	//nolint:exhaustruct_v5 // the endpoint and the client are what matter.
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: &http.Client{Transport: bearer{token: token}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = session.Close() })

	return session, m, con, srv.URL
}

//nolint:ireturn // the caller names the type.
func call[T any](t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (T, string) {
	t.Helper()

	var out T

	//nolint:exhaustruct_v5 // a call is a name and its arguments.
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	var text strings.Builder

	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}

	if res.IsError {
		return out, text.String()
	}

	if err := json.Unmarshal([]byte(text.String()), &out); err != nil {
		t.Fatalf("%s: decoding %q: %v", name, text.String(), err)
	}

	return out, ""
}

func message(to, text string, yes bool) map[string]any {
	return map[string]any{"to": to, "text": text, "yes": yes}
}

func TestNeedsTheToken(t *testing.T) {
	t.Parallel()

	_, m, _, url := connect(t)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_sms",`+
			`"arguments":{"to":"+989121112222","text":"x","yes":true}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sms_wrong")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized || len(m.sent) != 0 {
		t.Fatalf("a wrong token = %d, modem saw %v", resp.StatusCode, m.sent)
	}
}

//nolint:cyclop // a preview, a send and the history, in order.
func TestSendIsADryRunUntilYes(t *testing.T) {
	t.Parallel()

	cs, m, _, _ := connect(t)

	preview, errText := call[mcpserver.SendOut](t, cs, "send_sms", message("0912 111 2222", "سلام", false))
	if errText != "" || !preview.DryRun || preview.Sent || preview.To != "+989121112222" || preview.Encoding != sms.UCS2 {
		t.Fatalf("preview = %+v, %s", preview, errText)
	}

	if len(m.sent) != 0 {
		t.Fatalf("a dry run sent %v", m.sent)
	}

	sent, errText := call[mcpserver.SendOut](t, cs, "send_sms", message("0912 111 2222", "سلام", true))
	if errText != "" || !sent.Sent || sent.DryRun || sent.Remaining != 4 || len(m.sent) != 1 {
		t.Fatalf("send = %+v, %s, modem saw %v", sent, errText, m.sent)
	}

	if _, errText := call[mcpserver.SendOut](t, cs, "send_sms", message("BANK", "x", true)); errText == "" {
		t.Fatal("a sender name was accepted as a number")
	}

	list, _ := call[mcpserver.MessagesOut](t, cs, "list_messages", map[string]any{"with": "+98 912 111 2222"})
	if len(list.Messages) != 1 || list.Messages[0].Source != "mcp" || list.Messages[0].Direction != "out" {
		t.Fatalf("history = %+v", list)
	}
}

func TestUSSDIsADryRunUntilYes(t *testing.T) {
	t.Parallel()

	cs, m, _, _ := connect(t)

	dry, _ := call[mcpserver.USSDOut](t, cs, "ussd", map[string]any{"code": "*555#"})
	if !dry.DryRun || len(m.ussd) != 0 {
		t.Fatalf("dry run = %+v, modem saw %v", dry, m.ussd)
	}

	ran, errText := call[mcpserver.USSDOut](t, cs, "ussd", map[string]any{"code": "*555#", "yes": true})
	if errText != "" || ran.Answer == "" || len(m.ussd) != 1 {
		t.Fatalf("ussd = %+v, %s", ran, errText)
	}
}

//nolint:cyclop // the read tools, one after the other.
func TestReadAndWait(t *testing.T) {
	t.Parallel()

	cs, _, con, _ := connect(t)

	_ = con.Received(sms.SMS{
		Sender: "PARSIANBANK", Name: "", Date: "2026-11-15 10:00:00", Content: "withdrawal", Position: 0,
	})

	convs, _ := call[mcpserver.ConversationsOut](t, cs, "list_conversations", nil)
	if convs.Total != 1 || convs.Conversations[0].With != "parsianbank" {
		t.Fatalf("conversations = %+v", convs)
	}

	st, errText := call[console.Status](t, cs, "status", nil)
	if errText != "" || st.Archive != 1 || st.Remaining != 5 {
		t.Fatalf("status = %+v, %s", st, errText)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)

		_ = con.Received(sms.SMS{Sender: "SNAPP", Name: "", Date: "2026-11-15 10:01:00", Content: "Code 7719", Position: 0})
	}()

	got, _ := call[mcpserver.WaitOut](t, cs, "wait_for_sms", map[string]any{"from": "snapp", "timeout_seconds": 5})
	if !got.Arrived || got.Message == nil || got.Message.Text != "Code 7719" {
		t.Fatalf("wait = %+v", got)
	}

	none, errText := call[mcpserver.WaitOut](t, cs, "wait_for_sms",
		map[string]any{"contains": "never", "timeout_seconds": 1})
	if errText != "" || none.Arrived {
		t.Fatalf("a wait with nothing arriving = %+v, %s", none, errText)
	}
}
