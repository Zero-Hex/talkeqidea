// Package agent connects one game server to a TalkEQ hub.
//
// An agent is deliberately thin. It reports what was said on its own server
// and injects what the hub sends back. It holds no Discord credentials, no
// other server's token, and no routing rules — all of that lives on the hub,
// so adding the tenth server is the same amount of work as adding the second.
package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/relay"
	"github.com/xackery/talkeq/request"
	"github.com/xackery/talkeq/sanitize"
	"github.com/xackery/talkeq/tlog"
)

const (
	writeWait = 10 * time.Second
	pongWait  = 90 * time.Second

	minBackoff = 2 * time.Second
	maxBackoff = 60 * time.Second

	// outboundQueue is how many locally observed messages are held while the
	// hub is unreachable. Sized for a busy server through a short outage;
	// beyond that, dropping old chat is the right call.
	outboundQueue = 512
)

// Agent is a game server's connection to the hub.
type Agent struct {
	mu  sync.RWMutex
	cfg config.AgentConf

	conn        *websocket.Conn
	out         chan *relay.Frame
	subscribers []func(interface{}) error

	echo *echoCache
	// version is reported in test replies so a hub can spot a stale agent.
	version string

	isConnected atomic.Bool
	playerCount atomic.Int64
	sourceUp    atomic.Bool
	dropped     atomic.Int64

	ctx    context.Context
	cancel context.CancelFunc
}

// SetVersion records the build version reported to the hub.
func (a *Agent) SetVersion(version string) { a.version = version }

// New creates an agent from configuration.
func New(ctx context.Context, cfg config.AgentConf) (*Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	return &Agent{
		cfg:    cfg,
		out:    make(chan *relay.Frame, outboundQueue),
		echo:   newEchoCache(15 * time.Second),
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Subscribe registers a callback for requests the agent emits, currently
// telnet sends.
func (a *Agent) Subscribe(ctx context.Context, onMessage func(interface{}) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subscribers = append(a.subscribers, onMessage)
	return nil
}

// IsConnected reports whether the hub link is up.
func (a *Agent) IsConnected() bool { return a.isConnected.Load() }

// ServerKey returns this agent's identity.
func (a *Agent) ServerKey() string { return a.cfg.ServerKey }

// SetPlayerCount records the local online count so the hub can total players
// across every server.
//
// A change is pushed immediately rather than held for the next heartbeat: the
// count is polled about once a minute, so waiting for a tick would leave the
// Discord status stale for up to another heartbeat interval.
func (a *Agent) SetPlayerCount(n int) {
	if a.playerCount.Swap(int64(n)) == int64(n) {
		return
	}
	a.queueStatus()
}

// SetSourceUp records whether the local game server connection is healthy.
func (a *Agent) SetSourceUp(isUp bool) {
	if a.sourceUp.Swap(isUp) == isUp {
		return
	}
	a.queueStatus()
}

// queueStatus schedules a status report on the outbound queue.
func (a *Agent) queueStatus() {
	frame, err := relay.NewFrame(relay.FrameStatus, &relay.Status{
		PlayerCount: int(a.playerCount.Load()),
		SourceUp:    a.sourceUp.Load(),
	})
	if err != nil {
		return
	}
	a.enqueue(frame)
}

// Connect starts the connection loop. It returns immediately; the loop
// reconnects on its own for as long as the context lives.
func (a *Agent) Connect(ctx context.Context) error {
	go a.loop(ctx)
	return nil
}

// Disconnect tears the link down.
func (a *Agent) Disconnect(ctx context.Context) error {
	a.cancel()

	a.mu.Lock()
	conn := a.conn
	a.conn = nil
	a.mu.Unlock()

	if conn != nil {
		conn.Close()
	}
	a.isConnected.Store(false)
	return nil
}

// Publish reports a locally observed message to the hub.
//
// It returns false when the message was suppressed as an echo of something
// this agent injected, which lets the caller log the distinction.
func (a *Agent) Publish(channel, name, message string) bool {
	name = sanitize.Name(name)
	message = sanitize.Message(message)
	if name == "" || message == "" {
		return false
	}

	if a.echo.isEcho(name, message) {
		tlog.Debugf("[agent] suppressed echo of relayed message from %s", name)
		return false
	}

	event := relay.NewEvent(channel, name, message)
	event.Origin = a.cfg.ServerKey
	event.OriginName = a.cfg.ShortName

	frame, err := relay.NewFrame(relay.FrameEvent, event)
	if err != nil {
		tlog.Warnf("[agent] encode event failed: %s", err)
		return false
	}

	a.enqueue(frame)
	return true
}

// enqueue adds a frame to the outbound queue without blocking, discarding the
// oldest entry when full. A stalled hub connection must never back-pressure
// into the telnet reader, or the agent would stop parsing its own game server.
func (a *Agent) enqueue(frame *relay.Frame) {
	select {
	case a.out <- frame:
		return
	default:
	}

	select {
	case <-a.out:
		a.dropped.Add(1)
	default:
	}

	select {
	case a.out <- frame:
	default:
		a.dropped.Add(1)
	}
}

func (a *Agent) loop(ctx context.Context) {
	backoff := minBackoff

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.ctx.Done():
			return
		default:
		}

		err := a.session(ctx)
		if err != nil {
			tlog.Warnf("[agent] hub connection failed: %s", err)
		}
		a.isConnected.Store(false)

		select {
		case <-ctx.Done():
			return
		case <-a.ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Exponential backoff, capped. A hub restart brings every agent back
		// at a slightly different moment because each one's backoff started
		// when its own connection dropped.
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}

		if a.isConnected.Load() {
			backoff = minBackoff
		}
	}
}

