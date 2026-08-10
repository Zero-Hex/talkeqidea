package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Zero-Hex/modern-eq-chat/guard"
	"github.com/Zero-Hex/modern-eq-chat/relay"
	"github.com/Zero-Hex/modern-eq-chat/tlog"
)

// Authentication for the web interface.
//
// This UI can rewrite inbound_pattern, which is a telnet command template
// executed on every connected game server. That makes it a remote code
// execution primitive, not a settings page, and it is why the defaults here
// are as strict as they are: loopback binding, a real password hash, sessions
// that expire, CSRF on every mutation, and rate-limited logins.

const (
	sessionCookie = "meqc_session"
	csrfHeader    = "X-MEQC-CSRF"

	// sessionTTL is how long a login lasts without activity.
	sessionTTL = 8 * time.Hour
	// sessionSweepEvery bounds how often expired sessions are collected.
	sessionSweepEvery = 10 * time.Minute
)

type session struct {
	csrf      string
	expiresAt time.Time
}

// sessionStore holds logged-in sessions.
//
// In memory on purpose: a hub restart logging every browser out is correct
// behavior for an admin console, and it means there is no session file to
// steal.
type sessionStore struct {
	mu        sync.Mutex
	sessions  map[string]*session
	lastSweep time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{
		sessions:  make(map[string]*session),
		lastSweep: time.Now(),
	}
}

func (s *sessionStore) create() (id string, csrf string, err error) {
	id, err = randomToken()
	if err != nil {
		return "", "", err
	}
	csrf, err = randomToken()
	if err != nil {
		return "", "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.sweepLocked()
	s.sessions[id] = &session{csrf: csrf, expiresAt: time.Now().Add(sessionTTL)}

	return id, csrf, nil
}

// get returns a live session and extends it, so an operator working in the UI
// is not logged out mid-edit.
func (s *sessionStore) get(id string) (*session, bool) {
	if id == "" {
		return nil, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	found, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	if time.Now().After(found.expiresAt) {
		delete(s.sessions, id)
		return nil, false
	}

	found.expiresAt = time.Now().Add(sessionTTL)
	return found, true
}

func (s *sessionStore) destroy(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *sessionStore) sweepLocked() {
	now := time.Now()
	if now.Sub(s.lastSweep) < sessionSweepEvery {
		return
	}
	s.lastSweep = now

	for id, found := range s.sessions {
		if now.After(found.expiresAt) {
			delete(s.sessions, id)
		}
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashPassword derives a storable hash of an admin password.
func HashPassword(password string) (string, error) {
	return relay.HashToken(password)
}

// handleLogin authenticates and issues a session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Logins are rate limited the same way agent connections are, so the
	// admin password cannot be ground down over the loopback interface by
	// anything that gets a foothold on the box.
	if err := s.guard.AllowConnection(r.RemoteAddr, 0); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	if s.passwordHash == "" {
		writeError(w, http.StatusServiceUnavailable, "no admin password is set; run 'modern-eq-chat-hub web password'")
		return
	}

	if !relay.VerifyToken(body.Password, s.passwordHash) {
		s.guard.RecordAuthFailure(r.RemoteAddr)
		tlog.Warnf("[web] failed login from %s", r.RemoteAddr)
		// Deliberately vague and slow-pathed: no hint about whether the
		// password was close, and the same message for every failure.
		writeError(w, http.StatusUnauthorized, "incorrect password")
		return
	}
	s.guard.RecordAuthSuccess(r.RemoteAddr)

	id, csrf, err := s.sessions.create()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start a session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		// Strict rather than Lax: nothing legitimately links into this UI from
		// elsewhere, so there is no reason to let another site's navigation
		// carry the cookie.
		SameSite: http.SameSiteStrictMode,
		// Secure is deliberately not set. The UI is served over plain HTTP on
		// loopback, where there is nothing to intercept, and setting it would
		// make the cookie unusable.
		MaxAge: int(sessionTTL.Seconds()),
	})

	tlog.Infof("[web] admin logged in from %s", r.RemoteAddr)
	writeJSON(w, map[string]any{"csrf": csrf})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.destroy(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeJSON(w, map[string]any{"ok": true})
}

// requireAuth wraps a handler with session and CSRF checks.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}

		found, ok := s.sessions.get(cookie.Value)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}

		// CSRF applies to anything that changes state. A GET carries no
		// token because a cross-site read of JSON is blocked by the browser
		// anyway, and requiring one would break a plain page load.
		if r.Method != http.MethodGet {
			supplied := r.Header.Get(csrfHeader)
			if subtle.ConstantTimeCompare([]byte(supplied), []byte(found.csrf)) != 1 {
				tlog.Warnf("[web] rejected a request from %s with a bad CSRF token", r.RemoteAddr)
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
		}

		next(w, r)
	}
}

// newLoginGuard builds the rate limiter protecting the login endpoint.
func newLoginGuard() (*guard.Guard, error) {
	return guard.New(guard.Limits{
		ConnectionsPerMinute:  20,
		AuthFailuresBeforeBan: 5,
		BanDuration:           5 * time.Minute,
	})
}

// isLoopback reports whether a bind address is restricted to this machine.
func isLoopback(listen string) bool {
	host := strings.TrimSpace(listen)
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	host = strings.Trim(host, "[]")

	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}
