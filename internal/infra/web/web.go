// Package web serves the web UI: a single page over a small JSON API, behind
// a login. Everything it does goes through the console, the same as MCP.
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/domain/console"
	"github.com/1995parham-me/sms2telegram/internal/infra/archive"
)

//go:embed static
var static embed.FS

const (
	// maxBody bounds a request; the longest is a message of a few SMS.
	maxBody = 64 << 10
	// defaultMessages and maxMessages bound one page of a conversation.
	defaultMessages = 200
	maxMessages     = 1000
	maxContacts     = 8
	// keepAlive is how often an idle event stream says it is still there, so
	// a proxy does not close it.
	keepAlive = 25 * time.Second
	// source marks the messages sent from here in the history.
	source = "ui"
)

// Options configures the UI.
type Options struct {
	// Users maps user names to bcrypt hashes.
	Users map[string]string
	// SecretFile holds the session signing key; it is created when missing.
	SecretFile string
	// SessionTTL is how long a login lasts.
	SessionTTL time.Duration
	// TrustedProxies are the reverse proxies whose headers are believed.
	TrustedProxies []string
}

// Handler serves the UI and its API.
type Handler struct {
	console  *console.Console
	sessions *sessions
	files    http.Handler
	index    []byte
}

// New returns the UI over c.
func New(c *console.Console, opts Options) (*Handler, error) {
	s, err := newSessions(opts.Users, opts.SecretFile, opts.SessionTTL, opts.TrustedProxies)
	if err != nil {
		return nil, err
	}

	sub, err := fs.Sub(static, "static")
	if err != nil {
		return nil, fmt.Errorf("opening the UI files: %w", err)
	}

	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil, fmt.Errorf("opening the UI files: %w", err)
	}

	return &Handler{console: c, sessions: s, files: http.FileServerFS(sub), index: index}, nil
}

// Register adds the UI's routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.page)
	mux.Handle("GET /static/", http.StripPrefix("/static/", h.files))

	mux.HandleFunc("POST /api/login", h.login)
	mux.HandleFunc("POST /api/logout", h.logout)

	mux.HandleFunc("GET /api/me", h.authed(h.me))
	mux.HandleFunc("GET /api/status", h.authed(h.status))
	mux.HandleFunc("GET /api/conversations", h.authed(h.conversations))
	mux.HandleFunc("GET /api/messages", h.authed(h.messages))
	mux.HandleFunc("GET /api/contacts", h.authed(h.contacts))
	mux.HandleFunc("GET /api/events", h.authed(h.events))
	mux.HandleFunc("POST /api/preview", h.authed(h.preview))
	mux.HandleFunc("POST /api/send", h.authed(h.send))
	mux.HandleFunc("POST /api/ussd", h.authed(h.ussd))
}

func (h *Handler) page(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// Everything the page loads is served from here.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(h.index)
}

type handler func(w http.ResponseWriter, r *http.Request, user string)

// authed runs next only for a request with a valid session.
func (h *Handler) authed(next handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := h.sessions.user(r)
		if user == "" {
			fail(w, http.StatusUnauthorized, "log in first")

			return
		}

		next(w, r, user)
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // every answer is a plain struct or map.
}

func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}

// decode reads a JSON body. Requiring the JSON content type is what keeps
// another site's form from posting here with the user's cookie.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if kind != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "send JSON")

		return false
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "bad request: "+err.Error())

		return false
	}

	return true
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if !decode(w, r, &in) {
		return
	}

	token, err := h.sessions.login(r, strings.TrimSpace(in.Username), in.Password)

	switch {
	case errors.Is(err, ErrLocked):
		fail(w, http.StatusTooManyRequests, err.Error())
	case err != nil:
		slog.WarnContext(r.Context(), "failed login", "user", in.Username, "from", h.sessions.clientIP(r))
		fail(w, http.StatusUnauthorized, err.Error())
	default:
		http.SetCookie(w, h.sessions.cookie(r, token, int(h.sessions.ttl.Seconds())))
		reply(w, http.StatusOK, map[string]string{"user": in.Username})
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, h.sessions.cookie(r, "", -1))
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) me(w http.ResponseWriter, _ *http.Request, user string) {
	reply(w, http.StatusOK, map[string]string{"user": user})
}

func (h *Handler) status(w http.ResponseWriter, _ *http.Request, _ string) {
	reply(w, http.StatusOK, h.console.Status())
}

// message is an archived message with the sender's name looked up now, since
// names change after a message is stored.
type message struct {
	archive.Message

	Name string `json:"name,omitempty"`
}