// session runs one connection from dial to disconnect.
func (a *Agent) session(ctx context.Context) error {
	conn, err := a.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	welcome, err := a.handshake(conn)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
	a.isConnected.Store(true)

	tlog.Infof("[agent] connected to hub as %s (%s)", welcome.ServerKey, welcome.ShortName)

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	heartbeat := welcome.HeartbeatSecs
	if heartbeat < 5 {
		heartbeat = 30
	}

	go a.writeLoop(sessionCtx, conn, time.Duration(heartbeat)*time.Second)
	return a.readLoop(sessionCtx, conn)
}

func (a *Agent) dial(ctx context.Context) (*websocket.Conn, error) {
	scheme := "wss"
	if a.cfg.IsPlaintext {
		scheme = "ws"
	}

	address := a.cfg.HubAddress
	if strings.HasPrefix(address, ":") {
		address = "127.0.0.1" + address
	}
	endpoint := url.URL{Scheme: scheme, Host: address, Path: "/relay/v1"}

	dialer := &websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}
	if !a.cfg.IsPlaintext {
		tlsConfig, err := a.tlsConfig()
		if err != nil {
			return nil, err
		}
		dialer.TLSClientConfig = tlsConfig
	}

	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	conn, resp, err := dialer.DialContext(dialCtx, endpoint.String(), nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("dial %s: %w (http %d)", endpoint.String(), err, resp.StatusCode)
		}
		return nil, fmt.Errorf("dial %s: %w", endpoint.String(), err)
	}
	return conn, nil
}

// tlsConfig builds the client TLS settings, pinning the hub's certificate when
// a fingerprint is configured.
//
// Pinning replaces CA verification rather than supplementing it: a self-signed
// hub certificate has no chain to verify, and against an attacker who can mint
// a certificate from any public CA, a pin is the stronger check.
func (a *Agent) tlsConfig() (*tls.Config, error) {
	if a.cfg.Fingerprint == "" {
		// No pin configured: the hub is expected to present a publicly trusted
		// certificate, so use normal verification.
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}

	want := strings.ToLower(strings.ReplaceAll(a.cfg.Fingerprint, ":", ""))
	if _, err := hex.DecodeString(want); err != nil || len(want) != 64 {
		return nil, fmt.Errorf("fingerprint must be a hex sha-256 of the hub certificate")
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Verification is done by VerifyPeerCertificate below. Chain building
		// would fail for a self-signed hub certificate, which is the normal
		// case here.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("hub presented no certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			got := hex.EncodeToString(sum[:])
			if got != want {
				return fmt.Errorf("hub certificate fingerprint %s does not match the pinned %s", got, want)
			}
			return nil
		},
	}, nil
}

