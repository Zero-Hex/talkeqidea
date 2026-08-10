// Package webui serves the hub's local management interface.
//
// The UI can rewrite routing patterns, and those patterns become telnet
// commands on every connected game server. It is therefore treated as a
// privileged console rather than a settings page: it binds to loopback by
// default, refuses to start without an admin password, and every mutating
// request needs a session cookie and a CSRF token.
package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/guard"
	"github.com/xackery/talkeq/hub"
	"github.com/xackery/talkeq/tlog"
)

//go:embed assets
var assets embed.FS

// maxBodySize bounds a request body. The largest legitimate request is a
// channel list, which is small.
const maxBodySize = 256 * 1024

// Server is the web interface.
type Server struct {
	cfg          config.WebConfig
	hub          *hub.Hub
	configPath   string
	passwordHash string

	sessions *sessionStore
	guard    *guard.Guard

	server   *http.Server
	listener net.Listener
}

// New builds the web interface over a hub.
func New(cfg config.WebConfig, h *hub.Hub, configPath string) (*Server, error) {
	if h == nil {
		return nil, fmt.Errorf("the web interface needs a hub")
	}
	if cfg.PasswordHash == "" {
		return nil, fmt.Errorf("no admin password is set; run 'talkeq-hub web password' to set one")
	}

	loginGuard, err := newLoginGuard()
	if err != nil {
		return nil, fmt.Errorf("login guard: %w", err)
	}

	return &Server{
		cfg:          cfg,
		hub:          h,
		configPath:   configPath,
		passwordHash: cfg.PasswordHash,
		sessions:     newSessionStore(),
		guard:        loginGuard,
	}, nil
}

// Connect starts the listener.
func (s *Server) Connect(ctx context.Context) error {
	mux := http.NewServeMux()

	// Static assets are served from the embedded filesystem only, so there is
	// no path that can reach the real disk.
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		return fmt.Errorf("embedded assets: %w", err)
	}
	mux.Handle("/", s.secureHeaders(http.FileServer(http.FS(sub))))

	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)

	mux.HandleFunc("/api/overview", s.requireAuth(s.handleOverview))
	mux.HandleFunc("/api/agents", s.requireAuth(s.handleAgents))
	mux.HandleFunc("/api/agents/rename", s.requireAuth(s.handleRename))
	mux.HandleFunc("/api/agents/enable", s.requireAuth(s.handleSetEnabled))
	mux.HandleFunc("/api/agents/remove", s.requireAuth(s.handleRemove))
	mux.HandleFunc("/api/agents/rotate", s.requireAuth(s.handleRotate))
	mux.HandleFunc("/api/agents/test", s.requireAuth(s.handleTest))
	mux.HandleFunc("/api/enroll", s.requireAuth(s.handleEnroll))
	mux.HandleFunc("/api/enroll/revoke", s.requireAuth(s.handleEnrollRevoke))
	mux.HandleFunc("/api/channels", s.requireAuth(s.handleChannels))

	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	listener, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Listen, err)
	}
	s.listener = listener

	if !isLoopback(s.cfg.Listen) {
		tlog.Warnf("[web] listening on %s, which is reachable from other machines.", s.cfg.Listen)
		tlog.Warnf("[web] this interface can rewrite telnet command patterns on every connected server.")
		tlog.Warnf("[web] prefer 127.0.0.1 and reach it over an SSH tunnel.")
	}

	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			tlog.Errorf("[web] serve failed: %s", err)
		}
	}()

	tlog.Infof("[web] management interface on http://%s", s.Addr())
	return nil
}

// Addr is the address the interface is listening on.
func (s *Server) Addr() string {
	if s.listener == nil {
		return s.cfg.Listen
	}
	return s.listener.Addr().String()
}

// Disconnect stops the listener.
func (s *Server) Disconnect(ctx context.Context) error {
	if s.server == nil {
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// secureHeaders sets response headers for the served page.
//
// The content security policy is restrictive because everything the page needs
// is inline and same-origin: no CDN, no external fonts, no remote images. That
// makes a strict policy free, and it means an injected script has nowhere to
// send anything.
func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		tlog.Debugf("[web] encode response failed: %s", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		tlog.Debugf("[web] encode error failed: %s", err)
	}
}

func decodeJSON(r *http.Request, out any) error {
	body := http.MaxBytesReader(nil, r.Body, maxBodySize)
	defer io.Copy(io.Discard, body)

	decoder := json.NewDecoder(body)
	// Reject unknown fields so a typo in a request is an error rather than a
	// silently ignored setting.
	decoder.DisallowUnknownFields()

	return decoder.Decode(out)
}
