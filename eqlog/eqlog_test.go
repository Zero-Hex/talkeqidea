package eqlog

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/request"
	"github.com/Zero-Hex/modern-eq-chat/tlog"
)

func TestMain(m *testing.M) {
	tlog.Init(nil, os.Stdout)
	os.Exit(m.Run())
}

// collector records what the log watcher decided to relay.
type collector struct {
	mu       sync.Mutex
	messages []request.DiscordSend
}

func (c *collector) onMessage(raw interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if req, ok := raw.(request.DiscordSend); ok {
		c.messages = append(c.messages, req)
	}
	return nil
}

func (c *collector) all() []request.DiscordSend {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]request.DiscordSend, len(c.messages))
	copy(out, c.messages)
	return out
}

// This package follows a growing file with a third-party tail library, which
// was swapped from the abandoned hpcloud/tail to nxadm/tail. That is exactly
// the kind of change that compiles cleanly and silently stops working, so the
// test drives a real file on disk rather than mocking the reader.
func TestFollowsAppendedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eqlog_Soandso_server.txt")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create log: %s", err)
	}
	defer f.Close()

	// A line written before the watcher starts must not be relayed: the
	// watcher seeks to the end, so history is not replayed into Discord on
	// every restart.
	if _, err := f.WriteString("[Wed Jan 01 12:00:00 2025] Ancient says out of character, 'old news'\n"); err != nil {
		t.Fatalf("seed log: %s", err)
	}

	cfg := config.EQLog{
		IsEnabled: true,
		Path:      path,
		Routes: []config.Route{
			{
				IsEnabled: true,
				Trigger: config.Trigger{
					Regex:        `(\w+) says out of character, '(.*)'`,
					NameIndex:    1,
					MessageIndex: 2,
				},
				Target:         "discord",
				ChannelID:      "ooc-channel",
				MessagePattern: "{{.Name}} **OOC**: {{.Message}}",
			},
		},
	}
	if err := cfg.Verify(); err != nil {
		t.Fatalf("verify: %s", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("new: %s", err)
	}

	sink := &collector{}
	if err := watcher.Subscribe(ctx, sink.onMessage); err != nil {
		t.Fatalf("subscribe: %s", err)
	}
	if err := watcher.Connect(ctx); err != nil {
		t.Fatalf("connect: %s", err)
	}
	t.Cleanup(func() { watcher.Disconnect(context.Background()) })

	// Give the watcher a moment to seek to the end before appending.
	time.Sleep(300 * time.Millisecond)

	if _, err := f.WriteString("[Wed Jan 01 12:00:05 2025] Soandso says out of character, 'where does Master Claude spawn'\n"); err != nil {
		t.Fatalf("append: %s", err)
	}

	waitFor(t, "the appended line to be relayed", func() bool {
		return len(sink.all()) > 0
	})

	got := sink.all()[0]
	if got.ChannelID != "ooc-channel" {
		t.Errorf("channel = %q, want ooc-channel", got.ChannelID)
	}
	want := "Soandso **OOC**: where does Master Claude spawn"
	if got.Message != want {
		t.Errorf("message = %q, want %q", got.Message, want)
	}

	for _, message := range sink.all() {
		if message.Message == "Ancient **OOC**: old news" {
			t.Error("a line written before startup was replayed")
		}
	}
}

// Lines that match no route must be ignored rather than relayed raw.
func TestIgnoresUnmatchedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eqlog.txt")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create log: %s", err)
	}
	defer f.Close()

	cfg := config.EQLog{
		IsEnabled: true,
		Path:      path,
		Routes: []config.Route{
			{
				IsEnabled: true,
				Trigger: config.Trigger{
					Regex:        `(\w+) says out of character, '(.*)'`,
					NameIndex:    1,
					MessageIndex: 2,
				},
				Target:         "discord",
				ChannelID:      "ooc-channel",
				MessagePattern: "{{.Name}}: {{.Message}}",
			},
		},
	}
	if err := cfg.Verify(); err != nil {
		t.Fatalf("verify: %s", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("new: %s", err)
	}

	sink := &collector{}
	watcher.Subscribe(ctx, sink.onMessage)
	if err := watcher.Connect(ctx); err != nil {
		t.Fatalf("connect: %s", err)
	}
	t.Cleanup(func() { watcher.Disconnect(context.Background()) })

	time.Sleep(300 * time.Millisecond)

	f.WriteString("[Wed Jan 01 12:00:05 2025] You have entered Greater Faydark.\n")
	f.WriteString("[Wed Jan 01 12:00:06 2025] Soandso says out of character, 'hi'\n")

	waitFor(t, "the matching line", func() bool { return len(sink.all()) > 0 })
	time.Sleep(200 * time.Millisecond)

	messages := sink.all()
	if len(messages) != 1 {
		t.Fatalf("relayed %d messages, want only the matching one: %v", len(messages), messages)
	}
	if messages[0].Message != "Soandso: hi" {
		t.Errorf("message = %q", messages[0].Message)
	}
}

func TestNewRejectsMissingFile(t *testing.T) {
	cfg := config.EQLog{
		IsEnabled: true,
		Path:      filepath.Join(t.TempDir(), "does-not-exist.txt"),
	}
	if _, err := New(context.Background(), cfg); err == nil {
		t.Error("a missing log file was accepted")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
