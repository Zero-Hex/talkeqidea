// Package relay defines the wire protocol shared by the Modern EQ Chat hub and its
// agents.
//
// The protocol is deliberately small. An agent reports who said something,
// what they said, and which chat channel it happened in. The hub stamps the
// originating server and decides where it goes. Nothing else crosses the wire,
// which keeps the format stable as sources and destinations are added.
package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// ProtocolVersion is incremented when a change breaks older peers. The hub
// refuses agents that do not match, which surfaces a version mismatch as a
// clear error at connect time rather than as malformed chat later.
const ProtocolVersion = 1

// MaxFrameSize bounds a single frame read. Chat lines are small; anything
// approaching this is a bug or an attack.
const MaxFrameSize = 64 * 1024

// FrameType identifies the payload carried by a Frame.
type FrameType string

const (
	// FrameHello is the first frame an agent sends after connecting.
	FrameHello FrameType = "hello"
	// FrameWelcome is the hub's acceptance of a Hello.
	FrameWelcome FrameType = "welcome"
	// FrameEvent carries a chat event in either direction.
	FrameEvent FrameType = "event"
	// FrameStatus is an agent's periodic report of its own health.
	FrameStatus FrameType = "status"
	// FrameError reports a protocol or authorization failure.
	FrameError FrameType = "error"
)

// Frame is the envelope for every message on the wire.
type Frame struct {
	Type    FrameType       `json:"t"`
	Payload json.RawMessage `json:"p,omitempty"`
}

// NewFrame marshals payload into a Frame of the given type.
func NewFrame(t FrameType, payload any) (*Frame, error) {
	if payload == nil {
		return &Frame{Type: t}, nil
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s payload: %w", t, err)
	}
	return &Frame{Type: t, Payload: buf}, nil
}

// Decode unmarshals a frame's payload into out.
func (f *Frame) Decode(out any) error {
	if len(f.Payload) == 0 {
		return fmt.Errorf("frame %s has no payload", f.Type)
	}
	if err := json.Unmarshal(f.Payload, out); err != nil {
		return fmt.Errorf("unmarshal %s payload: %w", f.Type, err)
	}
	return nil
}

// Hello identifies an agent to the hub. The token authenticates it; the
// ServerKey it claims is only a hint, since the hub derives the authoritative
// identity from whichever token verified.
type Hello struct {
	ProtocolVersion int      `json:"protocol_version"`
	ServerKey       string   `json:"server_key"`
	Token           string   `json:"token"`
	Version         string   `json:"version,omitempty"`
	Channels        []string `json:"channels,omitempty"`
}

// Welcome confirms a Hello and tells the agent how the hub sees it.
type Welcome struct {
	ProtocolVersion int    `json:"protocol_version"`
	ServerKey       string `json:"server_key"`
	ShortName       string `json:"short_name"`
	HubVersion      string `json:"hub_version,omitempty"`
	HeartbeatSecs   int    `json:"heartbeat_secs"`
}

// Status is an agent's periodic health report. PlayerCount lets the hub
// aggregate an accurate total across every connected server.
type Status struct {
	PlayerCount    int  `json:"player_count"`
	SourceUp       bool `json:"source_up"`
	DroppedInbound int  `json:"dropped_inbound,omitempty"`
}

// Error reports a failure. The hub sends this before closing a connection it
// is rejecting, so the agent can log something better than "connection reset".
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes the hub may return.
const (
	ErrCodeUnauthorized    = "unauthorized"
	ErrCodeVersionMismatch = "version_mismatch"
	ErrCodeDuplicate       = "duplicate_session"
	ErrCodeMalformed       = "malformed"
)

// OriginDiscord is the pseudo server key used for events that entered the
// relay from Discord rather than from a game server. It can never collide with
// a real agent because sanitize.ServerKey output is checked against it at
// roster-add time.
const OriginDiscord = "discord"

// Event is a single chat message moving through the relay.
//
// Origin and Name are the two fields that matter for display: who said it and
// which server they said it on. Everything else is routing metadata.
type Event struct {
	// ID is a random identifier used to drop duplicates.
	ID string `json:"id"`
	// Origin is the server key the event came from. The hub always overwrites
	// whatever an agent puts here with the identity that authenticated, so a
	// compromised agent cannot impersonate another server.
	Origin string `json:"origin"`
	// OriginName is the human-readable short name for Origin, resolved by the
	// hub so agents do not need a copy of the roster.
	OriginName string `json:"origin_name,omitempty"`
	// Channel is the logical chat channel, e.g. "ooc" or "auction".
	Channel string `json:"channel"`
	// Name is the in-game character (or Discord user) who sent the message.
	Name string `json:"name"`
	// Message is the message body, already stripped of item-link encoding.
	Message string `json:"message"`
	// Hop counts relays. Agents send 0; the hub emits 1. Anything higher is a
	// loop and gets dropped.
	Hop uint8 `json:"hop,omitempty"`
	// SentAt is unix milliseconds, used to age out stale queued events.
	SentAt int64 `json:"sent_at"`
}

// NewEvent builds an event with a fresh ID and timestamp.
func NewEvent(channel, name, message string) *Event {
	return &Event{
		ID:      NewID(),
		Channel: channel,
		Name:    name,
		Message: message,
		SentAt:  time.Now().UnixMilli(),
	}
}

// NewID returns a random 128-bit hex identifier.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; fall back to a timestamp so a
		// hypothetical failure degrades dedupe rather than dropping chat.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Age returns how long ago the event was sent.
func (e *Event) Age() time.Duration {
	if e.SentAt == 0 {
		return 0
	}
	return time.Since(time.UnixMilli(e.SentAt))
}

// EchoKey identifies an event by its content, independent of which server it
// came from or how it was rendered. Agents use it to recognize a message they
// themselves injected coming back around on their own chat feed.
func (e *Event) EchoKey() string {
	return e.Name + "\x00" + e.Message
}

// Validate checks an event received from a peer. It enforces the bounds that
// keep a hostile or buggy agent from propagating garbage to every other
// server on the relay.
func (e *Event) Validate() error {
	if e.Channel == "" {
		return fmt.Errorf("channel is empty")
	}
	if len(e.Channel) > 64 {
		return fmt.Errorf("channel too long")
	}
	if e.Name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(e.Name) > 64 {
		return fmt.Errorf("name too long")
	}
	if e.Message == "" {
		return fmt.Errorf("message is empty")
	}
	if len(e.Message) > 4000 {
		return fmt.Errorf("message too long")
	}
	if e.Hop > 4 {
		return fmt.Errorf("hop count %d exceeds limit", e.Hop)
	}
	return nil
}
