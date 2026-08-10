package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/hub"
	"github.com/Zero-Hex/modern-eq-chat/tlog"
)

const testPassword = "correct-horse-battery"

func TestMain(m *testing.M) {
	tlog.Init(nil, os.Stdout)
	os.Exit(m.Run())
}

// startServer brings up a hub and its web interface on ephemeral ports.
func startServer(t *testing.T) (*Server, string) {
	t.Helper()

	stateDir := t.TempDir()
	configPath := filepath.Join(stateDir, "modern-eq-chat.conf")

	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("hash password: %s", err)
	}

	cfg := config.Default()
	cfg.Relay.Mode = config.ModeHub
	cfg.Relay.Hub.Listen = "127.0.0.1:0"
	cfg.Relay.Hub.AgentsDatabase = filepath.Join(stateDir, "agents.json")
	cfg.Relay.Hub.EnrollDatabase = filepath.Join(stateDir, "enroll.json")
	cfg.Relay.Hub.TLSMode = config.TLSNone
	cfg.Relay.Hub.Web = config.WebConfig{
		IsEnabled:    true,
		Listen:       "127.0.0.1:0",
		PasswordHash: hash,
	}
	cfg.Discord.IsEnabled = false
	cfg.API.IsEnabled = false

	if err := cfg.Verify(); err != nil {
		t.Fatalf("verify: %s", err)
	}
	if err := config.Save(&cfg, configPath); err != nil {
		t.Fatalf("save config: %s", err)
	}

	h, err := hub.New(context.Background(), cfg.Relay.Hub)
	if err != nil {
		t.Fatalf("new hub: %s", err)
	}
	if err := h.Connect(context.Background()); err != nil {
		t.Fatalf("hub connect: %s", err)
	}
	t.Cleanup(func() { h.Disconnect(context.Background()) })

	s, err := New(cfg.Relay.Hub.Web, h, configPath)
	if err != nil {
		t.Fatalf("new web server: %s", err)
	}
	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("web connect: %s", err)
	}
	t.Cleanup(func() { s.Disconnect(context.Background()) })

	return s, configPath
}

// client is a browser-shaped test client that keeps cookies and the CSRF token.
type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func newClient(t *testing.T, s *Server) *client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %s", err)
	}
	return &client{
		t:    t,
		base: "http://" + s.Addr(),
		http: &http.Client{Jar: jar},
	}
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal: %s", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("new request: %s", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.csrf != "" {
		req.Header.Set(csrfHeader, c.csrf)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %s", method, path, err)
	}
	defer resp.Body.Close()

	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c *client) login(password string) int {
	c.t.Helper()

	status, body := c.do(http.MethodPost, "/api/login", map[string]string{"password": password})
	if csrf, ok := body["csrf"].(string); ok {
		c.csrf = csrf
	}
	return status
}

// Nothing behind the login may be reachable without one. This is the whole
// security boundary: the endpoints below can rewrite telnet command patterns
// on every connected game server.
func TestEveryEndpointRequiresLogin(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)

	protected := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/overview"},
		{http.MethodGet, "/api/agents"},
		{http.MethodPost, "/api/agents/rename"},
		{http.MethodPost, "/api/agents/enable"},
		{http.MethodPost, "/api/agents/remove"},
		{http.MethodPost, "/api/agents/rotate"},
		{http.MethodPost, "/api/agents/test"},
		{http.MethodGet, "/api/enroll"},
		{http.MethodPost, "/api/enroll"},
		{http.MethodPost, "/api/enroll/revoke"},
		{http.MethodGet, "/api/channels"},
		{http.MethodPost, "/api/channels"},
	}

	for _, endpoint := range protected {
		status, _ := c.do(endpoint.method, endpoint.path, map[string]string{})
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session, want 401",
				endpoint.method, endpoint.path, status)
		}
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)

	if status := c.login("wrong"); status != http.StatusUnauthorized {
		t.Errorf("login with a wrong password returned %d, want 401", status)
	}
	if c.csrf != "" {
		t.Error("a CSRF token was issued for a failed login")
	}
}

func TestLoginSucceedsAndGrantsAccess(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)

	if status := c.login(testPassword); status != http.StatusOK {
		t.Fatalf("login returned %d, want 200", status)
	}
	if c.csrf == "" {
		t.Fatal("no CSRF token was issued")
	}

	status, body := c.do(http.MethodGet, "/api/overview", nil)
	if status != http.StatusOK {
		t.Fatalf("overview returned %d after login", status)
	}
	if _, ok := body["hub_address"]; !ok {
		t.Errorf("overview is missing hub_address: %v", body)
	}
}

