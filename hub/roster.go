package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Zero-Hex/modern-eq-chat/relay"
	"github.com/Zero-Hex/modern-eq-chat/sanitize"
	"github.com/Zero-Hex/modern-eq-chat/tlog"
)

// Roster is the hub's record of which agents are allowed to connect.
//
// It holds argon2id hashes, never tokens, so the file can be backed up or
// read by an operator without handing out relay access. It is managed through
// the `modern-eq-chat agent` subcommands rather than hand-edited, which is why it is
// JSON rather than the commented TOML used for operator-facing config.
type Roster struct {
	mu      sync.RWMutex
	path    string
	entries map[string]*RosterEntry
	// loadedModTime is the file's modification time as of the last read. The
	// administrative commands run as a separate process from the hub, so both
	// hold the roster open at once; comparing this before every access lets a
	// running hub pick up an agent added from the CLI, and stops its next save
	// from clobbering that addition.
	loadedModTime time.Time
}

// RosterEntry is one authorized agent.
type RosterEntry struct {
	ServerKey  string `json:"server_key"`
	ShortName  string `json:"short_name"`
	TokenHash  string `json:"token_hash"`
	IsDisabled bool   `json:"disabled,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at,omitempty"`
}

type rosterFile struct {
	Agents []*RosterEntry `json:"agents"`
}

