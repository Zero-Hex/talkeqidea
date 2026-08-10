package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xackery/talkeq/agent"
	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/hub"
	"github.com/xackery/talkeq/request"
	"github.com/xackery/talkeq/tlog"
)

func TestMain(m *testing.M) {
	tlog.Init(nil, os.Stdout)
	os.Exit(m.Run())
}

// collector records what an endpoint was asked to send.
type collector struct {
	mu      sync.Mutex
	discord []request.DiscordSend
	telnet  []request.TelnetSend
}

func (c *collector) onMessage(raw interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch req := raw.(type) {
	case request.DiscordSend:
		c.discord = append(c.discord, req)
	case request.TelnetSend:
		c.telnet = append(c.telnet, req)
	}
	return nil
}

func (c *collector) telnetLines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.telnet))
	for _, req := range c.telnet {
		out = append(out, req.Message)
	}
	return out
}

func (c *collector) discordMessages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.discord))
	for _, req := range c.discord {
		out = append(out, req.Message)
	}
	return out
}

// waitFor polls until cond holds or the deadline passes. Used instead of a
// fixed sleep so the tests stay fast and do not flake under load.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	// Generous: these are real sockets, and CI under -race is slow. The poll
	// means a passing run still finishes in milliseconds.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// startHub brings up a plaintext hub on an ephemeral port with the standard
// two-channel setup.
func startHub(t *testing.T) (*hub.Hub, *collector) {
	t.Helper()

	relayCfg := &config.Relay{
		Mode: config.ModeHub,
		Hub: config.HubConfig{
			Listen:         "127.0.0.1:0",
			AgentsDatabase: filepath.Join(t.TempDir(), "agents.json"),
			TLSMode:        config.TLSNone,
			HeartbeatSecs:  5,
			Channels: []config.HubChannel{
				{
					Name:             "ooc",
					IsEnabled:        true,
					IsCrossServer:    true,
					DiscordChannelID: "discord-ooc",
					DiscordPattern:   "{{.Name}} **OOC** [{{.OriginName}}]: {{.Message}}",
				},
			},
		},
	}
	if err := relayCfg.Verify(); err != nil {
		t.Fatalf("verify hub config: %s", err)
	}

	h, err := hub.New(context.Background(), relayCfg.Hub)
	if err != nil {
		t.Fatalf("new hub: %s", err)
	}

	sink := &collector{}
	if err := h.Subscribe(context.Background(), sink.onMessage); err != nil {
		t.Fatalf("subscribe: %s", err)
	}
	if err := h.Connect(context.Background()); err != nil {
		t.Fatalf("hub connect: %s", err)
	}
	t.Cleanup(func() { h.Disconnect(context.Background()) })

	return h, sink
}

// startAgent authorizes a server on the hub and connects an agent as it.
func startAgent(t *testing.T, h *hub.Hub, serverKey, shortName string) (*agent.Agent, *collector) {
	t.Helper()

	token, err := h.Roster().Add(serverKey, shortName)
	if err != nil {
		t.Fatalf("roster add %s: %s", serverKey, err)
	}

	relayCfg := &config.Relay{
		Mode: config.ModeAgent,
		Agent: config.AgentConf{
			ServerKey:   serverKey,
			ShortName:   shortName,
			HubAddress:  h.Addr(),
			Token:       token,
			IsPlaintext: true,
			Channels: []config.AgentChannel{
				{
					Name:           "ooc",
					IsEnabled:      true,
					InboundPattern: "emote world 260 {{.Name}} says from {{.OriginName}}, '{{.Message}}'",
				},
			},
		},
	}
	if err := relayCfg.Verify(); err != nil {
		t.Fatalf("verify agent config: %s", err)
	}

	a, err := agent.New(context.Background(), relayCfg.Agent)
	if err != nil {
		t.Fatalf("new agent: %s", err)
	}

	sink := &collector{}
	if err := a.Subscribe(context.Background(), sink.onMessage); err != nil {
		t.Fatalf("agent subscribe: %s", err)
	}
	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("agent connect: %s", err)
	}
	t.Cleanup(func() { a.Disconnect(context.Background()) })

	waitFor(t, serverKey+" to connect", a.IsConnected)
	return a, sink
}

