package relay

// Test frames let the hub confirm an agent is not just holding a socket open
// but actually alive and able to reach its game server.
//
// A connected websocket proves very little on its own: the agent process could
// be wedged, or its telnet side could be down while the relay link stays up.
// The round trip here answers "is this server really working" in a way the
// connection state cannot.

const (
	// FrameTest is a hub-initiated liveness probe.
	FrameTest FrameType = "test"
	// FrameTestReply is the agent's answer.
	FrameTestReply FrameType = "test_reply"
)

// Test asks an agent to report in.
type Test struct {
	// ID correlates the reply with the request, since several probes may be
	// outstanding at once.
	ID string `json:"id"`
	// IsEcho asks the agent to also inject a visible test line into its game
	// server, so an operator can confirm the whole path end to end.
	IsEcho bool `json:"is_echo,omitempty"`
	// Message is the text to inject when IsEcho is set.
	Message string `json:"message,omitempty"`
}

// TestReply is what the agent sends back.
type TestReply struct {
	ID string `json:"id"`
	// SourceUp reports whether the agent's telnet connection is healthy.
	SourceUp bool `json:"source_up"`
	// PlayerCount is the agent's current online count.
	PlayerCount int `json:"player_count"`
	// Version identifies the agent build, so a hub can spot a stale agent.
	Version string `json:"version,omitempty"`
	// InjectedOK reports whether an echo request reached the game server.
	InjectedOK bool `json:"injected_ok,omitempty"`
	// Detail carries an error explanation when something failed.
	Detail string `json:"detail,omitempty"`
}
