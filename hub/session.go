package hub

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xackery/talkeq/guard"
	"github.com/xackery/talkeq/relay"
	"github.com/xackery/talkeq/tlog"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

// Session is one connected agent.
//
// Every write goes through a bounded queue drained by a single writer
// goroutine. A game server that stops reading — a wedged box, a saturated
// link — fills its own queue and starts shedding its own messages. It cannot
// block the hub or slow down any other server, which is what lets the relay
// scale past a handful of agents.
type Session struct {
	serverKey string
	shortName string

	conn *websocket.Conn
	out  chan *relay.Frame

	// limiter caps how fast this agent may send. Set after authentication,
	// since an unauthenticated connection never gets far enough to send chat.
	limiter *guard.Limiter

	closeOnce sync.Once
	done      chan struct{}

	dropped     atomic.Int64
	playerCount atomic.Int64
	sourceUp    atomic.Bool
	connectedAt time.Time
}

func newSession(serverKey, shortName string, conn *websocket.Conn, queueSize int) *Session {
	return &Session{
		serverKey:   serverKey,
		shortName:   shortName,
		conn:        conn,
		out:         make(chan *relay.Frame, queueSize),
		done:        make(chan struct{}),
		connectedAt: time.Now(),
	}
}

// ServerKey returns the authenticated identity of this agent.
func (s *Session) ServerKey() string { return s.serverKey }

// ShortName returns the display name for this agent's server.
func (s *Session) ShortName() string { return s.shortName }

// PlayerCount returns the last reported online count.
func (s *Session) PlayerCount() int { return int(s.playerCount.Load()) }

// IsSourceUp reports whether the agent's own game server connection is up.
func (s *Session) IsSourceUp() bool { return s.sourceUp.Load() }

// Dropped returns how many frames were shed because this agent was too slow.
func (s *Session) Dropped() int64 { return s.dropped.Load() }

// Send queues a frame. It never blocks.
//
// When the queue is full the oldest pending frame is discarded to make room:
// for chat, a fresh message is worth more than a stale one, and a backed-up
// agent is better served catching up near the present than replaying history.
func (s *Session) Send(frame *relay.Frame) {
	select {
	case <-s.done:
		return
	default:
	}

	select {
	case s.out <- frame:
		return
	default:
	}

	// Queue full. Drop the head, then take its place.
	select {
	case <-s.out:
		s.dropped.Add(1)
	default:
	}

	select {
	case s.out <- frame:
	default:
		s.dropped.Add(1)
	}
}

// Close shuts the session down. Safe to call more than once.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.conn.Close()
	})
}

// writeLoop is the only goroutine that writes to the socket. gorilla/websocket
// does not support concurrent writers, so funnelling everything through here
// is a correctness requirement, not just tidiness.
func (s *Session) writeLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		s.Close()
	}()

	for {
		select {
		case <-s.done:
			return

		case frame := <-s.out:
			s.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := s.conn.WriteJSON(frame); err != nil {
				tlog.Debugf("[hub] write to %s failed: %s", s.serverKey, err)
				return
			}

		case <-ticker.C:
			s.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := s.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				tlog.Debugf("[hub] ping to %s failed: %s", s.serverKey, err)
				return
			}
		}
	}
}

// readLoop consumes frames until the connection ends, handing each to onFrame.
func (s *Session) readLoop(ctx context.Context, onFrame func(*Session, *relay.Frame)) {
	defer s.Close()

	s.conn.SetReadLimit(relay.MaxFrameSize)
	s.conn.SetReadDeadline(time.Now().Add(pongWait))
	s.conn.SetPongHandler(func(string) error {
		return s.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		default:
		}

		frame := &relay.Frame{}
		if err := s.conn.ReadJSON(frame); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				tlog.Infof("[hub] agent %s disconnected: %s", s.serverKey, err)
			} else {
				tlog.Infof("[hub] agent %s disconnected", s.serverKey)
			}
			return
		}
		onFrame(s, frame)
	}
}

// sendError delivers a protocol error and closes the session. Used for
// rejections so the agent logs a reason rather than a bare reset.
func (s *Session) sendError(code, message string) {
	frame, err := relay.NewFrame(relay.FrameError, &relay.Error{Code: code, Message: message})
	if err != nil {
		s.Close()
		return
	}
	s.conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := s.conn.WriteJSON(frame); err != nil {
		tlog.Debugf("[hub] send error frame failed: %s", err)
	}
	s.Close()
}

func (s *Session) String() string {
	return fmt.Sprintf("%s (%s)", s.serverKey, s.shortName)
}
