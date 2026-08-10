package agent

import (
	"sync"
	"time"

	"github.com/xackery/talkeq/relay"
)

// echoCache is the agent's guard against relaying back a message it just
// injected itself.
//
// The loop it prevents: the hub sends server 2 a line from server 1, the agent
// writes it into the game as an emote, the game echoes that emote back down
// the same telnet connection the agent is reading, the agent's OOC trigger
// matches it, and it goes back up to the hub as fresh server 2 chat. Without a
// guard, that ping-pongs until something falls over.
//
// The key is the message content rather than the rendered line, because the
// rendered form differs on each side ("Soandso says from Vanilla, 'hi'" going
// in, "Soandso says ooc, 'hi'" coming back out) while the name and body
// survive the round trip unchanged.
type echoCache struct {
	mu     sync.Mutex
	ttl    time.Duration
	limit  int
	expiry map[string]time.Time
}

func newEchoCache(ttl time.Duration) *echoCache {
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	return &echoCache{
		ttl:    ttl,
		limit:  4096,
		expiry: make(map[string]time.Time),
	}
}

// remember records that this agent injected an event locally.
func (c *echoCache) remember(e *relay.Event) {
	key := e.EchoKey()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.pruneLocked()
	c.expiry[key] = time.Now().Add(c.ttl)
}

// isEcho reports whether name/message matches something injected recently, and
// consumes the entry if so.
//
// Consuming matters: two players on different servers saying the same short
// word within the window should not silently swallow the second one forever.
// One suppression per injection is exactly the number of echoes produced.
func (c *echoCache) isEcho(name, message string) bool {
	key := name + "\x00" + message

	c.mu.Lock()
	defer c.mu.Unlock()

	deadline, ok := c.expiry[key]
	if !ok {
		return false
	}
	delete(c.expiry, key)
	return time.Now().Before(deadline)
}

// pruneLocked drops expired entries. Called on write, which is the only path
// that grows the map.
func (c *echoCache) pruneLocked() {
	now := time.Now()
	for key, deadline := range c.expiry {
		if now.After(deadline) {
			delete(c.expiry, key)
		}
	}

	// A pathological flood should not grow the map without bound between
	// prunes. Entries are all short-lived, so clearing is safe: the worst case
	// is a duplicated line, not a loop, because the hub's seen cache and the
	// hop counter still apply.
	if len(c.expiry) > c.limit {
		c.expiry = make(map[string]time.Time)
	}
}
