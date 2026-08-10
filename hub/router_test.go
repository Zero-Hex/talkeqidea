package hub

import (
	"testing"
	"time"

	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/relay"
)

func testHubConfig(t *testing.T) *config.HubConfig {
	t.Helper()
	cfg := &config.HubConfig{
		Channels: []config.HubChannel{
			{
				Name:             "ooc",
				IsEnabled:        true,
				IsCrossServer:    true,
				DiscordChannelID: "discord-ooc",
				DiscordPattern:   "{{.Name}} **OOC** [{{.OriginName}}]: {{.Message}}",
			},
			{
				Name:             "auction",
				IsEnabled:        true,
				IsCrossServer:    false,
				DiscordChannelID: "discord-auction",
				DiscordPattern:   "{{.Name}} auctions: {{.Message}}",
			},
			{
				Name:          "guild",
				IsEnabled:     false,
				IsCrossServer: true,
			},
		},
	}
	relayCfg := &config.Relay{Mode: config.ModeHub, Hub: *cfg}
	if err := relayCfg.Verify(); err != nil {
		t.Fatalf("verify config: %s", err)
	}
	return &relayCfg.Hub
}

func newTestEvent(origin, channel, name, message string) *relay.Event {
	e := relay.NewEvent(channel, name, message)
	e.Origin = origin
	e.OriginName = origin
	return e
}

// The headline behavior: one OOC line on server1 reaches Discord once and
// every other server exactly once, and never comes back to server1.
func TestRouteFansOutExcludingOrigin(t *testing.T) {
	r := NewRouter(testHubConfig(t))

	e := newTestEvent("server1", "ooc", "Soandso", "where does Master Claude spawn?")
	plan, err := r.Route(e, []string{"server1", "server2", "server3"})
	if err != nil {
		t.Fatalf("route: %s", err)
	}

	if plan.DiscordChannelID != "discord-ooc" {
		t.Errorf("discord channel = %q, want discord-ooc", plan.DiscordChannelID)
	}
	want := "Soandso **OOC** [server1]: where does Master Claude spawn?"
	if plan.DiscordMessage != want {
		t.Errorf("discord message = %q, want %q", plan.DiscordMessage, want)
	}

	if len(plan.Targets) != 2 {
		t.Fatalf("targets = %v, want 2 servers", plan.Targets)
	}
	for _, target := range plan.Targets {
		if target == "server1" {
			t.Fatalf("event was routed back to its origin: %v", plan.Targets)
		}
	}
}

func TestRouteHonorsCrossServerFlag(t *testing.T) {
	r := NewRouter(testHubConfig(t))

	e := newTestEvent("server1", "auction", "Soandso", "WTS sword")
	plan, err := r.Route(e, []string{"server1", "server2"})
	if err != nil {
		t.Fatalf("route: %s", err)
	}

	if len(plan.Targets) != 0 {
		t.Errorf("auction is not cross_server, but routed to %v", plan.Targets)
	}
	if plan.DiscordChannelID != "discord-auction" {
		t.Errorf("auction should still reach discord, got %q", plan.DiscordChannelID)
	}
}

// A message typed in Discord goes to the game servers but is not echoed back
// into Discord, where it is already visible.
func TestRouteFromDiscordDoesNotEchoToDiscord(t *testing.T) {
	r := NewRouter(testHubConfig(t))

	e := newTestEvent(relay.OriginDiscord, "ooc", "SomeUser", "hello from discord")
	plan, err := r.Route(e, []string{"server1", "server2"})
	if err != nil {
		t.Fatalf("route: %s", err)
	}

	if plan.DiscordChannelID != "" {
		t.Errorf("discord-origin event was echoed back to discord")
	}
	if len(plan.Targets) != 2 {
		t.Errorf("targets = %v, want both servers", plan.Targets)
	}
}

