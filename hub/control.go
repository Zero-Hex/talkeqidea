package hub

import (
	"fmt"
	"sync"
	"time"

	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/relay"
	"github.com/Zero-Hex/modern-eq-chat/sanitize"
	"github.com/Zero-Hex/modern-eq-chat/tlog"
)

// Control operations used by the web interface: probing agents, renaming them,
// and applying channel changes without a restart.

// TestResult is what a probe learned about one agent.
type TestResult struct {
	ServerKey   string        `json:"server_key"`
	ShortName   string        `json:"short_name"`
	IsConnected bool          `json:"is_connected"`
	SourceUp    bool          `json:"source_up"`
	PlayerCount int           `json:"player_count"`
	RoundTrip   time.Duration `json:"-"`
	RoundTripMS int64         `json:"round_trip_ms"`
	InjectedOK  bool          `json:"injected_ok"`
	Detail      string        `json:"detail"`
}

// pending tracks probes waiting for a reply.
type pendingTests struct {
	mu      sync.Mutex
	waiting map[string]chan *relay.TestReply
}

func newPendingTests() *pendingTests {
	return &pendingTests{waiting: make(map[string]chan *relay.TestReply)}
}

func (p *pendingTests) add(id string) chan *relay.TestReply {
	// Buffered so a reply arriving after the caller has given up does not
	// block the session's read loop forever.
	ch := make(chan *relay.TestReply, 1)

	p.mu.Lock()
	p.waiting[id] = ch
	p.mu.Unlock()

	return ch
}

func (p *pendingTests) remove(id string) {
	p.mu.Lock()
	delete(p.waiting, id)
	p.mu.Unlock()
}

func (p *pendingTests) deliver(reply *relay.TestReply) {
	p.mu.Lock()
	ch, ok := p.waiting[reply.ID]
	if ok {
		delete(p.waiting, reply.ID)
	}
	p.mu.Unlock()

	if ok {
		ch <- reply
	}
}

// TestAgent probes one agent and waits for its answer.
//
// A disconnected agent is reported rather than treated as an error: "that
// server is not connected" is the most useful answer an operator can get, and
// it is not a failure of the probe.
func (h *Hub) TestAgent(serverKey string, withEcho bool, timeout time.Duration) TestResult {
	serverKey = sanitize.ServerKey(serverKey)

	result := TestResult{ServerKey: serverKey}
	if entry, ok := h.roster.Entry(serverKey); ok {
		result.ShortName = entry.ShortName
	}

	session := h.session(serverKey)
	if session == nil {
		result.Detail = "not connected"
		return result
	}
	result.IsConnected = true
	result.ShortName = session.ShortName()

	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	id := relay.NewID()
	frame, err := relay.NewFrame(relay.FrameTest, &relay.Test{
		ID:      id,
		IsEcho:  withEcho,
		Message: "Modern EQ Chat connection test from the hub",
	})
	if err != nil {
		result.Detail = "could not build the probe: " + err.Error()
		return result
	}

	replies := h.tests.add(id)
	defer h.tests.remove(id)

	sentAt := time.Now()
	session.Send(frame)

	select {
	case reply := <-replies:
		result.RoundTrip = time.Since(sentAt)
		result.RoundTripMS = result.RoundTrip.Milliseconds()
		result.SourceUp = reply.SourceUp
		result.PlayerCount = reply.PlayerCount
		result.InjectedOK = reply.InjectedOK
		result.Detail = reply.Detail

		if result.Detail == "" {
			if reply.SourceUp {
				result.Detail = "ok"
			} else {
				// The relay link is fine but the game server is not, which is
				// exactly the distinction an operator is testing for.
				result.Detail = "relay link is up, but the game server connection is down"
			}
		}

	case <-time.After(timeout):
		result.Detail = fmt.Sprintf("no reply within %s", timeout)
	}

	return result
}

// TestAll probes every connected agent at once.
//
// Concurrently, because a sequential sweep of twenty servers each waiting out
// a ten second timeout would take over three minutes.
func (h *Hub) TestAll(withEcho bool, timeout time.Duration) []TestResult {
	entries := h.roster.Entries()

	results := make([]TestResult, len(entries))
	wg := sync.WaitGroup{}

	for i, entry := range entries {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			results[i] = h.TestAgent(key, withEcho, timeout)
		}(i, entry.ServerKey)
	}

	wg.Wait()
	return results
}

// RenameAgent changes a server's display name.
//
// The change lands in the roster and, when the agent is connected, in its live
// session too, so relayed chat picks up the new name immediately rather than
// at the agent's next reconnect.
func (h *Hub) RenameAgent(serverKey, shortName string) error {
	serverKey = sanitize.ServerKey(serverKey)

	shortName = sanitize.TelnetLine(shortName, 64)
	if shortName == "" {
		return fmt.Errorf("display name is empty after sanitizing")
	}

	if err := h.roster.Rename(serverKey, shortName); err != nil {
		return err
	}

	if session := h.session(serverKey); session != nil {
		session.setShortName(shortName)
	}

	tlog.Infof("[hub] renamed %s to %q", serverKey, shortName)
	return nil
}

// Channels returns the current channel configuration.
func (h *Hub) Channels() []config.HubChannel {
	return h.router.Channels()
}

// ReplaceChannels applies a new channel configuration to the running hub.
//
// Templates are parsed before anything is swapped, so a bad pattern is
// rejected outright rather than leaving the hub half-updated with a channel
// that panics on the next message.
func (h *Hub) ReplaceChannels(channels []config.HubChannel) error {
	candidate := config.HubConfig{Channels: channels}
	relayCfg := config.Relay{Mode: config.ModeHub, Hub: candidate}
	if err := relayCfg.Verify(); err != nil {
		return fmt.Errorf("channels are not valid: %w", err)
	}

	h.router.SetChannels(relayCfg.Hub.Channels)

	h.mu.Lock()
	h.cfg.Channels = relayCfg.Hub.Channels
	h.mu.Unlock()

	tlog.Infof("[hub] channel configuration reloaded (%d channels)", len(channels))
	return nil
}
