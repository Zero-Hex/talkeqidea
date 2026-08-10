// Package guard holds the hub's abuse controls: who may connect, how often,
// and how fast they may talk once they are in.
//
// The hub is the one component of a Modern EQ Chat relay exposed to the internet, so
// it is the one that has to assume hostile traffic. Everything here is
// deliberately in-memory: bans and rate state are cheap to rebuild, and a hub
// restart clearing them is the correct behavior rather than a limitation.
package guard

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limits configures the guard.
type Limits struct {
	// MaxAgents caps concurrent agent sessions. Zero means unlimited.
	MaxAgents int
	// ConnectionsPerMinute caps how often one address may open a connection.
	ConnectionsPerMinute int
	// AuthFailuresBeforeBan is how many rejected tokens an address may present
	// before it is temporarily blocked.
	AuthFailuresBeforeBan int
	// BanDuration is the first ban's length. Repeat offenders get longer.
	BanDuration time.Duration
	// MessagesPerSecond is the sustained per-agent message rate.
	MessagesPerSecond float64
	// MessageBurst is how many messages may arrive at once before the
	// sustained rate applies.
	MessageBurst int
	// AllowedNetworks restricts connections to these CIDRs or addresses. Empty
	// allows any source.
	AllowedNetworks []string
}

// Guard enforces the limits.
type Guard struct {
	mu sync.Mutex

	limits   Limits
	allowed  []netip.Prefix
	conns    map[string]*window
	failures map[string]*failureRecord
	bans     map[string]banRecord

	lastSweep time.Time
}

type failureRecord struct {
	count    int
	firstAt  time.Time
	offences int
}

type banRecord struct {
	until  time.Time
	reason string
}

// New builds a guard. An invalid entry in AllowedNetworks is an error rather
// than a silent skip: a typo that quietly widened the allowlist to everything
// would be the worst possible failure mode.
func New(limits Limits) (*Guard, error) {
	g := &Guard{
		limits:    limits,
		conns:     make(map[string]*window),
		failures:  make(map[string]*failureRecord),
		bans:      make(map[string]banRecord),
		lastSweep: time.Now(),
	}

	for _, entry := range limits.AllowedNetworks {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		prefix, err := parseNetwork(entry)
		if err != nil {
			return nil, fmt.Errorf("allowed network %q: %w", entry, err)
		}
		g.allowed = append(g.allowed, prefix)
	}
	return g, nil
}

// parseNetwork accepts either a CIDR or a bare address.
func parseNetwork(entry string) (netip.Prefix, error) {
	if strings.Contains(entry, "/") {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, err
		}
		return prefix.Masked(), nil
	}

	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("not an address or CIDR")
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// Rejection explains why a connection was refused.
type Rejection struct {
	Reason string
	// RetryAfter is how long the caller should wait, when that is known.
	RetryAfter time.Duration
}

func (r *Rejection) Error() string {
	if r.RetryAfter > 0 {
		return fmt.Sprintf("%s (retry in %s)", r.Reason, r.RetryAfter.Round(time.Second))
	}
	return r.Reason
}

// AllowConnection decides whether an address may open a connection.
//
// currentAgents is passed in rather than tracked here so the guard has no
// opinion about session lifecycle; the hub already knows that number.
func (g *Guard) AllowConnection(remoteAddr string, currentAgents int) error {
	ip := HostOf(remoteAddr)

	g.mu.Lock()
	defer g.mu.Unlock()

	g.sweepLocked()

	if !g.isAllowedLocked(ip) {
		return &Rejection{Reason: "address is not in the allowlist"}
	}

	if ban, ok := g.bans[ip]; ok && time.Now().Before(ban.until) {
		return &Rejection{
			Reason:     "address is temporarily blocked: " + ban.reason,
			RetryAfter: time.Until(ban.until),
		}
	}

	if g.limits.MaxAgents > 0 && currentAgents >= g.limits.MaxAgents {
		// Not the connecting party's fault, so no failure is recorded.
		return &Rejection{Reason: fmt.Sprintf("hub is at its limit of %d agents", g.limits.MaxAgents)}
	}

	if g.limits.ConnectionsPerMinute > 0 {
		w, ok := g.conns[ip]
		if !ok {
			w = newWindow(time.Minute)
			g.conns[ip] = w
		}
		if !w.allow(g.limits.ConnectionsPerMinute) {
			return &Rejection{
				Reason:     "too many connection attempts",
				RetryAfter: w.retryAfter(),
			}
		}
	}

	return nil
}