func (a *Agent) handshake(conn *websocket.Conn) (*relay.Welcome, error) {
	channels := make([]string, 0, len(a.cfg.Channels))
	for _, ch := range a.cfg.Channels {
		channels = append(channels, ch.Name)
	}

	hello, err := relay.NewFrame(relay.FrameHello, &relay.Hello{
		ProtocolVersion: relay.ProtocolVersion,
		ServerKey:       a.cfg.ServerKey,
		Token:           a.cfg.Token,
		Channels:        channels,
	})
	if err != nil {
		return nil, fmt.Errorf("encode hello: %w", err)
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(hello); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}

	conn.SetReadLimit(relay.MaxFrameSize)
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))

	frame := &relay.Frame{}
	if err := conn.ReadJSON(frame); err != nil {
		return nil, fmt.Errorf("read welcome: %w", err)
	}

	if frame.Type == relay.FrameError {
		relayErr := &relay.Error{}
		if decodeErr := frame.Decode(relayErr); decodeErr != nil {
			return nil, fmt.Errorf("hub rejected the connection")
		}
		return nil, fmt.Errorf("hub rejected the connection: %s (%s)", relayErr.Message, relayErr.Code)
	}
	if frame.Type != relay.FrameWelcome {
		return nil, fmt.Errorf("expected welcome, got %s", frame.Type)
	}

	welcome := &relay.Welcome{}
	if err := frame.Decode(welcome); err != nil {
		return nil, fmt.Errorf("decode welcome: %w", err)
	}
	return welcome, nil
}

func (a *Agent) writeLoop(ctx context.Context, conn *websocket.Conn, heartbeat time.Duration) {
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	// Report once immediately so the hub's player total is right from the
	// moment an agent attaches, rather than after the first tick.
	a.sendStatus(conn)

	for {
		select {
		case <-ctx.Done():
			return

		case frame := <-a.out:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteJSON(frame); err != nil {
				tlog.Debugf("[agent] write failed: %s", err)
				conn.Close()
				return
			}

		case <-ticker.C:
			if !a.sendStatus(conn) {
				return
			}
		}
	}
}

// sendStatus reports this agent's health to the hub. It doubles as the
// application-level heartbeat: a write failure here surfaces a dead link that
// a silent TCP connection would otherwise hide.
func (a *Agent) sendStatus(conn *websocket.Conn) bool {
	status, err := relay.NewFrame(relay.FrameStatus, &relay.Status{
		PlayerCount: int(a.playerCount.Load()),
		SourceUp:    a.sourceUp.Load(),
	})
	if err != nil {
		return true
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(status); err != nil {
		tlog.Debugf("[agent] heartbeat failed: %s", err)
		conn.Close()
		return false
	}
	return true
}

func (a *Agent) readLoop(ctx context.Context, conn *websocket.Conn) error {
	conn.SetReadLimit(relay.MaxFrameSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPingHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetWriteDeadline(time.Now().Add(writeWait))
		return conn.WriteMessage(websocket.PongMessage, nil)
	})

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		frame := &relay.Frame{}
		if err := conn.ReadJSON(frame); err != nil {
			return fmt.Errorf("read: %w", err)
		}
		conn.SetReadDeadline(time.Now().Add(pongWait))

		switch frame.Type {
		case relay.FrameEvent:
			event := &relay.Event{}
			if err := frame.Decode(event); err != nil {
				tlog.Warnf("[agent] bad event from hub: %s", err)
				continue
			}
			a.inject(event)

		case relay.FrameTest:
			a.handleTest(frame)

		case relay.FrameError:
			relayErr := &relay.Error{}
			if err := frame.Decode(relayErr); err == nil {
				tlog.Warnf("[agent] hub reported %s: %s", relayErr.Code, relayErr.Message)
			}

		default:
			tlog.Debugf("[agent] ignoring %s frame", frame.Type)
		}
	}
}

