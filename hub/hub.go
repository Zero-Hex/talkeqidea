package hub

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/relay"
	"github.com/xackery/talkeq/request"
	"github.com/xackery/talkeq/sanitize"
	"github.com/xackery/talkeq/tlog"
)

// Hub is the central router every agent connects to.
//
// It owns the only copy of the Discord credentials and the only copy of the
// agent roster. Agents hold nothing but their own token, so compromising a
// game server does not expose the bot or any other server's access.
type Hub struct {
	mu       sync.RWMutex
	cfg      config.HubConfig
	roster   *Roster
	enroll   *EnrollStore
	router   *Router
	sessions map[string]*Session

	subscribers []func(interface{}) error

	server      *http.Server
	listener    net.Listener
	fingerprint string
	isConnected bool

	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a hub from configuration.
func New(ctx context.Context, cfg config.HubConfig) (*Hub, error) {
	ctx, cancel := context.WithCancel(ctx)

	roster, err := NewRoster(cfg.AgentsDatabase)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("roster: %w", err)
	}

	enroll, err := NewEnrollStore(cfg.EnrollDatabase)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("enroll store: %w", err)
	}

	h := &Hub{
		cfg:      cfg,
		roster:   roster,
		enroll:   enroll,
		sessions: make(map[string]*Session),
		ctx:      ctx,
		cancel:   cancel,
	}
	h.router = NewRouter(&h.cfg)
	return h, nil
}

// Roster exposes the agent roster for the CLI subcommands.
func (h *Hub) Roster() *Roster { return h.roster }

// Enroll exposes the enrollment store for the CLI subcommands.
func (h *Hub) Enroll() *EnrollStore { return h.enroll }

// Fingerprint returns the hex SHA-256 of the hub's TLS certificate, which is
// what agents pin. Empty when TLS is disabled.
func (h *Hub) Fingerprint() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.fingerprint
}

// Subscribe registers a callback for requests the hub emits, currently
// Discord sends. It matches the pattern the other endpoints already use so
// client wiring stays uniform.
func (h *Hub) Subscribe(ctx context.Context, onMessage func(interface{}) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subscribers = append(h.subscribers, onMessage)
	return nil
}

// IsConnected reports whether the listener is up.
func (h *Hub) IsConnected() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.isConnected
}

// EnsureCertificate loads or creates the hub's TLS certificate without
// starting the listener, so the CLI can print a join code containing the
// fingerprint before the hub has ever been run.
func (h *Hub) EnsureCertificate() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.fingerprint != "" {
		return nil
	}
	_, fingerprint, err := certificateFor(&h.cfg)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	h.fingerprint = fingerprint
	return nil
}

// Connect starts the listener.
func (h *Hub) Connect(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.isConnected {
		return nil
	}

	tlsConfig, fingerprint, err := certificateFor(&h.cfg)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	h.fingerprint = fingerprint

	mux := http.NewServeMux()
	mux.HandleFunc("/relay/v1", h.handleAgent)

	h.server = &http.Server{
		Addr:              h.cfg.Listen,
		Handler:           mux,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", h.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", h.cfg.Listen, err)
	}

	h.listener = ln

	if tlsConfig == nil {
		tlog.Warnf("[hub] tls_mode is none - agent traffic and tokens are sent in the clear. Only do this on a private network")
	}

	go func() {
		var err error
		if tlsConfig != nil {
			err = h.server.ServeTLS(ln, "", "")
		} else {
			err = h.server.Serve(ln)
		}
		if err != nil && err != http.ErrServerClosed {
			tlog.Errorf("[hub] serve failed: %s", err)
		}
		h.mu.Lock()
		h.isConnected = false
		h.mu.Unlock()
	}()

	h.isConnected = true
	tlog.Infof("[hub] listening on %s, %d agents authorized", h.cfg.Listen, len(h.roster.Entries()))
	if fingerprint != "" {
		tlog.Infof("[hub] certificate fingerprint %s", fingerprint)
	}
	return nil
}