func (h *Handler) view(m archive.Message) message {
	return message{Message: m, Name: h.console.Name(m.Number)}
}

type conversation struct {
	Peer  string  `json:"peer"`
	Name  string  `json:"name,omitempty"`
	Count int     `json:"count"`
	Last  message `json:"last"`
}

func (h *Handler) conversations(w http.ResponseWriter, _ *http.Request, _ string) {
	list := h.console.Archive().Conversations()
	out := make([]conversation, 0, len(list))

	for _, c := range list {
		last := h.view(c.Last)
		out = append(out, conversation{Peer: c.Peer, Name: last.Name, Count: c.Count, Last: last})
	}

	reply(w, http.StatusOK, out)
}

func (h *Handler) messages(w http.ResponseWriter, r *http.Request, _ string) {
	q := r.URL.Query()

	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = defaultMessages
	}

	filter := archive.Filter{ //nolint:exhaustruct_v5 // the other fields select everything.
		Peer:  q.Get("peer"),
		Query: q.Get("q"),
		Limit: min(limit, maxMessages),
	}

	if before, err := time.Parse(time.RFC3339Nano, q.Get("before")); err == nil {
		filter.Before = before
	}

	list := h.console.Archive().Messages(filter)
	out := make([]message, 0, len(list))

	for _, m := range list {
		out = append(out, h.view(m))
	}

	reply(w, http.StatusOK, out)
}

func (h *Handler) contacts(w http.ResponseWriter, r *http.Request, _ string) {
	found := h.console.Contacts(r.URL.Query().Get("q"), maxContacts)
	if found == nil {
		reply(w, http.StatusOK, []struct{}{})

		return
	}

	reply(w, http.StatusOK, found)
}

type outgoing struct {
	To   string `json:"to"`
	Text string `json:"text"`
}

func (h *Handler) preview(w http.ResponseWriter, r *http.Request, _ string) {
	var in outgoing
	if !decode(w, r, &in) {
		return
	}

	p, err := h.console.Check(in.To, in.Text, source)
	if err != nil {
		fail(w, refusal(err), err.Error())

		return
	}

	reply(w, http.StatusOK, p)
}

func (h *Handler) send(w http.ResponseWriter, r *http.Request, user string) {
	var in outgoing
	if !decode(w, r, &in) {
		return
	}

	msg, err := h.console.Send(r.Context(), in.To, in.Text, source)

	switch {
	case err != nil && msg.ID == "":
		fail(w, refusal(err), err.Error())
	case err != nil:
		slog.WarnContext(r.Context(), "SMS not sent", "user", user, "to", msg.Number, "error", err)
		// The attempt is in the history; the page shows it as failed.
		reply(w, http.StatusBadGateway, struct {
			Error   string  `json:"error"`
			Message message `json:"message"`
		}{Error: err.Error(), Message: h.view(msg)})
	default:
		slog.InfoContext(r.Context(), "SMS sent", "user", user, "to", msg.Number)
		reply(w, http.StatusOK, h.view(msg))
	}
}

func (h *Handler) ussd(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Code string `json:"code"`
	}

	if !decode(w, r, &in) {
		return
	}

	answer, err := h.console.RunUSSD(r.Context(), in.Code)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, console.ErrUSSDCode) || errors.Is(err, console.ErrNoUSSD) {
			status = http.StatusBadRequest
		}

		fail(w, status, err.Error())

		return
	}

	slog.InfoContext(r.Context(), "USSD run", "user", user, "code", in.Code)
	reply(w, http.StatusOK, map[string]string{"answer": answer})
}

// refusal is the status for a message the console would not send.
func refusal(err error) int {
	if errors.Is(err, console.ErrLimit) {
		return http.StatusTooManyRequests
	}

	return http.StatusBadRequest
}

// events streams every new message as a server-sent event, so the page
// shows an SMS the moment it is forwarded.
func (h *Handler) events(w http.ResponseWriter, r *http.Request, _ string) {
	ctl := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	// nginx buffers a proxied answer unless told not to.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	_, _ = fmt.Fprint(w, ": connected\n\n")
	_ = ctl.Flush()

	ch, stop := h.console.Archive().Subscribe()
	defer stop()

	ticker := time.NewTicker(keepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		case m := <-ch:
			data, err := json.Marshal(h.view(m))
			if err != nil {
				continue
			}

			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		}

		if ctl.Flush() != nil {
			return
		}
	}
}
