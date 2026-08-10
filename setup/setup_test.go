package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xackery/talkeq/config"
)

// script drives a wizard with canned answers, one per line.
func script(answers ...string) *bytes.Buffer {
	return bytes.NewBufferString(strings.Join(answers, "\n") + "\n")
}

// The hub wizard must turn a handful of answers into a config that actually
// verifies, since a config that fails Verify would leave the operator with a
// hub that refuses to start.
func TestRunHubWritesUsableConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "talkeq.conf")

	// Run in the temp dir so generated certificates and databases land there.
	chdir(t, dir)

	out := &bytes.Buffer{}
	in := script(
		"bot-token-here",  // Discord bot token
		"123456789",       // client id
		"987654321",       // server id
		"34197",           // port
		"hub.example.com", // advertise host
		"1",               // self-signed
		"555000111",       // ooc channel id
		"y",               // cross server
		"y",               // discord talk back
		"n",               // no local game server
		"n",               // do not add an agent now
	)

	if err := RunHub(context.Background(), NewPrompterFrom(in, out), path); err != nil {
		t.Fatalf("run hub: %s\noutput:\n%s", err, out.String())
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load written config: %s", err)
	}
	if err := cfg.Verify(); err != nil {
		t.Fatalf("wizard wrote a config that does not verify: %s", err)
	}

	if cfg.Relay.Mode != config.ModeHub {
		t.Errorf("mode = %q, want hub", cfg.Relay.Mode)
	}
	if cfg.Relay.Hub.Listen != ":34197" {
		t.Errorf("listen = %q, want :34197", cfg.Relay.Hub.Listen)
	}
	if cfg.Relay.Hub.AdvertiseAddr != "hub.example.com:34197" {
		t.Errorf("advertise = %q, want hub.example.com:34197", cfg.Relay.Hub.AdvertiseAddr)
	}
	if cfg.Discord.Token != "bot-token-here" {
		t.Errorf("discord token was not saved")
	}

	channel, ok := cfg.Relay.Hub.Channel("ooc")
	if !ok {
		t.Fatal("no ooc channel was configured")
	}
	if channel.DiscordChannelID != "555000111" {
		t.Errorf("ooc discord channel = %q, want 555000111", channel.DiscordChannelID)
	}
	if !channel.IsCrossServer {
		t.Error("cross_server was not enabled")
	}

	// Discord must be able to talk back in, which means a relay-targeted route.
	found := false
	for _, route := range cfg.Discord.Routes {
		if route.Target == "relay" && route.Channel == "ooc" {
			found = true
			if route.Trigger.ChannelID != "555000111" {
				t.Errorf("discord relay route listens on %q, want 555000111", route.Trigger.ChannelID)
			}
		}
	}
	if !found {
		t.Error("no discord route feeds the relay")
	}
}

// Re-running the wizard must not discard settings it never asks about.
func TestRunHubPreservesUnrelatedSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "talkeq.conf")
	chdir(t, dir)

	seed := config.Default()
	seed.SQLReport.Host = "db.example.com:3306"
	seed.SQLReport.Username = "someuser"
	if err := config.Save(&seed, path); err != nil {
		t.Fatalf("seed config: %s", err)
	}

	in := script(
		"bot-token-here", "123456789", "987654321",
		"34197", "hub.example.com", "1",
		"555000111", "y", "y",
		"n",
		"n",
	)
	if err := RunHub(context.Background(), NewPrompterFrom(in, &bytes.Buffer{}), path); err != nil {
		t.Fatalf("run hub: %s", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %s", err)
	}
	if cfg.SQLReport.Host != "db.example.com:3306" {
		t.Errorf("sql_report host = %q, wizard discarded an unrelated setting", cfg.SQLReport.Host)
	}
	if cfg.SQLReport.Username != "someuser" {
		t.Errorf("sql_report username = %q, wizard discarded an unrelated setting", cfg.SQLReport.Username)
	}

	// And it should have left a backup of what was there before.
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("no backup was written: %s", err)
	}
}

// Choosing no encryption is a decision that should take two deliberate
// answers, not one.
func TestRunHubRequiresConfirmationToDisableTLS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "talkeq.conf")
	chdir(t, dir)

	in := script(
		"bot-token-here", "123456789", "987654321",
		"34197", "hub.example.com",
		"3", // no encryption
		"n", // ...on reflection, no
		"555000111", "y", "y",
		"n",
		"n",
	)
	if err := RunHub(context.Background(), NewPrompterFrom(in, &bytes.Buffer{}), path); err != nil {
		t.Fatalf("run hub: %s", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %s", err)
	}
	if cfg.Relay.Hub.TLSMode != config.TLSSelfSigned {
		t.Errorf("tls_mode = %q, want self-signed after declining the warning", cfg.Relay.Hub.TLSMode)
	}
}

func TestNormalizeHubAddress(t *testing.T) {
	tests := map[string]string{
		"hub.example.com:34197":       "hub.example.com:34197",
		"hub.example.com":             "hub.example.com:34197",
		"wss://hub.example.com:34197": "hub.example.com:34197",
		"https://hub.example.com/":    "hub.example.com:34197",
		"10.0.0.5:9999":               "10.0.0.5:9999",
		"10.0.0.5":                    "10.0.0.5:34197",
		"":                            "",
	}
	for in, want := range tests {
		if got := normalizeHubAddress(in); got != want {
			t.Errorf("normalizeHubAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrompterDefaults(t *testing.T) {
	p := NewPrompterFrom(script("", "", "", ""), &bytes.Buffer{})

	if got, _ := p.String("q", "fallback"); got != "fallback" {
		t.Errorf("String default = %q", got)
	}
	if got, _ := p.Confirm("q", true); !got {
		t.Error("Confirm default of true was not used")
	}
	if got, _ := p.Int("q", 42, 1, 100); got != 42 {
		t.Errorf("Int default = %d", got)
	}
	if got, _ := p.Optional("q", ""); got != "" {
		t.Errorf("Optional default = %q", got)
	}
}

func TestPrompterRejectsBadInputAndRetries(t *testing.T) {
	out := &bytes.Buffer{}
	p := NewPrompterFrom(script("maybe", "y"), out)

	got, err := p.Confirm("q", false)
	if err != nil {
		t.Fatalf("confirm: %s", err)
	}
	if !got {
		t.Error("retry answer was not used")
	}
	if !strings.Contains(out.String(), "answer y or n") {
		t.Error("no guidance was shown after invalid input")
	}
}

func TestPrompterAbortsOnEndOfInput(t *testing.T) {
	p := NewPrompterFrom(bytes.NewBufferString(""), &bytes.Buffer{})
	if _, err := p.String("q", ""); err != ErrAborted {
		t.Errorf("err = %v, want ErrAborted", err)
	}
}

// chdir moves into dir for the duration of a test, since the wizards write
// certificates and databases relative to the working directory.
func chdir(t *testing.T, dir string) {
	t.Helper()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %s", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %s", err)
	}
	t.Cleanup(func() { os.Chdir(previous) })
}
