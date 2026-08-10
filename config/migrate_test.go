package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Zero-Hex/modern-eq-chat/relay"
)

// An operator upgrading from TalkEQ must end up with their existing setup
// intact under the new names, without touching anything themselves.
func TestMigrateRenamesConfigAndData(t *testing.T) {
	dir := t.TempDir()

	seed := Default()
	seed.Relay.Mode = ModeHub
	seed.Discord.Token = "keep-me"
	seed.UsersDatabasePath = "talkeq_users.txt"
	seed.GuildsDatabasePath = "talkeq_guilds.txt"
	seed.Relay.Hub.AgentsDatabase = "talkeq_agents.json"
	seed.Relay.Hub.EnrollDatabase = "talkeq_enroll.json"
	seed.Relay.Hub.TLSCertPath = "talkeq_hub_cert.pem"
	seed.Relay.Hub.TLSKeyPath = "talkeq_hub_key.pem"

	legacyConfig := filepath.Join(dir, "talkeq.conf")
	if err := Save(&seed, legacyConfig); err != nil {
		t.Fatalf("seed config: %s", err)
	}

	// The data files an existing install would have beside it.
	legacyData := map[string]string{
		"talkeq_users.txt":    "1234:Soandso",
		"talkeq_guilds.txt":   "1:5678",
		"talkeq_agents.json":  `{"agents":[]}`,
		"talkeq_hub_cert.pem": "CERT",
		"talkeq_hub_key.pem":  "KEY",
	}
	for name, body := range legacyData {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("seed %s: %s", name, err)
		}
	}

	target := filepath.Join(dir, "modern-eq-chat.conf")
	actions, err := Migrate(target)
	if err != nil {
		t.Fatalf("migrate: %s", err)
	}
	if len(actions) == 0 {
		t.Fatal("migration reported no actions")
	}

	if Exists(legacyConfig) {
		t.Error("the old config file is still there")
	}
	if !Exists(target) {
		t.Fatal("the config was not renamed")
	}

	// Data files moved, contents preserved.
	expected := map[string]string{
		"modern-eq-chat-users.txt":   "1234:Soandso",
		"modern-eq-chat-guilds.txt":  "1:5678",
		"modern-eq-chat-agents.json": `{"agents":[]}`,
		"modern-eq-chat-cert.pem":    "CERT",
		"modern-eq-chat-key.pem":     "KEY",
	}
	for name, want := range expected {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s was not created: %s", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s contents = %q, want %q", name, got, want)
		}
	}
	for name := range legacyData {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was left behind", name)
		}
	}

	// The config must now point at the new names, or it would reference files
	// that no longer exist.
	migrated, err := Load(target)
	if err != nil {
		t.Fatalf("load migrated config: %s", err)
	}
	if migrated.UsersDatabasePath != "modern-eq-chat-users.txt" {
		t.Errorf("users_database = %q", migrated.UsersDatabasePath)
	}
	if migrated.Relay.Hub.AgentsDatabase != "modern-eq-chat-agents.json" {
		t.Errorf("agents_database = %q", migrated.Relay.Hub.AgentsDatabase)
	}
	if migrated.Relay.Hub.TLSCertPath != "modern-eq-chat-cert.pem" {
		t.Errorf("tls_cert = %q", migrated.Relay.Hub.TLSCertPath)
	}
	if migrated.Discord.Token != "keep-me" {
		t.Errorf("unrelated settings were lost: token = %q", migrated.Discord.Token)
	}
}

// A fresh install has nothing to migrate and must not be disturbed.
func TestMigrateIsANoOpOnAFreshInstall(t *testing.T) {
	dir := t.TempDir()

	actions, err := Migrate(filepath.Join(dir, "modern-eq-chat.conf"))
	if err != nil {
		t.Fatalf("migrate: %s", err)
	}
	if len(actions) != 0 {
		t.Errorf("migration acted on an empty directory: %v", actions)
	}
}

// A current config sitting beside a leftover old one must win. Overwriting it
// would silently discard the operator's real configuration.
func TestMigrateNeverOverwritesACurrentConfig(t *testing.T) {
	dir := t.TempDir()

	current := Default()
	current.Discord.Token = "current"
	target := filepath.Join(dir, "modern-eq-chat.conf")
	if err := Save(&current, target); err != nil {
		t.Fatalf("seed current: %s", err)
	}

	legacy := Default()
	legacy.Discord.Token = "legacy"
	if err := Save(&legacy, filepath.Join(dir, "talkeq.conf")); err != nil {
		t.Fatalf("seed legacy: %s", err)
	}

	if _, err := Migrate(target); err != nil {
		t.Fatalf("migrate: %s", err)
	}

	got, err := Load(target)
	if err != nil {
		t.Fatalf("load: %s", err)
	}
	if got.Discord.Token != "current" {
		t.Errorf("the current config was overwritten by the old one: token = %q", got.Discord.Token)
	}
}

// An operator who pointed a path somewhere custom must keep it.
func TestMigrateLeavesCustomPathsAlone(t *testing.T) {
	dir := t.TempDir()

	seed := Default()
	seed.UsersDatabasePath = "/opt/eq/my-users.txt"
	target := filepath.Join(dir, "modern-eq-chat.conf")
	if err := Save(&seed, target); err != nil {
		t.Fatalf("seed: %s", err)
	}

	if _, err := Migrate(target); err != nil {
		t.Fatalf("migrate: %s", err)
	}

	got, err := Load(target)
	if err != nil {
		t.Fatalf("load: %s", err)
	}
	if got.UsersDatabasePath != "/opt/eq/my-users.txt" {
		t.Errorf("a custom path was rewritten: %q", got.UsersDatabasePath)
	}
}

// A legacy name inside a custom directory keeps that directory.
func TestMigratePreservesConfiguredDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "data")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %s", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "talkeq_users.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %s", err)
	}

	seed := Default()
	seed.UsersDatabasePath = "data/talkeq_users.txt"
	target := filepath.Join(dir, "modern-eq-chat.conf")
	if err := Save(&seed, target); err != nil {
		t.Fatalf("seed config: %s", err)
	}

	if _, err := Migrate(target); err != nil {
		t.Fatalf("migrate: %s", err)
	}

	got, err := Load(target)
	if err != nil {
		t.Fatalf("load: %s", err)
	}
	want := filepath.Join("data", "modern-eq-chat-users.txt")
	if got.UsersDatabasePath != want {
		t.Errorf("users_database = %q, want %q", got.UsersDatabasePath, want)
	}
	if _, err := os.Stat(filepath.Join(sub, "modern-eq-chat-users.txt")); err != nil {
		t.Errorf("file was not moved within its directory: %s", err)
	}
}

// Join codes issued before the rename must still work, so an operator midway
// through adding a server is not stranded by upgrading the hub.
func TestMigratedInstallStillParsesOldJoinCodes(t *testing.T) {
	code := &relay.JoinCode{
		Address:   "hub.example.com:34197",
		ServerKey: "server2",
		Token:     "abc123",
	}
	encoded, err := code.Encode()
	if err != nil {
		t.Fatalf("encode: %s", err)
	}

	// Re-label it with the pre-rename prefix, as an older hub would have.
	legacy := "talkeq1_" + strings.TrimPrefix(encoded, "meqc1_")

	parsed, err := relay.ParseJoinCode(legacy)
	if err != nil {
		t.Fatalf("a join code issued before the rename was rejected: %s", err)
	}
	if parsed.ServerKey != "server2" || parsed.Token != "abc123" {
		t.Errorf("legacy join code decoded incorrectly: %+v", parsed)
	}
}