// The scenario from the original request: Soandso OOCs on server 1, it shows
// up in Discord and on server 2, worded to say where it came from.
func TestOOCReachesDiscordAndOtherServer(t *testing.T) {
	h, hubSink := startHub(t)

	server1, sink1 := startAgent(t, h, "server1", "Vanilla")
	_, sink2 := startAgent(t, h, "server2", "Classic")

	server1.Publish("ooc", "Soandso", "Hey does anyone know where Master Claude spawns?")

	waitFor(t, "discord message", func() bool { return len(hubSink.discordMessages()) > 0 })
	waitFor(t, "server2 injection", func() bool { return len(sink2.telnetLines()) > 0 })

	wantDiscord := "Soandso **OOC** [Vanilla]: Hey does anyone know where Master Claude spawns?"
	if got := hubSink.discordMessages(); got[0] != wantDiscord {
		t.Errorf("discord got %q, want %q", got[0], wantDiscord)
	}

	wantTelnet := "emote world 260 Soandso says from Vanilla, 'Hey does anyone know where Master Claude spawns?'"
	if got := sink2.telnetLines(); got[0] != wantTelnet {
		t.Errorf("server2 got %q, want %q", got[0], wantTelnet)
	}

	// The originating server must not receive its own message back.
	time.Sleep(100 * time.Millisecond)
	if lines := sink1.telnetLines(); len(lines) != 0 {
		t.Errorf("server1 received its own message back: %v", lines)
	}
}

// The loop this architecture exists to prevent: server 2's game echoes the
// injected emote back onto its own telnet feed, and the agent must not treat
// that as fresh chat.
func TestInjectedMessageIsNotRelayedBack(t *testing.T) {
	h, hubSink := startHub(t)

	server1, _ := startAgent(t, h, "server1", "Vanilla")
	server2, sink2 := startAgent(t, h, "server2", "Classic")

	server1.Publish("ooc", "Soandso", "hello")
	waitFor(t, "server2 injection", func() bool { return len(sink2.telnetLines()) > 0 })

	discordBefore := len(hubSink.discordMessages())

	// Server 2's game server echoes the emote; its telnet parser extracts the
	// same name and body and tries to publish them.
	if server2.Publish("ooc", "Soandso", "hello") {
		t.Error("agent relayed back a message it had just injected")
	}

	time.Sleep(200 * time.Millisecond)
	if got := len(hubSink.discordMessages()); got != discordBefore {
		t.Errorf("echo reached discord: %d messages, want %d", got, discordBefore)
	}
	if lines := sink2.telnetLines(); len(lines) != 1 {
		t.Errorf("server2 injected %d lines, want 1: %v", len(lines), lines)
	}
}

// Suppression must be one-shot: a second, genuine message with the same words
// still goes through.
func TestEchoSuppressionOnlyConsumesOneMessage(t *testing.T) {
	h, _ := startHub(t)

	server1, _ := startAgent(t, h, "server1", "Vanilla")
	server2, sink2 := startAgent(t, h, "server2", "Classic")

	server1.Publish("ooc", "Soandso", "hi")
	waitFor(t, "server2 injection", func() bool { return len(sink2.telnetLines()) > 0 })

	if server2.Publish("ooc", "Soandso", "hi") {
		t.Fatal("first echo was not suppressed")
	}
	// A different player on server 2 genuinely saying the same thing.
	if !server2.Publish("ooc", "Soandso", "hi") {
		t.Error("a genuine message was suppressed as an echo")
	}
}