// handleTest answers a liveness probe from the hub.
//
// The reply reports the telnet side's health, not just that this process is
// running: an agent whose game server connection is down is exactly the case
// an operator is checking for, and it looks identical from the hub otherwise.
func (a *Agent) handleTest(frame *relay.Frame) {
	req := &relay.Test{}
	if err := frame.Decode(req); err != nil {
		tlog.Debugf("[agent] bad test frame: %s", err)
		return
	}

	reply := &relay.TestReply{
		ID:          req.ID,
		SourceUp:    a.sourceUp.Load(),
		PlayerCount: int(a.playerCount.Load()),
		Version:     a.version,
	}

	if req.IsEcho {
		reply.InjectedOK, reply.Detail = a.injectTestLine(req.Message)
	}

	out, err := relay.NewFrame(relay.FrameTestReply, reply)
	if err != nil {
		return
	}
	a.enqueue(out)
}

// injectTestLine writes a visible line into the local game server so an
// operator can confirm the whole path, not just the relay link.
func (a *Agent) injectTestLine(message string) (bool, string) {
	if message == "" {
		message = "TalkEQ relay test"
	}

	ch, ok := a.cfg.Channel("ooc")
	if !ok || !ch.IsEnabled {
		return false, "no enabled ooc channel to inject into"
	}

	event := relay.NewEvent("ooc", "TalkEQ", sanitize.Message(message))
	event.Origin = relay.OriginDiscord
	event.OriginName = "Hub"

	line, err := relay.Render(ch.InboundTemplate(), event, "")
	if err != nil {
		return false, "render inbound pattern: " + err.Error()
	}
	if !sanitize.IsSafeTelnetLine(line) {
		return false, "rendered line was unsafe to send"
	}

	// Remembered like any injection, so the echo coming back off telnet is not
	// relayed onward as if a player had said it.
	a.echo.remember(event)

	a.mu.RLock()
	subscribers := make([]func(interface{}) error, len(a.subscribers))
	copy(subscribers, a.subscribers)
	a.mu.RUnlock()

	if len(subscribers) == 0 {
		return false, "no telnet connection is wired up"
	}

	for _, s := range subscribers {
		if err := s(request.TelnetSend{Ctx: a.ctx, Message: line}); err != nil {
			return false, err.Error()
		}
	}
	return true, ""
}

// inject writes a relayed message into the local game server.
func (a *Agent) inject(event *relay.Event) {
	if event.Origin == a.cfg.ServerKey {
		// The hub already excludes the origin; this is a cheap backstop in
		// case a future routing change forgets to.
		return
	}

	ch, ok := a.cfg.Channel(event.Channel)
	if !ok || !ch.IsEnabled {
		tlog.Debugf("[agent] no local channel for %s, ignoring", event.Channel)
		return
	}

	event.Name = sanitize.Name(event.Name)
	event.Message = sanitize.Message(event.Message)
	if event.Name == "" || event.Message == "" {
		return
	}

	line, err := relay.Render(ch.InboundTemplate(), event, "")
	if err != nil {
		tlog.Warnf("[agent] render %s inbound pattern failed: %s", ch.Name, err)
		return
	}

	// Last line of defense before the command reaches the console. Fields were
	// sanitized above; this catches a pattern that itself contains a newline.
	if !sanitize.IsSafeTelnetLine(line) {
		tlog.Warnf("[agent] refusing to send unsafe telnet line for channel %s", ch.Name)
		return
	}

	// Remember before sending. The game echoes the emote back on the same
	// connection, sometimes before the write call has even returned.
	a.echo.remember(event)

	req := request.TelnetSend{Ctx: a.ctx, Message: line}

	a.mu.RLock()
	subscribers := make([]func(interface{}) error, len(a.subscribers))
	copy(subscribers, a.subscribers)
	a.mu.RUnlock()

	for i, s := range subscribers {
		if err := s(req); err != nil {
			tlog.Warnf("[agent->telnet subscriber %d] failed: %s", i, err)
			continue
		}
		tlog.Infof("[hub->%s] %s", a.cfg.ServerKey, line)
	}
}
