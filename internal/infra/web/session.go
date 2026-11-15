package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// CookieName is the session cookie.
const CookieName = "sms2telegram_session"

// Login limits: this many failures from one address within the window lock
// that address out until the window passes.
const (
	maxFailures   = 5
	failureWindow = 15 * time.Minute
)

const (
	secretBytes = 32
	tagBytes    = 6
	dirMode     = 0o700
	fileMode    = 0o600
	// tokenFields are the user, the expiry and the password tag.
	tokenFields = 3
)

// Errors of the session layer.
var (
	ErrBadCredentials = errors.New("wrong user name or password")
	ErrLocked         = errors.New("too many failed logins; try again later")
	errShortSecret    = errors.New("the session key is shorter than 32 bytes")
)

// sessions issues and checks signed session cookies.
type sessions struct {
	users   map[string]string
	secret  []byte
	ttl     time.Duration
	proxies []string
	now     func() time.Time
	// dummy is checked for an unknown user, so that takes as long as a known one.
	dummy []byte

	mu       sync.Mutex
	failures map[string][]time.Time
}

func newSessions(users map[string]string, secretFile string, ttl time.Duration, proxies []string) (*sessions, error) {
	secret, err := loadSecret(secretFile)
	if err != nil {
		return nil, err
	}

	dummy, err := bcrypt.GenerateFromPassword([]byte("sms2telegram"), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}

	return &sessions{
		users:    users,
		secret:   secret,
		ttl:      ttl,
		proxies:  proxies,
		now:      time.Now,
		dummy:    dummy,
		mu:       sync.Mutex{},
		failures: map[string][]time.Time{},
	}, nil
}

// loadSecret reads the signing key, creating it (mode 0600) when missing, so
// sessions survive a restart.
func loadSecret(path string) ([]byte, error) {
	secret, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's own config.
	if errors.Is(err, fs.ErrNotExist) {
		raw := make([]byte, secretBytes)
		_, _ = rand.Read(raw)

		secret = []byte(hex.EncodeToString(raw))

		if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
			return nil, fmt.Errorf("session key: %w", err)
		}

		if err := os.WriteFile(path, secret, fileMode); err != nil {
			return nil, fmt.Errorf("session key: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}

	secret = []byte(strings.TrimSpace(string(secret)))
	if len(secret) < secretBytes {
		return nil, fmt.Errorf("%w: %s", errShortSecret, path)
	}

	return secret, nil
}

// passwordTag ties a session to the password hash it was issued for:
// changing a password ends every older session.
func passwordTag(hash string) string {
	sum := sha256.Sum256([]byte(hash))

	return hex.EncodeToString(sum[:tagBytes])
}

func (s *sessions) sign(payload string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// login checks the password and returns a session token.
func (s *sessions) login(r *http.Request, user, password string) (string, error) {
	ip := s.clientIP(r)

	if s.locked(ip) {
		return "", ErrLocked
	}

	hash, known := s.users[user]
	if !known {
		_ = bcrypt.CompareHashAndPassword(s.dummy, []byte(password))
		s.fail(ip)

		return "", ErrBadCredentials
	}

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		s.fail(ip)

		return "", ErrBadCredentials
	}

	payload := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte(user)),
		strconv.FormatInt(s.now().Add(s.ttl).Unix(), 10),
		passwordTag(hash),
	}, ".")

	return payload + "." + s.sign(payload), nil
}

// user returns the user a request's session belongs to, or "".
func (s *sessions) user(r *http.Request) string {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}

	at := strings.LastIndexByte(cookie.Value, '.')
	if at < 0 {
		return ""
	}

	payload, sig := cookie.Value[:at], cookie.Value[at+1:]
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return ""
	}

	fields := strings.Split(payload, ".")
	if len(fields) != tokenFields {
		return ""
	}

	name, err := base64.RawURLEncoding.DecodeString(fields[0])
	if err != nil {
		return ""
	}

	expiry, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || s.now().Unix() >= expiry {
		return ""
	}

	hash, known := s.users[string(name)]
	if !known || passwordTag(hash) != fields[2] {
		return ""
	}

	return string(name)
}

func (s *sessions) locked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.now().Add(-failureWindow)
	recent := slices.DeleteFunc(s.failures[ip], func(t time.Time) bool { return t.Before(cutoff) })

	if len(recent) == 0 {
		delete(s.failures, ip)
	} else {
		s.failures[ip] = recent
	}

	return len(recent) >= maxFailures
}

func (s *sessions) fail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failures[ip] = append(s.failures[ip], s.now())
}

// proxied reports whether the request came through a trusted reverse proxy.
func (s *sessions) proxied(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}

	return slices.Contains(s.proxies, host)
}

// clientIP is the peer address, or the X-Real-IP a trusted proxy sets.
func (s *sessions) clientIP(r *http.Request) string {
	if s.proxied(r) {
		if ip := r.Header.Get("X-Real-IP"); ip != "" {
			return ip
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// secure reports whether the browser reached the service over HTTPS, directly
// or through a trusted proxy.
func (s *sessions) secure(r *http.Request) bool {
	return r.TLS != nil || (s.proxied(r) && r.Header.Get("X-Forwarded-Proto") == "https")
}

func (s *sessions) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	// Secure follows the request: the UI is also reached over plain HTTP on the LAN.
	//nolint:exhaustruct_v5,gosec // only the set fields matter; see above for Secure.
	return &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secure(r),
		SameSite: http.SameSiteStrictMode,
	}
}

// HashPassword returns the bcrypt hash to put in ui.users.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}

	return string(hash), nil
}