// A valid session is not enough for a mutation; the CSRF token has to match.
func TestMutationsRequireCSRFToken(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)

	if status := c.login(testPassword); status != http.StatusOK {
		t.Fatalf("login failed")
	}

	// Keep the session cookie, drop the token.
	c.csrf = ""
	status, _ := c.do(http.MethodPost, "/api/enroll", map[string]string{"server_key": "server2"})
	if status != http.StatusForbidden {
		t.Errorf("POST without a CSRF token returned %d, want 403", status)
	}

	c.csrf = "wrong-token"
	status, _ = c.do(http.MethodPost, "/api/enroll", map[string]string{"server_key": "server2"})
	if status != http.StatusForbidden {
		t.Errorf("POST with a bad CSRF token returned %d, want 403", status)
	}
}

// Reads are exempt from CSRF, which must not accidentally exempt writes.
func TestReadsDoNotRequireCSRFToken(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)

	if status := c.login(testPassword); status != http.StatusOK {
		t.Fatalf("login failed")
	}
	c.csrf = ""

	if status, _ := c.do(http.MethodGet, "/api/agents", nil); status != http.StatusOK {
		t.Errorf("GET without a CSRF token returned %d, want 200", status)
	}
}

func TestLogoutInvalidatesTheSession(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)

	c.login(testPassword)
	if status, _ := c.do(http.MethodPost, "/api/logout", nil); status != http.StatusOK {
		t.Fatal("logout failed")
	}

	if status, _ := c.do(http.MethodGet, "/api/overview", nil); status != http.StatusUnauthorized {
		t.Errorf("session still worked after logout: %d", status)
	}
}

// A bad channel pattern must be rejected outright. If it were accepted, it
// would be written to modern-eq-chat.conf and break the hub's next startup.
func TestInvalidChannelPatternIsRejected(t *testing.T) {
	s, configPath := startServer(t)
	c := newClient(t, s)
	c.login(testPassword)

	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %s", err)
	}

	status, body := c.do(http.MethodPost, "/api/channels", map[string]any{
		"channels": []map[string]any{{
			"name":               "ooc",
			"enabled":            true,
			"cross_server":       true,
			"discord_channel_id": "123",
			"discord_pattern":    "{{.Name} broken",
		}},
	})
	if status != http.StatusBadRequest {
		t.Errorf("invalid pattern returned %d, want 400 (%v)", status, body)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %s", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("config was written despite the pattern being rejected")
	}
}

// A valid change must reach both the running hub and the config file, so it
// survives a restart.
func TestChannelUpdateAppliesAndPersists(t *testing.T) {
	s, configPath := startServer(t)
	c := newClient(t, s)
	c.login(testPassword)

	status, body := c.do(http.MethodPost, "/api/channels", map[string]any{
		"channels": []map[string]any{{
			"name":               "ooc",
			"enabled":            true,
			"cross_server":       false,
			"discord_channel_id": "999888777",
			"discord_pattern":    "[{{.OriginName}}] {{.Name}}: {{.Message}}",
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("channel update returned %d: %v", status, body)
	}

	// Applied live.
	channels := s.hub.Channels()
	if len(channels) != 1 || channels[0].DiscordChannelID != "999888777" {
		t.Errorf("running hub did not pick up the change: %+v", channels)
	}
	if channels[0].IsCrossServer {
		t.Error("cross_server was not turned off on the running hub")
	}

	// And saved.
	saved, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %s", err)
	}
	if len(saved.Relay.Hub.Channels) != 1 || saved.Relay.Hub.Channels[0].DiscordChannelID != "999888777" {
		t.Errorf("change was not written to the config: %+v", saved.Relay.Hub.Channels)
	}
}

func TestRenameAgent(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)
	c.login(testPassword)

	if _, err := s.hub.Roster().Add("server2", "Old Name"); err != nil {
		t.Fatalf("add agent: %s", err)
	}

	status, body := c.do(http.MethodPost, "/api/agents/rename", map[string]string{
		"server_key": "server2",
		"short_name": "New Name",
	})
	if status != http.StatusOK {
		t.Fatalf("rename returned %d: %v", status, body)
	}

	entry, ok := s.hub.Roster().Entry("server2")
	if !ok {
		t.Fatal("agent vanished")
	}
	if entry.ShortName != "New Name" {
		t.Errorf("short name = %q, want New Name", entry.ShortName)
	}
}

// A display name ends up inside relayed chat, so it must not be able to carry
// a line break into a telnet command.
func TestRenameSanitizesTheName(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)
	c.login(testPassword)

	if _, err := s.hub.Roster().Add("server2", "Classic"); err != nil {
		t.Fatalf("add agent: %s", err)
	}

	c.do(http.MethodPost, "/api/agents/rename", map[string]string{
		"server_key": "server2",
		"short_name": "Evil\nzoneshutdown",
	})

	entry, _ := s.hub.Roster().Entry("server2")
	if strings.ContainsAny(entry.ShortName, "\r\n") {
		t.Errorf("stored name contains a line break: %q", entry.ShortName)
	}
}

