package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/console"
	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
	"github.com/1995parham-me/sms2telegram/internal/infra/archive"
	"github.com/1995parham-me/sms2telegram/internal/infra/web"
)

const password = "correct horse"

type modem struct{ sent []string }

func (m *modem) Send(_ context.Context, to, text string) error {
	m.sent = append(m.sent, to+": "+text)

	return nil
}

func (m *modem) USSD(context.Context, string) (string, error) { return "balance 12,500", nil }

type site struct {
	url    string
	client *http.Client
	modem  *modem
	con    *console.Console
}

func serve(t *testing.T) *site {
	t.Helper()

	store, err := archive.Open("")
	if err != nil {
		t.Fatal(err)
	}

	m := &modem{sent: nil}
	con := console.New(store, m, m, nil, console.Options{
		Country: "98", DailyLimit: 10, MaxParts: 2, Timeout: time.Second, Location: time.UTC,
	})

	hash, err := web.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}

	ui, err := web.New(con, web.Options{
		Users:          map[string]string{"parham": hash},
		SecretFile:     filepath.Join(t.TempDir(), "session.key"),
		SessionTTL:     time.Hour,
		TrustedProxies: nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	ui.Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)

	return &site{url: srv.URL, client: &http.Client{Jar: jar}, modem: m, con: con} //nolint:exhaustruct_v5 // defaults.
}

// call sends a request and decodes the JSON answer into out, when given.
func (s *site) call(t *testing.T, method, path, kind, body string, out any) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, s.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	if kind != "" {
		req.Header.Set("Content-Type", kind)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}

	return resp.StatusCode
}

func (s *site) post(t *testing.T, path, body string, out any) int {
	t.Helper()

	return s.call(t, http.MethodPost, path, "application/json", body, out)
}

func (s *site) login(t *testing.T) {
	t.Helper()

	if code := s.post(t, "/api/login", `{"username":"parham","password":"`+password+`"}`, nil); code != http.StatusOK {
		t.Fatalf("login = %d", code)
	}
}

func TestEverythingNeedsALogin(t *testing.T) {
	t.Parallel()

	s := serve(t)

	for _, path := range []string{"/api/me", "/api/status", "/api/conversations", "/api/messages", "/api/events"} {
		if code := s.call(t, http.MethodGet, path, "", "", nil); code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d", path, code)
		}
	}

	if code := s.post(t, "/api/send", `{"to":"+989121112222","text":"hi"}`, nil); code != http.StatusUnauthorized {
		t.Fatalf("send without a session = %d", code)
	}

	if len(s.modem.sent) != 0 {
		t.Fatal("something was sent without a login")
	}

	// The page itself is public: it is what shows the login form.
	if code := s.call(t, http.MethodGet, "/", "", "", nil); code != http.StatusOK {
		t.Fatalf("GET / = %d", code)
	}

	if code := s.call(t, http.MethodGet, "/static/app.js", "", "", nil); code != http.StatusOK {
		t.Fatalf("GET /static/app.js = %d", code)
	}
}

//nolint:cyclop // one session, checked step by step.
func TestLoginSendAndRead(t *testing.T) {
	t.Parallel()

	s := serve(t)
	s.login(t)

	_ = s.con.Received(sms.SMS{
		Sender: "09121112222", Name: "", Date: "2026-11-15 20:05:06", Content: "hello", Position: 0,
	})

	var sent archive.Message
	if code := s.post(t, "/api/send", `{"to":"0912 111 2222","text":"on my way"}`, &sent); code != http.StatusOK {
		t.Fatalf("send = %d", code)
	}

	if sent.Peer != "+989121112222" || sent.Source != "ui" || len(s.modem.sent) != 1 {
		t.Fatalf("sent = %+v, modem saw %v", sent, s.modem.sent)
	}

	var convs []struct {
		Peer  string `json:"peer"`
		Count int    `json:"count"`
	}
	if s.call(t, http.MethodGet, "/api/conversations", "", "", &convs); len(convs) != 1 || convs[0].Count != 2 {
		t.Fatalf("conversations = %+v", convs)
	}

	var msgs []archive.Message
	if s.call(t, http.MethodGet, "/api/messages?peer=%2B989121112222", "", "", &msgs); len(msgs) != 2 {
		t.Fatalf("messages = %+v", msgs)
	}

	var st console.Status
	if s.call(t, http.MethodGet, "/api/status", "", "", &st); st.Remaining != 9 || st.Archive != 2 || !st.USSD {
		t.Fatalf("status = %+v", st)
	}

	var answer map[string]string
	if code := s.post(t, "/api/ussd", `{"code":"*555#"}`, &answer); code != http.StatusOK || answer["answer"] == "" {
		t.Fatalf("ussd = %d, %v", code, answer)
	}

	if code := s.post(t, "/api/send", `{"to":"nobody","text":"x"}`, nil); code != http.StatusBadRequest {
		t.Fatalf("a bad number = %d", code)
	}

	if code := s.post(t, "/api/logout", `{}`, nil); code != http.StatusOK {
		t.Fatalf("logout = %d", code)
	}

	if code := s.call(t, http.MethodGet, "/api/me", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("after logout = %d", code)
	}
}

// A form on another site can post with the user's cookie, but not as JSON.
func TestFormPostsAreRefused(t *testing.T) {
	t.Parallel()

	s := serve(t)
	s.login(t)

	code := s.call(t, http.MethodPost, "/api/send", "application/x-www-form-urlencoded", "to=%2B989121112222&text=hi", nil)
	if code != http.StatusUnsupportedMediaType || len(s.modem.sent) != 0 {
		t.Fatalf("a form post = %d, modem saw %v", code, s.modem.sent)
	}
}

func TestWrongPasswordsLockTheAddress(t *testing.T) {
	t.Parallel()

	s := serve(t)

	for range 5 {
		if code := s.post(t, "/api/login", `{"username":"parham","password":"guess"}`, nil); code != http.StatusUnauthorized {
			t.Fatalf("a wrong password = %d", code)
		}
	}

	// Even the right password waits out the lock.
	code := s.post(t, "/api/login", `{"username":"parham","password":"`+password+`"}`, nil)
	if code != http.StatusTooManyRequests {
		t.Fatalf("after five failures = %d", code)
	}
}

func TestAForgedCookieIsRefused(t *testing.T) {
	t.Parallel()

	s := serve(t)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, s.url+"/api/me", nil)
	//nolint:exhaustruct_v5,gosec // a forged cookie needs a name and a value.
	req.AddCookie(&http.Cookie{Name: web.CookieName, Value: "cGFyaGFt.9999999999.abcdef012345.c2ln"})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a forged cookie = %d", resp.StatusCode)
	}
}