// RecordAuthFailure notes a rejected credential and bans the address once it
// has failed too often.
//
// Failures are counted in a rolling window so an agent with a stale token that
// retries slowly for days is never banned, while a script trying thousands of
// tokens is stopped in seconds.
func (g *Guard) RecordAuthFailure(remoteAddr string) {
	if g.limits.AuthFailuresBeforeBan <= 0 {
		return
	}
	ip := HostOf(remoteAddr)

	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	record, ok := g.failures[ip]
	if !ok || now.Sub(record.firstAt) > failureWindow {
		offences := 0
		if ok {
			offences = record.offences
		}
		record = &failureRecord{firstAt: now, offences: offences}
		g.failures[ip] = record
	}
	record.count++

	if record.count < g.limits.AuthFailuresBeforeBan {
		return
	}

	record.offences++
	record.count = 0
	record.firstAt = now

	// Each repeat offence doubles the ban, up to a day. Someone who comes
	// back after every expiry is not making an honest mistake.
	duration := g.limits.BanDuration
	if duration <= 0 {
		duration = 15 * time.Minute
	}
	for i := 1; i < record.offences && duration < maxBan; i++ {
		duration *= 2
	}
	if duration > maxBan {
		duration = maxBan
	}

	g.bans[ip] = banRecord{
		until:  now.Add(duration),
		reason: "repeated authentication failures",
	}
}

// RecordAuthSuccess clears the failure count for an address.
func (g *Guard) RecordAuthSuccess(remoteAddr string) {
	ip := HostOf(remoteAddr)

	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.failures, ip)
}

// IsBanned reports whether an address is currently blocked, and until when.
func (g *Guard) IsBanned(remoteAddr string) (bool, time.Time) {
	ip := HostOf(remoteAddr)

	g.mu.Lock()
	defer g.mu.Unlock()

	ban, ok := g.bans[ip]
	if !ok || time.Now().After(ban.until) {
		return false, time.Time{}
	}
	return true, ban.until
}

// Bans lists the addresses currently blocked, for status output.
func (g *Guard) Bans() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	out := make([]string, 0, len(g.bans))
	for ip, ban := range g.bans {
		if now.After(ban.until) {
			continue
		}
		out = append(out, fmt.Sprintf("%s (%s remaining)", ip, time.Until(ban.until).Round(time.Second)))
	}
	sort.Strings(out)
	return out
}

// Unban lifts a block early.
func (g *Guard) Unban(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.bans, ip)
	delete(g.failures, ip)
}

// NewMessageLimiter returns a limiter for one agent's message rate.
func (g *Guard) NewMessageLimiter() *Limiter {
	return NewLimiter(g.limits.MessagesPerSecond, g.limits.MessageBurst)
}

func (g *Guard) isAllowedLocked(ip string) bool {
	if len(g.allowed) == 0 {
		return true
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	// Unmap so an IPv4-mapped IPv6 address matches an IPv4 allowlist entry,
	// which is how a dual-stack listener reports IPv4 clients.
	addr = addr.Unmap()

	for _, prefix := range g.allowed {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

const (
	failureWindow = 10 * time.Minute
	maxBan        = 24 * time.Hour
	sweepEvery    = time.Minute
)

// sweepLocked discards state for addresses that have gone quiet, so a hub
// facing a spray of one-shot connections from many addresses does not grow its
// maps without bound.
func (g *Guard) sweepLocked() {
	now := time.Now()
	if now.Sub(g.lastSweep) < sweepEvery {
		return
	}
	g.lastSweep = now

	for ip, ban := range g.bans {
		if now.After(ban.until) {
			delete(g.bans, ip)
		}
	}
	for ip, record := range g.failures {
		if now.Sub(record.firstAt) > failureWindow*2 {
			delete(g.failures, ip)
		}
	}
	for ip, w := range g.conns {
		if w.isIdle(now) {
			delete(g.conns, ip)
		}
	}
}

// HostOf extracts the address portion of a host:port pair, tolerating input
// that is already a bare address.
func HostOf(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return strings.TrimSpace(remoteAddr)
	}
	return host
}