// Discord-to-game works even for a channel that does not link servers to each
// other, since those are separate features.
func TestRouteFromDiscordIgnoresCrossServerFlag(t *testing.T) {
	r := NewRouter(testHubConfig(t))

	e := newTestEvent(relay.OriginDiscord, "auction", "SomeUser", "WTB sword")
	plan, err := r.Route(e, []string{"server1"})
	if err != nil {
		t.Fatalf("route: %s", err)
	}
	if len(plan.Targets) != 1 {
		t.Errorf("targets = %v, want server1", plan.Targets)
	}
}

func TestRouteRejectsDuplicate(t *testing.T) {
	r := NewRouter(testHubConfig(t))

	e := newTestEvent("server1", "ooc", "Soandso", "hello")
	if _, err := r.Route(e, []string{"server2"}); err != nil {
		t.Fatalf("first route: %s", err)
	}

	_, err := r.Route(e, []string{"server2"})
	if err == nil {
		t.Fatal("second route of the same event ID succeeded, want duplicate")
	}
	if !IsDuplicate(err) {
		t.Errorf("err = %v, want duplicate", err)
	}
}

// The hop counter is the backstop against a relayed message being fed back in
// as if it were fresh chat.
func TestRouteRejectsAlreadyRelayed(t *testing.T) {
	r := NewRouter(testHubConfig(t))

	e := newTestEvent("server1", "ooc", "Soandso", "hello")
	e.Hop = 1

	if _, err := r.Route(e, []string{"server2"}); err == nil {
		t.Fatal("routed an event that had already been relayed")
	}
}

func TestRouteRejectsDisabledAndUnknownChannels(t *testing.T) {
	r := NewRouter(testHubConfig(t))

	_, err := r.Route(newTestEvent("server1", "guild", "Soandso", "hi"), []string{"server2"})
	if !IsChannelDisabled(err) {
		t.Errorf("disabled channel err = %v, want channel disabled", err)
	}

	_, err = r.Route(newTestEvent("server1", "nosuchchannel", "Soandso", "hi"), []string{"server2"})
	if err == nil || IsChannelDisabled(err) {
		t.Errorf("unknown channel err = %v, want a routing error", err)
	}
}

func TestRouteRejectsInvalidEvents(t *testing.T) {
	r := NewRouter(testHubConfig(t))

	tests := []struct {
		name  string
		event *relay.Event
	}{
		{"no name", newTestEvent("server1", "ooc", "", "hi")},
		{"no message", newTestEvent("server1", "ooc", "Soandso", "")},
		{"no channel", newTestEvent("server1", "", "Soandso", "hi")},
		{"huge message", newTestEvent("server1", "ooc", "Soandso", string(make([]byte, 5000)))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := r.Route(tt.event, []string{"server2"}); err == nil {
				t.Error("routed an invalid event")
			}
		})
	}
}

// With many servers attached, fan-out must still exclude exactly one.
func TestRouteScalesToManyServers(t *testing.T) {
	r := NewRouter(testHubConfig(t))

	connected := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		connected = append(connected, string(rune('a'+i%26))+string(rune('0'+i/26)))
	}
	origin := connected[7]

	plan, err := r.Route(newTestEvent(origin, "ooc", "Soandso", "hi"), connected)
	if err != nil {
		t.Fatalf("route: %s", err)
	}
	if len(plan.Targets) != len(connected)-1 {
		t.Errorf("targets = %d, want %d", len(plan.Targets), len(connected)-1)
	}
}

func TestSeenCacheRotatesWithoutLosingRecentIDs(t *testing.T) {
	c := newSeenCache(time.Millisecond)

	if c.seenBefore("abc") {
		t.Fatal("fresh id reported as seen")
	}
	// Force a rotation; the id must still be found in the previous generation.
	time.Sleep(2 * time.Millisecond)
	if !c.seenBefore("abc") {
		t.Error("id was forgotten immediately after one rotation")
	}
}
