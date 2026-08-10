package guard

import (
	"errors"
	"testing"
	"time"
)

func testLimits() Limits {
	return Limits{
		MaxAgents:             4,
		ConnectionsPerMinute:  5,
		AuthFailuresBeforeBan: 3,
		BanDuration:           time.Minute,
		MessagesPerSecond:     10,
		MessageBurst:          5,
	}
}

func newGuard(t *testing.T, limits Limits) *Guard {
	t.Helper()
	g, err := New(limits)
	if err != nil {
		t.Fatalf("new guard: %s", err)
	}
	return g
}

func TestAllowConnectionUnderLimits(t *testing.T) {
	g := newGuard(t, testLimits())

	if err := g.AllowConnection("10.0.0.1:5000", 0); err != nil {
		t.Errorf("first connection rejected: %s", err)
	}
}

func TestConnectionRateLimit(t *testing.T) {
	g := newGuard(t, testLimits())

	for i := 0; i < 5; i++ {
		if err := g.AllowConnection("10.0.0.1:5000", 0); err != nil {
			t.Fatalf("connection %d rejected early: %s", i, err)
		}
	}

	err := g.AllowConnection("10.0.0.1:5000", 0)
	if err == nil {
		t.Fatal("sixth connection was allowed past the limit of five")
	}

	var rejection *Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("err = %T, want *Rejection", err)
	}
	if rejection.RetryAfter <= 0 {
		t.Error("rate limit rejection carries no retry hint")
	}

	// A different address is unaffected.
	if err := g.AllowConnection("10.0.0.2:5000", 0); err != nil {
		t.Errorf("unrelated address was caught by another address's limit: %s", err)
	}
}

func TestMaxAgents(t *testing.T) {
	g := newGuard(t, testLimits())

	if err := g.AllowConnection("10.0.0.1:5000", 4); err == nil {
		t.Error("connection allowed at the agent cap")
	}
	if err := g.AllowConnection("10.0.0.1:5000", 3); err != nil {
		t.Errorf("connection rejected below the cap: %s", err)
	}
}

// Repeated bad tokens must lead to a block, and the block must actually
// prevent further connections.
func TestAuthFailureLeadsToBan(t *testing.T) {
	g := newGuard(t, testLimits())

	for i := 0; i < 3; i++ {
		g.RecordAuthFailure("10.0.0.1:5000")
	}

	banned, until := g.IsBanned("10.0.0.1:5000")
	if !banned {
		t.Fatal("address was not banned after three failures")
	}
	if time.Until(until) <= 0 {
		t.Error("ban has already expired")
	}

	if err := g.AllowConnection("10.0.0.1:6000", 0); err == nil {
		t.Error("banned address was allowed to connect from a new source port")
	}
}

func TestAuthSuccessClearsFailures(t *testing.T) {
	g := newGuard(t, testLimits())

	g.RecordAuthFailure("10.0.0.1:5000")
	g.RecordAuthFailure("10.0.0.1:5000")
	g.RecordAuthSuccess("10.0.0.1:5000")
	g.RecordAuthFailure("10.0.0.1:5000")

	if banned, _ := g.IsBanned("10.0.0.1:5000"); banned {
		t.Error("address was banned even though a success reset the count")
	}
}

// A repeat offender should be blocked for longer each time, so someone who
// waits out every ban does not get unlimited attempts.
func TestBansEscalate(t *testing.T) {
	g := newGuard(t, testLimits())

	for i := 0; i < 3; i++ {
		g.RecordAuthFailure("10.0.0.1:5000")
	}
	_, first := g.IsBanned("10.0.0.1:5000")
	firstLength := time.Until(first)

	for i := 0; i < 3; i++ {
		g.RecordAuthFailure("10.0.0.1:5000")
	}
	_, second := g.IsBanned("10.0.0.1:5000")
	secondLength := time.Until(second)

	if secondLength <= firstLength {
		t.Errorf("second ban %s is not longer than the first %s", secondLength, firstLength)
	}
}

func TestUnban(t *testing.T) {
	g := newGuard(t, testLimits())

	for i := 0; i < 3; i++ {
		g.RecordAuthFailure("10.0.0.1:5000")
	}
	if banned, _ := g.IsBanned("10.0.0.1:5000"); !banned {
		t.Fatal("not banned")
	}

	g.Unban("10.0.0.1")
	if banned, _ := g.IsBanned("10.0.0.1:5000"); banned {
		t.Error("still banned after unban")
	}
	if err := g.AllowConnection("10.0.0.1:5000", 0); err != nil {
		t.Errorf("connection still refused after unban: %s", err)
	}
}

