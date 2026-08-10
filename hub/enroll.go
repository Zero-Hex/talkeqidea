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

// EnrollStore holds outstanding enrollment codes.
//
// Like the roster it lives in a file, because the operator creates codes from
// the command line while the hub runs as a separate process. Codes are stored
// as argon2id hashes: the file is short-lived state, but a code is a bearer
// credential for the few minutes it lives, and there is no reason to write one
// down in the clear.
type EnrollStore struct {
	mu            sync.Mutex
	path          string
	entries       map[string]*EnrollEntry
	loadedModTime time.Time
}

// EnrollEntry is one outstanding code.
type EnrollEntry struct {
	// ID is a public, non-secret handle so entries can be addressed without
	// knowing the code.
	ID string `json:"id"`
	// CodeHash is the argon2id hash of the code itself.
	CodeHash string `json:"code_hash"`
	// ServerKey and ShortName are assigned by the hub operator when the code
	// is created, so the naming of servers stays under hub control.
	ServerKey string `json:"server_key"`
	ShortName string `json:"short_name"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
	// Attempts counts failed presentations. A code burns after a handful, so a
	// guess-and-retry attack cannot grind against it even within its lifetime.
	Attempts int `json:"attempts,omitempty"`
}

type enrollFile struct {
	Codes []*EnrollEntry `json:"codes"`
}

// DefaultEnrollTTL is how long a code stays valid. Long enough to walk to
// another machine, short enough that a forgotten code is not a standing risk.
const DefaultEnrollTTL = 15 * time.Minute

// maxEnrollAttempts is how many failed presentations a code tolerates.
const maxEnrollAttempts = 5

// IsExpired reports whether the code can no longer be used.
func (e *EnrollEntry) IsExpired() bool {
	return time.Now().Unix() > e.ExpiresAt || e.Attempts >= maxEnrollAttempts
}

// NewEnrollStore opens the enrollment store at path.
func NewEnrollStore(path string) (*EnrollStore, error) {
	if path == "" {
		path = "modern-eq-chat-enroll.json"
	}
	s := &EnrollStore{
		path:    path,
		entries: make(map[string]*EnrollEntry),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *EnrollStore) loadLocked() error {
	fi, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat enroll store: %w", err)
	}

	buf, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read enroll store: %w", err)
	}

	ef := enrollFile{}
	if err := json.Unmarshal(buf, &ef); err != nil {
		return fmt.Errorf("parse enroll store %s: %w", s.path, err)
	}

	s.entries = make(map[string]*EnrollEntry, len(ef.Codes))
	for _, entry := range ef.Codes {
		if entry.ID == "" {
			continue
		}
		s.entries[entry.ID] = entry
	}
	s.loadedModTime = fi.ModTime()
	return nil
}

func (s *EnrollStore) refreshLocked() {
	fi, err := os.Stat(s.path)
	if err != nil {
		return
	}
	if fi.ModTime().Equal(s.loadedModTime) {
		return
	}
	if err := s.loadLocked(); err != nil {
		tlog.Warnf("[hub] enrollment store changed on disk but could not be reloaded: %s", err)
	}
}

// Create issues a new enrollment code for the given server.
func (s *EnrollStore) Create(serverKey, shortName string, ttl time.Duration) (string, error) {
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
	if ttl <= 0 {
		ttl = DefaultEnrollTTL
	}

	code, err := relay.NewEnrollCode()
	if err != nil {
		return "", fmt.Errorf("new code: %w", err)
	}
	hash, err := relay.HashToken(relay.NormalizeEnrollCode(code))
	if err != nil {
		return "", fmt.Errorf("hash code: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.refreshLocked()
	s.pruneLocked()

	entry := &EnrollEntry{
		ID:        relay.NewID(),
		CodeHash:  hash,
		ServerKey: serverKey,
		ShortName: shortName,
		ExpiresAt: time.Now().Add(ttl).Unix(),
		CreatedAt: time.Now().Unix(),
	}
	s.entries[entry.ID] = entry

	if err := s.saveLocked(); err != nil {
		delete(s.entries, entry.ID)
		return "", err
	}
	return code, nil
}

// Redeem consumes a code and returns the server it was issued for.
//
// A successful redemption deletes the entry, so a code works exactly once even
// if two agents race to present it.
func (s *EnrollStore) Redeem(code string) (*EnrollEntry, error) {
	normalized := relay.NormalizeEnrollCode(code)
	if len(normalized) != relay.EnrollCodeLength {
		return nil, fmt.Errorf("malformed enrollment code")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.refreshLocked()
	s.pruneLocked()

	for id, entry := range s.entries {
		if !relay.VerifyToken(normalized, entry.CodeHash) {
			continue
		}
		if entry.IsExpired() {
			delete(s.entries, id)
			_ = s.saveLocked()
			return nil, fmt.Errorf("enrollment code has expired")
		}

		redeemed := *entry
		delete(s.entries, id)
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
		return &redeemed, nil
	}

	// No match. Charge an attempt against every live code so repeated guessing
	// burns the outstanding codes rather than running indefinitely.
	for _, entry := range s.entries {
		entry.Attempts++
	}
	s.pruneLocked()
	_ = s.saveLocked()

	return nil, fmt.Errorf("enrollment code not recognized")
}

// Pending lists outstanding codes. The codes themselves are not recoverable.
func (s *EnrollStore) Pending() []EnrollEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.refreshLocked()
	s.pruneLocked()

	out := make([]EnrollEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// Revoke cancels an outstanding code by ID.
func (s *EnrollStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.refreshLocked()
	if _, ok := s.entries[id]; !ok {
		return fmt.Errorf("no pending enrollment %s", id)
	}
	delete(s.entries, id)
	return s.saveLocked()
}

func (s *EnrollStore) pruneLocked() {
	for id, entry := range s.entries {
		if entry.IsExpired() {
			delete(s.entries, id)
		}
	}
}

func (s *EnrollStore) saveLocked() error {
	ef := enrollFile{Codes: make([]*EnrollEntry, 0, len(s.entries))}
	for _, entry := range s.entries {
		ef.Codes = append(ef.Codes, entry)
	}
	sort.Slice(ef.Codes, func(i, j int) bool { return ef.Codes[i].CreatedAt < ef.Codes[j].CreatedAt })

	buf, err := json.MarshalIndent(ef, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal enroll store: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".modern-eq-chat-enroll-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp enroll store: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp enroll store: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp enroll store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp enroll store: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace enroll store: %w", err)
	}
	if fi, err := os.Stat(s.path); err == nil {
		s.loadedModTime = fi.ModTime()
	}
	return nil
}
