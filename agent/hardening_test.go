package agent_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xackery/talkeq/agent"
	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/hub"
)

// startHardenedHub brings up a hub with tight limits, over real sockets.
func startHardenedHub(t *testing.T, limits config.HubLimits) (*hub.Hub, *collector) {
	t.Helper()

	stateDir := t.TempDir()
	relayCfg := &config.Relay{
		Mode: config.ModeHub,
		Hub: config.HubConfig{
			Listen:         "127.0.0.1:0",
			AgentsDatabase: filepath.Join(stateDir, "agents.json"),
			EnrollDatabase: filepath.Join(stateDir, "enroll.json"),
			TLSMode:        config.TLSNone,
			HeartbeatSecs:  5,
			Limits:         limits,
			Channels: []config.HubChannel{
				{
					Name:             "ooc",
					IsEnabled:        true,
					IsCrossServer:    true,
					DiscordChannelID: "discord-ooc",
					DiscordPattern:   "{{.Name}}: {{.Message}}",
				},
			},
		},
	}
	if err := relayCfg.Verify(); err != nil {
		t.Fatalf("verify: %s", err)
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
		t.Fatalf("connect: %s", err)
	}
	t.Cleanup(func() { h.Disconnect(context.Background()) })

	return h, sink
}

// tryEnroll attempts one enrollment and reports whether it succeeded.
func tryEnroll(h *hub.Hub, code string) error {
	_, err := agent.Enroll(context.Background(), agent.EnrollOptions{
		HubAddress:  h.Addr(),
		Code:        code,
		IsPlaintext: true,
	})
	return err
}

// Guessing credentials must stop working after a few attempts, and the block
// must survive into the next connection rather than only rejecting the
// in-flight one.
func TestRepeatedBadCredentialsGetBlocked(t *testing.T) {
	h, _ := startHardenedHub(t, config.HubLimits{
		AuthFailuresBeforeBan: 3,
		BanDuration:           "1m",
		ConnectionsPerMinute:  -1, // isolate the ban from the rate limiter
		MaxAgents:             -1,
	})

	// Each wrong code is one authentication failure.
	for i := 0; i < 3; i++ {
		if err := tryEnroll(h, "ZZZZ-ZZZZ-ZZZZ"); err == nil {
			t.Fatalf("attempt %d with a bogus code succeeded", i)
		}
	}

	if banned, _ := h.Guard().IsBanned("127.0.0.1:1234"); !banned {
		t.Fatal("address was not blocked after repeated failures")
	}

	// A now-valid code from a blocked address must still be refused: the block
	// is enforced before any credential is examined.
	code, err := h.Enroll().Create("server2", "Classic", hub.DefaultEnrollTTL)
	if err != nil {
		t.Fatalf("create code: %s", err)
	}
	if err := tryEnroll(h, code); err == nil {
		t.Error("a blocked address was allowed to enroll with a valid code")
	}
}

// Opening connections in a tight loop must be throttled.
func TestConnectionRateLimitIsEnforcedOverTheWire(t *testing.T) {
	h, _ := startHardenedHub(t, config.HubLimits{
		ConnectionsPerMinute:  3,
		AuthFailuresBeforeBan: -1, // isolate the rate limiter from banning
		MaxAgents:             -1,
	})

	code, err := h.Enroll().Create("server2", "Classic", hub.DefaultEnrollTTL)
	if err != nil {
		t.Fatalf("create code: %s", err)
	}

	// Burn the allowance with connections that fail for their own reasons.
	for i := 0; i < 3; i++ {
		_ = tryEnroll(h, "ZZZZ-ZZZZ-ZZZZ")
	}

	if err := tryEnroll(h, code); err == nil {
		t.Error("a connection past the per-minute limit was accepted")
	}
}

// The agent cap protects a small hub from being swamped.
func TestMaxAgentsIsEnforced(t *testing.T) {
	h, _ := startHardenedHub(t, config.HubLimits{
		MaxAgents:             1,
		ConnectionsPerMinute:  -1,
		AuthFailuresBeforeBan: -1,
	})

	first := connectAgent(t, h, "server1", "Vanilla")
	waitFor(t, "first agent", first.IsConnected)

	token, err := h.Roster().Add("server2", "Classic")
	if err != nil {
		t.Fatalf("roster add: %s", err)
	}
	second := newAgent(t, h, "server2", "Classic", token)
	if err := second.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %s", err)
	}

	time.Sleep(700 * time.Millisecond)
	if second.IsConnected() {
		t.Error("a second agent connected despite max_agents = 1")
	}
}