func TestAllowlist(t *testing.T) {
	limits := testLimits()
	limits.AllowedNetworks = []string{"10.0.0.0/8", "192.168.1.50"}
	g := newGuard(t, limits)

	allowed := []string{"10.5.5.5:100", "10.0.0.1:100", "192.168.1.50:100"}
	for _, addr := range allowed {
		if err := g.AllowConnection(addr, 0); err != nil {
			t.Errorf("allowlisted %s was rejected: %s", addr, err)
		}
	}

	denied := []string{"172.16.0.1:100", "192.168.1.51:100", "8.8.8.8:100"}
	for _, addr := range denied {
		if err := g.AllowConnection(addr, 0); err == nil {
			t.Errorf("%s was allowed despite not being on the allowlist", addr)
		}
	}
}

// A dual-stack listener reports IPv4 clients as IPv4-mapped IPv6 addresses.
// An IPv4 allowlist entry has to match those, or the allowlist would lock out
// exactly the operators who configured it.
func TestAllowlistMatchesMappedIPv4(t *testing.T) {
	limits := testLimits()
	limits.AllowedNetworks = []string{"10.0.0.0/8"}
	g := newGuard(t, limits)

	if err := g.AllowConnection("[::ffff:10.1.2.3]:5000", 0); err != nil {
		t.Errorf("IPv4-mapped address was rejected by an IPv4 allowlist: %s", err)
	}
}

// A typo in the allowlist must fail loudly. Silently ignoring it would leave
// the hub open to everyone while the operator believed it was restricted.
func TestInvalidAllowlistIsAnError(t *testing.T) {
	limits := testLimits()
	limits.AllowedNetworks = []string{"not-an-address"}

	if _, err := New(limits); err == nil {
		t.Fatal("an unparseable allowlist entry was accepted")
	}
}

func TestEmptyAllowlistAllowsEveryone(t *testing.T) {
	g := newGuard(t, testLimits())
	if err := g.AllowConnection("203.0.113.9:5000", 0); err != nil {
		t.Errorf("connection rejected with no allowlist configured: %s", err)
	}
}

func TestLimiterBurstThenRate(t *testing.T) {
	l := NewLimiter(10, 5)

	for i := 0; i < 5; i++ {
		if !l.Allow() {
			t.Fatalf("burst message %d was dropped", i)
		}
	}
	if l.Allow() {
		t.Error("message past the burst was allowed with no time elapsed")
	}
	if l.Dropped() != 1 {
		t.Errorf("dropped = %d, want 1", l.Dropped())
	}

	// At ten per second, a token is back within about 100ms.
	time.Sleep(150 * time.Millisecond)
	if !l.Allow() {
		t.Error("no token refilled after waiting")
	}
}

func TestLimiterDisabledWhenRateIsZero(t *testing.T) {
	l := NewLimiter(0, 1)
	for i := 0; i < 1000; i++ {
		if !l.Allow() {
			t.Fatal("a limiter with no rate refused a message")
		}
	}
}

func TestNilLimiterAllows(t *testing.T) {
	var l *Limiter
	if !l.Allow() {
		t.Error("nil limiter refused a message")
	}
}

func TestHostOf(t *testing.T) {
	tests := map[string]string{
		"10.0.0.1:5000":         "10.0.0.1",
		"[::1]:5000":            "::1",
		"10.0.0.1":              "10.0.0.1",
		"[::ffff:10.1.2.3]:900": "::ffff:10.1.2.3",
	}
	for in, want := range tests {
		if got := HostOf(in); got != want {
			t.Errorf("HostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// The rolling window must not let double the limit through by straddling a
// boundary, which is the classic fixed-counter bug.
func TestWindowIsRolling(t *testing.T) {
	w := newWindow(200 * time.Millisecond)

	for i := 0; i < 3; i++ {
		if !w.allow(3) {
			t.Fatalf("event %d refused early", i)
		}
	}
	if w.allow(3) {
		t.Fatal("fourth event allowed within the window")
	}

	time.Sleep(250 * time.Millisecond)
	if !w.allow(3) {
		t.Error("event refused after the window had passed")
	}
}
