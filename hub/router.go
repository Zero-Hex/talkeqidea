package hub

import (
	"fmt"
	"sync"
	"time"

	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/relay"
)

// Router decides where an event goes. It holds no sockets and does no I/O,
// which keeps the interesting logic — fan-out, self-exclusion, loop and
// duplicate rejection — testable without standing up a server.
type Router struct {
	cfg  *config.HubConfig
	seen *seenCache
}

// Plan is the set of deliveries for one event.
type Plan struct {
	// DiscordChannelID is empty when the channel has no Discord leg.
	DiscordChannelID string
	// DiscordMessage is the rendered text for Discord.
	DiscordMessage string
	// Targets are the server keys this event should be forwarded to. The
	// originating server is never included.
	Targets []string
}

// IsEmpty reports whether the plan would deliver nothing.
func (p *Plan) IsEmpty() bool {
	return p.DiscordChannelID == "" && len(p.Targets) == 0
}

// NewRouter builds a router over hub configuration.
func NewRouter(cfg *config.HubConfig) *Router {
	return &Router{
		cfg:  cfg,
		seen: newSeenCache(30 * time.Second),
	}
}

// Route computes the delivery plan for an event.
//
// connected is the set of currently attached agent server keys. Passing it in
// rather than reading it from a registry keeps Route pure.
func (r *Router) Route(e *relay.Event, connected []string) (*Plan, error) {
	if err := e.Validate(); err != nil {
		return nil, fmt.Errorf("invalid event: %w", err)
	}

	// An event the hub has already handled is a loop. This catches the case
	// where an agent echoes back a message the hub just sent it, which is the
	// failure mode that would otherwise saturate every server on the relay.
	if r.seen.seenBefore(e.ID) {
		return nil, errDuplicate
	}
	if e.Hop > 0 {
		return nil, fmt.Errorf("event already relayed (hop %d)", e.Hop)
	}

	ch, ok := r.cfg.Channel(e.Channel)
	if !ok {
		return nil, fmt.Errorf("no channel named %q", e.Channel)
	}
	if !ch.IsEnabled {
		return nil, errChannelDisabled
	}

	plan := &Plan{}
	isFromDiscord := e.Origin == relay.OriginDiscord

	// A message that came from Discord is already visible in Discord. Echoing
	// it back would double every line players type there.
	if ch.DiscordChannelID != "" && !isFromDiscord {
		msg, err := relay.Render(ch.DiscordTemplate(), e, ch.DiscordChannelID)
		if err != nil {
			return nil, fmt.Errorf("render discord pattern for %s: %w", ch.Name, err)
		}
		if msg != "" {
			plan.DiscordChannelID = ch.DiscordChannelID
			plan.DiscordMessage = msg
		}
	}

	// cross_server governs game-to-game relay. Discord-to-game is the separate
	// feature of letting players talk in from Discord, and stays on even when
	// servers are not linked to each other.
	if ch.IsCrossServer || isFromDiscord {
		for _, key := range connected {
			// Never send a server its own chat back. This is the primary loop
			// guard; the seen cache and hop count are the backstops.
			if key == e.Origin {
				continue
			}
			plan.Targets = append(plan.Targets, key)
		}
	}

	return plan, nil
}

var (
	errDuplicate       = fmt.Errorf("duplicate event")
	errChannelDisabled = fmt.Errorf("channel disabled")
)

// IsDuplicate reports whether an error from Route means the event was already
// handled. Callers log these at debug rather than warning, since a handful are
// expected whenever an agent reconnects and replays its queue.
func IsDuplicate(err error) bool { return err == errDuplicate }

// IsChannelDisabled reports whether an error from Route means the channel is
// configured but switched off.
func IsChannelDisabled(err error) bool { return err == errChannelDisabled }

// seenCache remembers recently handled event IDs.
//
// It uses two generations rather than per-entry timers: when the current
// generation ages out it becomes the previous one and a fresh map is started.
// Lookups check both. That bounds memory to two windows' worth of traffic and
// costs one map allocation per window regardless of message volume, which
// matters once a few dozen servers are attached.
type seenCache struct {
	mu       sync.Mutex
	window   time.Duration
	current  map[string]struct{}
	previous map[string]struct{}
	rotateAt time.Time
}

func newSeenCache(window time.Duration) *seenCache {
	return &seenCache{
		window:   window,
		current:  make(map[string]struct{}),
		previous: make(map[string]struct{}),
		rotateAt: time.Now().Add(window),
	}
}

// seenBefore records id and reports whether it had already been recorded.
func (s *seenCache) seenBefore(id string) bool {
	if id == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Now().After(s.rotateAt) {
		s.previous = s.current
		s.current = make(map[string]struct{}, len(s.previous))
		s.rotateAt = time.Now().Add(s.window)
	}

	if _, ok := s.current[id]; ok {
		return true
	}
	if _, ok := s.previous[id]; ok {
		return true
	}
	s.current[id] = struct{}{}
	return false
}