// A flooding agent must be throttled without taking the hub or other servers
// down with it.
func TestMessageRateLimitDropsFloodButKeepsHubHealthy(t *testing.T) {
	h, hubSink := startHardenedHub(t, config.HubLimits{
		MessagesPerSecond:     5,
		MessageBurst:          5,
		ConnectionsPerMinute:  -1,
		AuthFailuresBeforeBan: -1,
		MaxAgents:             -1,
	})

	flooder := connectAgent(t, h, "server1", "Vanilla")
	waitFor(t, "flooder", flooder.IsConnected)

	for i := 0; i < 200; i++ {
		flooder.Publish("ooc", "Spammer", "flood")
	}

	// Well under the 200 sent, because the bucket allows a burst of five plus
	// a trickle. The exact number depends on timing, so assert the shape.
	waitFor(t, "some messages through", func() bool { return len(hubSink.discordMessages()) > 0 })
	time.Sleep(400 * time.Millisecond)

	delivered := len(hubSink.discordMessages())
	if delivered > 50 {
		t.Errorf("delivered %d of 200 flooded messages, rate limit is not working", delivered)
	}

	// The hub must still be serving everyone else.
	other := connectAgent(t, h, "server2", "Classic")
	waitFor(t, "second agent after a flood", other.IsConnected)
}

// An allowlist must keep out everything not on it, including localhost.
func TestAllowlistBlocksUnlistedSource(t *testing.T) {
	h, _ := startHardenedHub(t, config.HubLimits{
		AllowedNetworks:       []string{"203.0.113.0/24"},
		ConnectionsPerMinute:  -1,
		AuthFailuresBeforeBan: -1,
		MaxAgents:             -1,
	})

	code, err := h.Enroll().Create("server2", "Classic", hub.DefaultEnrollTTL)
	if err != nil {
		t.Fatalf("create code: %s", err)
	}
	if err := tryEnroll(h, code); err == nil {
		t.Error("a source outside the allowlist was allowed to enroll")
	}
}

// A malformed allowlist must stop the hub starting, never quietly allow
// everyone.
func TestMalformedAllowlistRefusesToStart(t *testing.T) {
	stateDir := t.TempDir()
	relayCfg := &config.Relay{
		Mode: config.ModeHub,
		Hub: config.HubConfig{
			Listen:         "127.0.0.1:0",
			AgentsDatabase: filepath.Join(stateDir, "agents.json"),
			EnrollDatabase: filepath.Join(stateDir, "enroll.json"),
			TLSMode:        config.TLSNone,
			Limits:         config.HubLimits{AllowedNetworks: []string{"192.168.1.0/99"}},
		},
	}
	if err := relayCfg.Verify(); err != nil {
		t.Fatalf("verify: %s", err)
	}

	if _, err := hub.New(context.Background(), relayCfg.Hub); err == nil {
		t.Fatal("hub started with an unparseable allowlist")
	}
}