// NewRoster loads the roster at path, creating an empty one if absent.
func NewRoster(path string) (*Roster, error) {
	if path == "" {
		path = "modern-eq-chat-agents.json"
	}
	r := &Roster{
		path:    path,
		entries: make(map[string]*RosterEntry),
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.loadLocked(); err != nil {
		return nil, err
	}
	if r.loadedModTime.IsZero() {
		// No file yet; write an empty one so the path and its permissions are
		// established before the first agent is added.
		if err := r.saveLocked(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// loadLocked reads the roster from disk. A missing file is not an error: it
// means no agents have been added yet.
func (r *Roster) loadLocked() error {
	fi, err := os.Stat(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat roster: %w", err)
	}

	buf, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("read roster: %w", err)
	}

	rf := rosterFile{}
	if err := json.Unmarshal(buf, &rf); err != nil {
		return fmt.Errorf("parse roster %s: %w", r.path, err)
	}

	r.entries = make(map[string]*RosterEntry, len(rf.Agents))
	for _, entry := range rf.Agents {
		if entry.ServerKey == "" {
			continue
		}
		r.entries[entry.ServerKey] = entry
	}
	r.loadedModTime = fi.ModTime()
	return nil
}

// refreshLocked re-reads the roster when another process has written it.
//
// Called at the start of every read and every mutation. Without it, a running
// hub would neither see an agent added by the CLI nor preserve it: the hub's
// next save writes its own stale map over the file.
func (r *Roster) refreshLocked() {
	fi, err := os.Stat(r.path)
	if err != nil {
		return
	}
	if fi.ModTime().Equal(r.loadedModTime) {
		return
	}
	if err := r.loadLocked(); err != nil {
		tlog.Warnf("[hub] roster changed on disk but could not be reloaded: %s", err)
	}
}

// Add registers a new agent and returns its token. The token is shown once,
// here, and only its hash is kept — there is no way to recover it later, which
// is the property that makes a leaked roster harmless.
func (r *Roster) Add(serverKey, shortName string) (string, error) {
	serverKey = sanitize.ServerKey(serverKey)
	if serverKey == "" {
		return "", fmt.Errorf("server key is empty after sanitizing")
	}
	if serverKey == relay.OriginDiscord {
		return "", fmt.Errorf("%q is reserved for Discord-originated messages", relay.OriginDiscord)
	}
	if shortName == "" {
		shortName = serverKey
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.refreshLocked()

	if _, ok := r.entries[serverKey]; ok {
		return "", fmt.Errorf("agent %s already exists (use rotate to issue a new token)", serverKey)
	}

	token, err := relay.NewToken()
	if err != nil {
		return "", fmt.Errorf("new token: %w", err)
	}
	hash, err := relay.HashToken(token)
	if err != nil {
		return "", fmt.Errorf("hash token: %w", err)
	}

	r.entries[serverKey] = &RosterEntry{
		ServerKey: serverKey,
		ShortName: shortName,
		TokenHash: hash,
		CreatedAt: time.Now().Unix(),
	}
	if err := r.saveLocked(); err != nil {
		delete(r.entries, serverKey)
		return "", err
	}
	return token, nil
}

// Rotate issues a new token for an existing agent, invalidating the old one.
func (r *Roster) Rotate(serverKey string) (string, error) {
	serverKey = sanitize.ServerKey(serverKey)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.refreshLocked()

	entry, ok := r.entries[serverKey]
	if !ok {
		return "", fmt.Errorf("agent %s not found", serverKey)
	}

	token, err := relay.NewToken()
	if err != nil {
		return "", fmt.Errorf("new token: %w", err)
	}
	hash, err := relay.HashToken(token)
	if err != nil {
		return "", fmt.Errorf("hash token: %w", err)
	}

	previous := entry.TokenHash
	entry.TokenHash = hash
	if err := r.saveLocked(); err != nil {
		entry.TokenHash = previous
		return "", err
	}
	return token, nil
}

// Remove deletes an agent from the roster.
func (r *Roster) Remove(serverKey string) error {
	serverKey = sanitize.ServerKey(serverKey)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.refreshLocked()

	if _, ok := r.entries[serverKey]; !ok {
		return fmt.Errorf("agent %s not found", serverKey)
	}
	delete(r.entries, serverKey)
	return r.saveLocked()
}

// SetEnabled toggles an agent without discarding its token.
func (r *Roster) SetEnabled(serverKey string, isEnabled bool) error {
	serverKey = sanitize.ServerKey(serverKey)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.refreshLocked()

	entry, ok := r.entries[serverKey]
	if !ok {
		return fmt.Errorf("agent %s not found", serverKey)
	}
	entry.IsDisabled = !isEnabled
	return r.saveLocked()
}

// Rename changes an agent's display name without touching its token.
func (r *Roster) Rename(serverKey, shortName string) error {
	serverKey = sanitize.ServerKey(serverKey)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.refreshLocked()

	entry, ok := r.entries[serverKey]
	if !ok {
		return fmt.Errorf("agent %s not found", serverKey)
	}

	previous := entry.ShortName
	entry.ShortName = shortName
	if err := r.saveLocked(); err != nil {
		entry.ShortName = previous
		return err
	}
	return nil
}

// Authenticate resolves a token to the agent it belongs to.
//
// It deliberately ignores the server key the agent claimed and checks the
// token against every entry, so identity is derived purely from the secret.
// An agent cannot connect as a server it does not hold the token for.
//
// The claimed key is used only to order the search, keeping the common case
// at one hash verification while a wrong claim still resolves correctly.
func (r *Roster) Authenticate(claimedKey, token string) (*RosterEntry, error) {
	if token == "" {
		return nil, fmt.Errorf("no token supplied")
	}

	// Take the write lock: a refresh may replace the entry map.
	r.mu.Lock()
	r.refreshLocked()
	candidates := make([]*RosterEntry, 0, len(r.entries))
	if entry, ok := r.entries[sanitize.ServerKey(claimedKey)]; ok {
		candidates = append(candidates, entry)
	}
	for key, entry := range r.entries {
		if key == sanitize.ServerKey(claimedKey) {
			continue
		}
		candidates = append(candidates, entry)
	}
	r.mu.Unlock()

	for _, entry := range candidates {
		if !relay.VerifyToken(token, entry.TokenHash) {
			continue
		}
		if entry.IsDisabled {
			return nil, fmt.Errorf("agent %s is disabled", entry.ServerKey)
		}
		return entry, nil
	}
	return nil, fmt.Errorf("token not recognized")
}

// Entry returns a copy of an agent's record.
func (r *Roster) Entry(serverKey string) (RosterEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.refreshLocked()
	entry, ok := r.entries[sanitize.ServerKey(serverKey)]
	if !ok {
		return RosterEntry{}, false
	}
	return *entry, true
}

// Entries returns every agent, sorted by server key.
func (r *Roster) Entries() []RosterEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.refreshLocked()

	out := make([]RosterEntry, 0, len(r.entries))
	for _, entry := range r.entries {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServerKey < out[j].ServerKey })
	return out
}

// MarkSeen records that an agent connected.
func (r *Roster) MarkSeen(serverKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.refreshLocked()
	entry, ok := r.entries[serverKey]
	if !ok {
		return
	}
	entry.LastSeenAt = time.Now().Unix()
	// A failed save here is not worth failing the connection over; the roster
	// is still correct in memory and last-seen is informational.
	_ = r.saveLocked()
}

// save writes the roster atomically so a crash mid-write cannot leave the hub
// unable to authenticate any agent.
func (r *Roster) saveLocked() error {
	rf := rosterFile{Agents: make([]*RosterEntry, 0, len(r.entries))}
	for _, entry := range r.entries {
		rf.Agents = append(rf.Agents, entry)
	}
	sort.Slice(rf.Agents, func(i, j int) bool { return rf.Agents[i].ServerKey < rf.Agents[j].ServerKey })

	buf, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal roster: %w", err)
	}

	dir := filepath.Dir(r.path)
	tmp, err := os.CreateTemp(dir, ".modern-eq-chat-agents-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp roster: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp roster: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp roster: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp roster: %w", err)
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return fmt.Errorf("replace roster: %w", err)
	}

	// Record what we just wrote so the next refresh does not treat our own
	// write as someone else's change.
	if fi, err := os.Stat(r.path); err == nil {
		r.loadedModTime = fi.ModTime()
	}
	return nil
}