func TestAgentWithBadTokenIsRejected(t *testing.T) {
	h, _ := startHub(t)

	relayCfg := &config.Relay{
		Mode: config.ModeAgent,
		Agent: config.AgentConf{
			ServerKey:   "rogue",
			ShortName:   "Rogue",
			HubAddress:  h.Addr(),
			Token:       "not-a-real-token",
			IsPlaintext: true,
			Channels: []config.AgentChannel{
				{Name: "ooc", IsEnabled: true, InboundPattern: "emote world 260 {{.Message}}"},
			},
		},
	}
	if err := relayCfg.Verify(); err != nil {
		t.Fatalf("verify: %s", err)
	}

	a, err := agent.New(context.Background(), relayCfg.Agent)
	if err != nil {
		t.Fatalf("new agent: %s", err)
	}
	defer a.Disconnect(context.Background())

	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %s", err)
	}

	time.Sleep(500 * time.Millisecond)
	if a.IsConnected() {
		t.Error("agent with an invalid token was accepted")
	}
}

// An agent cannot claim to be another server: identity comes from the token.
func TestAgentCannotSpoofOrigin(t *testing.T) {
	h, hubSink := startHub(t)

	token, err := h.Roster().Add("server2", "Classic")
	if err != nil {
		t.Fatalf("roster add: %s", err)
	}

	relayCfg := &config.Relay{
		Mode: config.ModeAgent,
		Agent: config.AgentConf{
			// Claims to be server1 while holding server2's token.
			ServerKey:   "server1",
			ShortName:   "Impostor",
			HubAddress:  h.Addr(),
			Token:       token,
			IsPlaintext: true,
			Channels: []config.AgentChannel{
				{Name: "ooc", IsEnabled: true, InboundPattern: "emote world 260 {{.Message}}"},
			},
		},
	}
	if err := relayCfg.Verify(); err != nil {
		t.Fatalf("verify: %s", err)
	}

	a, err := agent.New(context.Background(), relayCfg.Agent)
	if err != nil {
		t.Fatalf("new agent: %s", err)
	}
	defer a.Disconnect(context.Background())

	sink := &collector{}
	a.Subscribe(context.Background(), sink.onMessage)
	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %s", err)
	}
	waitFor(t, "impostor to connect", a.IsConnected)

	a.Publish("ooc", "Soandso", "spoofed")
	waitFor(t, "discord message", func() bool { return len(hubSink.discordMessages()) > 0 })

	// The hub labels it with the identity that authenticated, not the claim.
	got := hubSink.discordMessages()[0]
	want := "Soandso **OOC** [Classic]: spoofed"
	if got != want {
		t.Errorf("origin = %q, want %q (hub must use the authenticated identity)", got, want)
	}
}

// A relayed name or message must never be able to smuggle a second telnet
// command onto another server's console.
func TestInjectionAttemptIsNeutralized(t *testing.T) {
	h, _ := startHub(t)

	server1, _ := startAgent(t, h, "server1", "Vanilla")
	_, sink2 := startAgent(t, h, "server2", "Classic")

	server1.Publish("ooc", "Soandso", "hello'\nzoneshutdown\n")

	waitFor(t, "server2 injection", func() bool { return len(sink2.telnetLines()) > 0 })

	for _, line := range sink2.telnetLines() {
		for _, r := range line {
			if r == '\n' || r == '\r' {
				t.Fatalf("relayed line contains a line break, allowing command injection: %q", line)
			}
		}
	}
}

func TestPlayerCountAggregates(t *testing.T) {
	h, _ := startHub(t)

	server1, _ := startAgent(t, h, "server1", "Vanilla")
	server2, _ := startAgent(t, h, "server2", "Classic")

	server1.SetPlayerCount(25)
	server2.SetPlayerCount(15)

	// Heartbeat is 5s in the test config; wait for both reports to land.
	waitFor(t, "player counts to arrive", func() bool { return h.PlayerCount() == 40 })

	servers := h.ConnectedServers()
	if len(servers) != 2 {
		t.Fatalf("connected servers = %d, want 2", len(servers))
	}
}
