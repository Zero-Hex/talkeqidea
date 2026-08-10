package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestRoster(t *testing.T) (*Roster, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agents.json")
	r, err := NewRoster(path)
	if err != nil {
		t.Fatalf("new roster: %s", err)
	}
	return r, path
}

func TestRosterAddAndAuthenticate(t *testing.T) {
	r, _ := newTestRoster(t)

	token, err := r.Add("Server One", "Vanilla")
	if err != nil {
		t.Fatalf("add: %s", err)
	}

	entry, err := r.Authenticate("server-one", token)
	if err != nil {
		t.Fatalf("authenticate: %s", err)
	}
	if entry.ServerKey != "server-one" {
		t.Errorf("server key = %q, want server-one (sanitized)", entry.ServerKey)
	}
	if entry.ShortName != "Vanilla" {
		t.Errorf("short name = %q, want Vanilla", entry.ShortName)
	}

	if _, err := r.Authenticate("server-one", "wrong"); err == nil {
		t.Error("wrong token authenticated")
	}
	if _, err := r.Authenticate("server-one", ""); err == nil {
		t.Error("empty token authenticated")
	}
}

// Identity comes from the token, not from the name the agent claims.
func TestRosterAuthenticateIgnoresClaimedKey(t *testing.T) {
	r, _ := newTestRoster(t)

	if _, err := r.Add("server1", "Vanilla"); err != nil {
		t.Fatalf("add server1: %s", err)
	}
	token2, err := r.Add("server2", "Classic")
	if err != nil {
		t.Fatalf("add server2: %s", err)
	}

	entry, err := r.Authenticate("server1", token2)
	if err != nil {
		t.Fatalf("authenticate: %s", err)
	}
	if entry.ServerKey != "server2" {
		t.Errorf("resolved to %q, want server2 - identity must follow the token", entry.ServerKey)
	}
}

func TestRosterRotateInvalidatesOldToken(t *testing.T) {
	r, _ := newTestRoster(t)

	old, err := r.Add("server1", "Vanilla")
	if err != nil {
		t.Fatalf("add: %s", err)
	}
	fresh, err := r.Rotate("server1")
	if err != nil {
		t.Fatalf("rotate: %s", err)
	}
	if old == fresh {
		t.Fatal("rotate returned the same token")
	}

	if _, err := r.Authenticate("server1", old); err == nil {
		t.Error("old token still works after rotation")
	}
	if _, err := r.Authenticate("server1", fresh); err != nil {
		t.Errorf("new token rejected: %s", err)
	}
}

func TestRosterDisableBlocksAuthentication(t *testing.T) {
	r, _ := newTestRoster(t)

	token, err := r.Add("server1", "Vanilla")
	if err != nil {
		t.Fatalf("add: %s", err)
	}
	if err := r.SetEnabled("server1", false); err != nil {
		t.Fatalf("disable: %s", err)
	}
	if _, err := r.Authenticate("server1", token); err == nil {
		t.Error("disabled agent authenticated")
	}

	if err := r.SetEnabled("server1", true); err != nil {
		t.Fatalf("enable: %s", err)
	}
	if _, err := r.Authenticate("server1", token); err != nil {
		t.Errorf("re-enabled agent rejected: %s", err)
	}
}

func TestRosterRejectsDuplicateAndReservedKeys(t *testing.T) {
	r, _ := newTestRoster(t)

	if _, err := r.Add("server1", "Vanilla"); err != nil {
		t.Fatalf("add: %s", err)
	}
	if _, err := r.Add("server1", "Other"); err == nil {
		t.Error("duplicate server key accepted")
	}
	if _, err := r.Add("discord", "Discord"); err == nil {
		t.Error("reserved key 'discord' accepted")
	}
	if _, err := r.Add("!!!", ""); err == nil {
		t.Error("key that sanitizes to empty accepted")
	}
}

// A leaked roster file must not hand out relay access.
func TestRosterFileStoresNoPlaintextToken(t *testing.T) {
	r, path := newTestRoster(t)

	token, err := r.Add("server1", "Vanilla")
	if err != nil {
		t.Fatalf("add: %s", err)
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read roster: %s", err)
	}
	if strings.Contains(string(buf), token) {
		t.Fatal("roster file contains the token in the clear")
	}
	if !strings.Contains(string(buf), "argon2id") {
		t.Error("roster file does not appear to store a hash")
	}
}

func TestRosterPersistsAcrossReload(t *testing.T) {
	r, path := newTestRoster(t)

	token, err := r.Add("server1", "Vanilla")
	if err != nil {
		t.Fatalf("add: %s", err)
	}

	reloaded, err := NewRoster(path)
	if err != nil {
		t.Fatalf("reload: %s", err)
	}
	entry, err := reloaded.Authenticate("server1", token)
	if err != nil {
		t.Fatalf("authenticate after reload: %s", err)
	}
	if entry.ShortName != "Vanilla" {
		t.Errorf("short name = %q, want Vanilla", entry.ShortName)
	}
}

func TestRosterEntriesSorted(t *testing.T) {
	r, _ := newTestRoster(t)

	for _, key := range []string{"zulu", "alpha", "mike"} {
		if _, err := r.Add(key, ""); err != nil {
			t.Fatalf("add %s: %s", key, err)
		}
	}

	entries := r.Entries()
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].ServerKey != "alpha" || entries[2].ServerKey != "zulu" {
		t.Errorf("entries are not sorted: %v", entries)
	}
}