func TestEnrollThroughTheAPI(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)
	c.login(testPassword)

	status, body := c.do(http.MethodPost, "/api/enroll", map[string]string{
		"server_key": "server2",
		"short_name": "Classic",
	})
	if status != http.StatusOK {
		t.Fatalf("enroll returned %d: %v", status, body)
	}

	code, _ := body["code"].(string)
	if code == "" {
		t.Fatal("no code was returned")
	}

	// And it shows up as pending.
	status, body = c.do(http.MethodGet, "/api/enroll", nil)
	if status != http.StatusOK {
		t.Fatalf("pending list returned %d", status)
	}
	pending, _ := body["pending"].([]any)
	if len(pending) != 1 {
		t.Errorf("pending = %d entries, want 1", len(pending))
	}
}

func TestTestingADisconnectedAgentReportsRatherThanFails(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)
	c.login(testPassword)

	if _, err := s.hub.Roster().Add("server2", "Classic"); err != nil {
		t.Fatalf("add agent: %s", err)
	}

	status, body := c.do(http.MethodPost, "/api/agents/test", map[string]any{
		"server_key": "server2",
	})
	if status != http.StatusOK {
		t.Fatalf("test returned %d: %v", status, body)
	}

	results, _ := body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	first, _ := results[0].(map[string]any)
	if connected, _ := first["is_connected"].(bool); connected {
		t.Error("a disconnected agent was reported as connected")
	}
	if detail, _ := first["detail"].(string); detail != "not connected" {
		t.Errorf("detail = %q, want a clear explanation", detail)
	}
}

func TestUnknownJSONFieldsAreRejected(t *testing.T) {
	s, _ := startServer(t)
	c := newClient(t, s)
	c.login(testPassword)

	status, _ := c.do(http.MethodPost, "/api/enroll", map[string]any{
		"server_key":   "server2",
		"unknown_junk": true,
	})
	if status != http.StatusBadRequest {
		t.Errorf("request with an unknown field returned %d, want 400", status)
	}
}

func TestSecurityHeadersAreSet(t *testing.T) {
	s, _ := startServer(t)

	resp, err := http.Get("http://" + s.Addr() + "/")
	if err != nil {
		t.Fatalf("get index: %s", err)
	}
	defer resp.Body.Close()

	required := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for header, want := range required {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	csp := resp.Header.Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP is missing %q: %s", directive, csp)
		}
	}
}

func TestServerRefusesWithoutPassword(t *testing.T) {
	_, err := New(config.WebConfig{IsEnabled: true, Listen: "127.0.0.1:0"}, &hub.Hub{}, "modern-eq-chat.conf")
	if err == nil {
		t.Fatal("the web interface started with no admin password")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error does not explain the problem: %s", err)
	}
}

func TestIsLoopback(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:34198": true,
		"localhost:34198": true,
		"[::1]:34198":     true,
		"0.0.0.0:34198":   false,
		":34198":          false,
		"10.0.0.5:34198":  false,
	}
	for listen, want := range tests {
		if got := isLoopback(listen); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", listen, got, want)
		}
	}
}

// The index page must be served from the embedded filesystem, with no path
// that reaches the real disk.
func TestStaticAssetsAreServed(t *testing.T) {
	s, _ := startServer(t)

	for _, path := range []string{"/", "/style.css", "/app.js"} {
		resp, err := http.Get("http://" + s.Addr() + path)
		if err != nil {
			t.Fatalf("get %s: %s", path, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s returned %d", path, resp.StatusCode)
		}
	}

	// A traversal attempt must not escape the embedded filesystem.
	resp, err := http.Get("http://" + s.Addr() + "/../../modern-eq-chat.conf")
	if err != nil {
		t.Fatalf("traversal request: %s", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "password_hash") {
		t.Fatal("path traversal reached the config file")
	}
}

func TestHashPasswordDoesNotStoreThePassword(t *testing.T) {
	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("hash: %s", err)
	}
	if strings.Contains(hash, testPassword) {
		t.Fatal("the hash contains the password")
	}
	if !strings.HasPrefix(hash, "argon2id$") {
		t.Errorf("hash is not argon2id: %s", hash)
	}
}
