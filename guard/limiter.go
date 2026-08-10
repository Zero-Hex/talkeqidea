package guard

import (
	"sync"
	"time"
)

// Limiter is a token bucket, used for per-agent message rates.
//
// A bucket rather than a fixed window because chat is bursty by nature: a
// player pasting three lines at once is normal, a thousand lines a second is
// not, and a bucket distinguishes them without penalizing the first.
type Limiter struct {
	mu sync.Mutex

	ratePerSecond float64
	burst         float64
	tokens        float64
	last          time.Time
	// dropped counts refusals, so the hub can log that an agent is being
	// throttled rather than silently losing its chat.
	dropped int64
}

// NewLimiter builds a limiter. A rate of zero or less disables limiting.
func NewLimiter(ratePerSecond float64, burst int) *Limiter {
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		ratePerSecond: ratePerSecond,
		burst:         float64(burst),
		tokens:        float64(burst),
		last:          time.Now(),
	}
}

// Allow consumes a token, reporting whether one was available.
func (l *Limiter) Allow() bool {
	if l == nil || l.ratePerSecond <= 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(l.last).Seconds()
	l.last = now

	l.tokens += elapsed * l.ratePerSecond
	if l.tokens > l.burst {
		l.tokens = l.burst
	}

	if l.tokens < 1 {
		l.dropped++
		return false
	}
	l.tokens--
	return true
}

// Dropped returns how many calls have been refused.
func (l *Limiter) Dropped() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

// window counts events in a rolling period.
//
// It keeps timestamps rather than a counter so the limit is a true rolling
// window: a fixed counter reset on a boundary would let twice the limit
// through by straddling it.
type window struct {
	period time.Duration
	events []time.Time
}

func newWindow(period time.Duration) *window {
	return &window{period: period}
}

// allow records an event if fewer than limit have occurred in the period.
func (w *window) allow(limit int) bool {
	now := time.Now()
	w.trim(now)

	if len(w.events) >= limit {
		return false
	}
	w.events = append(w.events, now)
	return true
}

// retryAfter is how long until the oldest event ages out.
func (w *window) retryAfter() time.Duration {
	if len(w.events) == 0 {
		return 0
	}
	remaining := time.Until(w.events[0].Add(w.period))
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (w *window) isIdle(now time.Time) bool {
	w.trim(now)
	return len(w.events) == 0
}

func (w *window) trim(now time.Time) {
	cutoff := now.Add(-w.period)
	keep := 0
	for _, at := range w.events {
		if at.After(cutoff) {
			break
		}
		keep++
	}
	if keep > 0 {
		w.events = w.events[keep:]
	}
}