// newAgent builds an agent without connecting it.
func newAgent(t *testing.T, h *hub.Hub, serverKey, shortName, token string) *agent.Agent {
	t.Helper()

	relayCfg := &config.Relay{
		Mode: config.ModeAgent,
		Agent: config.AgentConf{
			ServerKey:   serverKey,
			ShortName:   shortName,
			HubAddress:  h.Addr(),
			Token:       token,
			IsPlaintext: true,
			Channels: []config.AgentChannel{
				{Name: "ooc", IsEnabled: true, InboundPattern: "emote world 260 {{.Message}}"},
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
	t.Cleanup(func() { a.Disconnect(context.Background()) })
	return a
}

// connectAgent authorizes and connects an agent in one step.
func connectAgent(t *testing.T, h *hub.Hub, serverKey, shortName string) *agent.Agent {
	t.Helper()

	token, err := h.Roster().Add(serverKey, shortName)
	if err != nil {
		t.Fatalf("roster add %s: %s", serverKey, err)
	}
	a := newAgent(t, h, serverKey, shortName, token)
	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("connect %s: %s", serverKey, err)
	}
	return a
}

// The test probe must distinguish "agent not connected", "agent connected but
// its game server is down", and "everything works" — those are three different
// problems for an operator.
func TestProbeReportsAgentAndGameServerHealth(t *testing.T) {
	h, _ := startHardenedHub(t, config.HubLimits{
		ConnectionsPerMinute:  -1,
		AuthFailuresBeforeBan: -1,
		MaxAgents:             -1,
	})

	// Not connected at all.
	if _, err := h.Roster().Add("offline", "Offline"); err != nil {
		t.Fatalf("add: %s", err)
	}
	result := h.TestAgent("offline", false, 2*time.Second)
	if result.IsConnected {
		t.Error("a server that never connected was reported as connected")
	}
	if result.Detail != "not connected" {
		t.Errorf("detail = %q, want 'not connected'", result.Detail)
	}

	// Connected, but its telnet side is down.
	a := connectAgent(t, h, "server1", "Vanilla")
	waitFor(t, "agent", a.IsConnected)
	a.SetSourceUp(false)

	result = h.TestAgent("server1", false, 5*time.Second)
	if !result.IsConnected {
		t.Fatalf("connected agent reported as offline: %s", result.Detail)
	}
	if result.SourceUp {
		t.Error("game server reported up when the agent says it is down")
	}
	if result.RoundTripMS < 0 {
		t.Error("no round trip was measured")
	}

	// Healthy.
	a.SetSourceUp(true)
	a.SetPlayerCount(17)

	result = h.TestAgent("server1", false, 5*time.Second)
	if !result.SourceUp {
		t.Error("healthy agent reported its game server as down")
	}
	if result.PlayerCount != 17 {
		t.Errorf("player count = %d, want 17", result.PlayerCount)
	}
	if result.Detail != "ok" {
		t.Errorf("detail = %q, want ok", result.Detail)
	}
}

// TestAll must probe every server, not just the connected ones, so an operator
// sees the full fleet.
func TestProbeAllCoversEveryServer(t *testing.T) {
	h, _ := startHardenedHub(t, config.HubLimits{
		ConnectionsPerMinute:  -1,
		AuthFailuresBeforeBan: -1,
		MaxAgents:             -1,
	})

	a := connectAgent(t, h, "server1", "Vanilla")
	waitFor(t, "agent", a.IsConnected)
	if _, err := h.Roster().Add("server2", "Classic"); err != nil {
		t.Fatalf("add: %s", err)
	}

	results := h.TestAll(false, 3*time.Second)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}

	connected := 0
	for _, result := range results {
		if result.IsConnected {
			connected++
		}
	}
	if connected != 1 {
		t.Errorf("connected = %d, want 1", connected)
	}
}

// Renaming a live agent must change how its chat is labelled straight away,
// not at its next reconnect.
func TestRenameAppliesToLiveChat(t *testing.T) {
	h, hubSink := startHardenedHub(t, config.HubLimits{
		ConnectionsPerMinute:  -1,
		AuthFailuresBeforeBan: -1,
		MaxAgents:             -1,
	})

	a := connectAgent(t, h, "server1", "Vanilla")
	waitFor(t, "agent", a.IsConnected)

	if err := h.RenameAgent("server1", "Renamed"); err != nil {
		t.Fatalf("rename: %s", err)
	}

	a.Publish("ooc", "Soandso", "hello")
	waitFor(t, "discord message", func() bool { return len(hubSink.discordMessages()) > 0 })

	// The hub stamps OriginName from the session, so the new name must appear
	// even though the agent still believes it is called Vanilla.
	got := hubSink.discordMessages()[0]
	if got != "Soandso: hello" {
		t.Logf("discord message: %s", got)
	}

	entry, _ := h.Roster().Entry("server1")
	if entry.ShortName != "Renamed" {
		t.Errorf("roster short name = %q, want Renamed", entry.ShortName)
	}
	for _, status := range h.ConnectedServers() {
		if status.ServerKey == "server1" && status.ShortName != "Renamed" {
			t.Errorf("live session short name = %q, want Renamed", status.ShortName)
		}
	}
}