// Addr returns the address the hub is actually listening on, which differs
// from the configured value when the port was left to the OS.
func (h *Hub) Addr() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.listener == nil {
		return h.cfg.Listen
	}
	return h.listener.Addr().String()
}

// AdvertiseAddress is where agents should be told to dial. It differs from
// Addr, which is the local bind address and may be a wildcard.
func (h *Hub) AdvertiseAddress() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.cfg.AdvertiseAddr != "" {
		return h.cfg.AdvertiseAddr
	}
	return h.cfg.Listen
}

// Disconnect stops the listener and closes every session.
func (h *Hub) Disconnect(ctx context.Context) error {
	h.mu.Lock()
	server := h.server
	sessions := make([]*Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.sessions = make(map[string]*Session)
	h.isConnected = false
	h.mu.Unlock()

	for _, s := range sessions {
		s.Close()
	}
	h.cancel()

	if server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	HandshakeTimeout: 10 * time.Second,
	// Agents are not browsers; there is no origin to check and no cookie auth
	// to protect, so cross-origin rules do not apply. Authentication is the
	// bearer token in the Hello frame.
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (h *Hub) handleAgent(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		tlog.Debugf("[hub] upgrade failed: %s", err)
		return
	}

	// Authenticate before allocating a session, and before the agent can send
	// anything else.
	conn.SetReadLimit(relay.MaxFrameSize)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	frame := &relay.Frame{}
	if err := conn.ReadJSON(frame); err != nil {
		tlog.Debugf("[hub] read hello failed: %s", err)
		conn.Close()
		return
	}
	// An agent that has a code but no token yet enrolls instead of connecting.
	// Enrollment is a one-shot exchange: the connection closes either way.
	if frame.Type == relay.FrameEnroll {
		h.handleEnroll(conn, frame, r.RemoteAddr)
		return
	}

	if frame.Type != relay.FrameHello {
		tlog.Debugf("[hub] first frame was %s, expected hello", frame.Type)
		conn.Close()
		return
	}

	hello := &relay.Hello{}
	if err := frame.Decode(hello); err != nil {
		tlog.Debugf("[hub] decode hello failed: %s", err)
		conn.Close()
		return
	}

	session := newSession("", "", conn, h.cfg.QueueSize)

	if hello.ProtocolVersion != relay.ProtocolVersion {
		tlog.Warnf("[hub] agent %s speaks protocol %d, hub speaks %d", hello.ServerKey, hello.ProtocolVersion, relay.ProtocolVersion)
		session.sendError(relay.ErrCodeVersionMismatch,
			fmt.Sprintf("hub speaks protocol %d, agent speaks %d - upgrade the older side", relay.ProtocolVersion, hello.ProtocolVersion))
		return
	}

	entry, err := h.roster.Authenticate(hello.ServerKey, hello.Token)
	if err != nil {
		// Log the claimed key and the remote address, never the token.
		tlog.Warnf("[hub] rejected agent claiming %q from %s: %s", sanitize.ServerKey(hello.ServerKey), r.RemoteAddr, err)
		session.sendError(relay.ErrCodeUnauthorized, "token not accepted")
		return
	}

	session.serverKey = entry.ServerKey
	session.shortName = entry.ShortName

	welcome, err := relay.NewFrame(relay.FrameWelcome, &relay.Welcome{
		ProtocolVersion: relay.ProtocolVersion,
		ServerKey:       entry.ServerKey,
		ShortName:       entry.ShortName,
		HeartbeatSecs:   h.cfg.HeartbeatSecs,
	})
	if err != nil {
		session.Close()
		return
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(welcome); err != nil {
		tlog.Debugf("[hub] send welcome failed: %s", err)
		session.Close()
		return
	}

	h.addSession(session)
	h.roster.MarkSeen(entry.ServerKey)
	tlog.Infof("[hub] agent %s connected from %s", session, r.RemoteAddr)

	go session.writeLoop()
	session.readLoop(h.ctx, h.onFrame)

	h.removeSession(session)
	tlog.Infof("[hub] agent %s disconnected after %s (%d dropped)", session, time.Since(session.connectedAt).Round(time.Second), session.Dropped())
}

// handleEnroll exchanges a valid enrollment code for a durable token.
//
// The agent is not yet in the roster, so this runs before any session exists
// and always ends with the connection closed. The agent reconnects normally
// with the token it was issued.
func (h *Hub) handleEnroll(conn *websocket.Conn, frame *relay.Frame, remoteAddr string) {
	defer conn.Close()

	session := newSession("", "", conn, 1)

	req := &relay.Enroll{}
	if err := frame.Decode(req); err != nil {
		tlog.Debugf("[hub] decode enroll failed: %s", err)
		return
	}
	if req.ProtocolVersion != relay.ProtocolVersion {
		session.sendError(relay.ErrCodeVersionMismatch,
			fmt.Sprintf("hub speaks protocol %d, agent speaks %d - upgrade the older side", relay.ProtocolVersion, req.ProtocolVersion))
		return
	}

	entry, err := h.enroll.Redeem(req.Code)
	if err != nil {
		tlog.Warnf("[hub] enrollment from %s rejected: %s", remoteAddr, err)
		// Deliberately vague: distinguishing "unknown" from "expired" would
		// tell a guesser when they had found a real code.
		session.sendError(relay.ErrCodeEnrollment, "enrollment code was not accepted")
		return
	}

	token, err := h.roster.Add(entry.ServerKey, entry.ShortName)
	if err != nil {
		tlog.Warnf("[hub] enrollment for %s failed: %s", entry.ServerKey, err)
		session.sendError(relay.ErrCodeEnrollment, err.Error())
		return
	}

	reply, err := relay.NewFrame(relay.FrameEnrolled, &relay.Enrolled{
		ServerKey: entry.ServerKey,
		ShortName: entry.ShortName,
		Token:     token,
	})
	if err != nil {
		session.sendError(relay.ErrCodeEnrollment, "could not issue credentials")
		return
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(reply); err != nil {
		// The agent never received the token but the roster entry exists. Roll
		// it back so the operator can reissue a code rather than being told
		// the server already exists.
		tlog.Warnf("[hub] enrolled %s but could not deliver the token, rolling back: %s", entry.ServerKey, err)
		if removeErr := h.roster.Remove(entry.ServerKey); removeErr != nil {
			tlog.Warnf("[hub] rollback of %s failed: %s", entry.ServerKey, removeErr)
		}
		return
	}

	tlog.Infof("[hub] enrolled %s (%s) from %s", entry.ServerKey, entry.ShortName, remoteAddr)
}

func (h *Hub) addSession(s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// One session per server key. A reconnect after an unclean disconnect
	// would otherwise leave a zombie session receiving half the traffic.
	if existing, ok := h.sessions[s.serverKey]; ok {
		tlog.Infof("[hub] agent %s reconnected, closing previous session", s.serverKey)
		go existing.Close()
	}
	h.sessions[s.serverKey] = s
}

func (h *Hub) removeSession(s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Only remove if it is still the current session; a newer one may have
	// already replaced it.
	if current, ok := h.sessions[s.serverKey]; ok && current == s {
		delete(h.sessions, s.serverKey)
	}
}

func (h *Hub) onFrame(s *Session, frame *relay.Frame) {
	switch frame.Type {
	case relay.FrameEvent:
		event := &relay.Event{}
		if err := frame.Decode(event); err != nil {
			tlog.Warnf("[hub] bad event from %s: %s", s.serverKey, err)
			return
		}
		// The agent does not get to say who it is. Identity comes from the
		// token that authenticated this session.
		event.Origin = s.serverKey
		event.OriginName = s.shortName
		h.Publish(event)

	case relay.FrameStatus:
		status := &relay.Status{}
		if err := frame.Decode(status); err != nil {
			tlog.Debugf("[hub] bad status from %s: %s", s.serverKey, err)
			return
		}
		s.playerCount.Store(int64(status.PlayerCount))
		s.sourceUp.Store(status.SourceUp)

	default:
		tlog.Debugf("[hub] ignoring %s frame from %s", frame.Type, s.serverKey)
	}
}

// Publish routes an event. Agents reach it through onFrame; the hub's own
// Discord listener calls it directly.
func (h *Hub) Publish(event *relay.Event) {
	// Sanitize on arrival, once, so nothing downstream has to remember to.
	event.Name = sanitize.Name(event.Name)
	event.Message = sanitize.Message(event.Message)

	connected := h.connectedKeys()

	plan, err := h.router.Route(event, connected)
	if err != nil {
		switch {
		case IsDuplicate(err), IsChannelDisabled(err):
			tlog.Debugf("[hub] dropped event from %s on %s: %s", event.Origin, event.Channel, err)
		default:
			tlog.Warnf("[hub] cannot route event from %s on %s: %s", event.Origin, event.Channel, err)
		}
		return
	}
	if plan.IsEmpty() {
		return
	}

	if plan.DiscordChannelID != "" {
		h.notify(request.DiscordSend{
			Ctx:       h.ctx,
			ChannelID: plan.DiscordChannelID,
			Message:   plan.DiscordMessage,
		})
	}

	if len(plan.Targets) == 0 {
		return
	}

	// Mark the relay before fan-out so a returning echo is recognizable.
	outbound := *event
	outbound.Hop = event.Hop + 1

	frame, err := relay.NewFrame(relay.FrameEvent, &outbound)
	if err != nil {
		tlog.Warnf("[hub] encode event failed: %s", err)
		return
	}

	// One frame value shared by every target: sessions only read it, and
	// encoding once keeps fan-out cheap as the server count grows.
	for _, key := range plan.Targets {
		session := h.session(key)
		if session == nil {
			continue
		}
		session.Send(frame)
	}

	tlog.Infof("[hub] %s/%s from %s relayed to %d server(s)", event.Channel, event.Name, event.Origin, len(plan.Targets))
}

func (h *Hub) notify(req interface{}) {
	h.mu.RLock()
	subscribers := make([]func(interface{}) error, len(h.subscribers))
	copy(subscribers, h.subscribers)
	h.mu.RUnlock()

	for i, s := range subscribers {
		if err := s(req); err != nil {
			tlog.Warnf("[hub->subscriber %d] failed: %s", i, err)
		}
	}
}

func (h *Hub) session(serverKey string) *Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[serverKey]
}

func (h *Hub) connectedKeys() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	keys := make([]string, 0, len(h.sessions))
	for key := range h.sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// PlayerCount totals the online players every connected agent last reported.
func (h *Hub) PlayerCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	total := 0
	for _, s := range h.sessions {
		total += s.PlayerCount()
	}
	return total
}

// ConnectedServers describes every attached agent, for status output.
func (h *Hub) ConnectedServers() []ServerStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]ServerStatus, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, ServerStatus{
			ServerKey:   s.ServerKey(),
			ShortName:   s.ShortName(),
			PlayerCount: s.PlayerCount(),
			IsSourceUp:  s.IsSourceUp(),
			ConnectedAt: s.connectedAt,
			Dropped:     s.Dropped(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServerKey < out[j].ServerKey })
	return out
}

// ServerStatus is a snapshot of one connected agent.
type ServerStatus struct {
	ServerKey   string
	ShortName   string
	PlayerCount int
	IsSourceUp  bool
	ConnectedAt time.Time
	Dropped     int64
}

// JoinCode builds the setup blob an operator pastes into an agent's config.
func (h *Hub) JoinCode(serverKey, token string) (string, error) {
	entry, ok := h.roster.Entry(serverKey)
	if !ok {
		return "", fmt.Errorf("agent %s not found", serverKey)
	}

	address := h.cfg.AdvertiseAddr
	if address == "" {
		address = h.cfg.Listen
	}

	code := &relay.JoinCode{
		Address:     address,
		ServerKey:   entry.ServerKey,
		Token:       token,
		Fingerprint: h.Fingerprint(),
	}
	return code.Encode()
}
