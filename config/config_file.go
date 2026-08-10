package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jbsmith7741/toml"
)

// DefaultPath is where modern-eq-chat looks for its configuration.
const DefaultPath = "modern-eq-chat.conf"

// Default returns the built-in configuration, the same one written on first
// run. Exported so the setup wizards can start from it rather than from a
// zero value, which would lose every default and every explanatory comment.
func Default() Config {
	return getDefaultConfig()
}

// Exists reports whether a configuration file is already present.
func Exists(path string) bool {
	if path == "" {
		path = DefaultPath
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !fi.IsDir()
}

// Load reads a configuration without the first-run behavior of NewConfig,
// which creates a default file and exits the process. The wizards need to read
// and rewrite config in place, so they use this instead.
//
// Verification is skipped: a half-configured file is exactly what a wizard
// expects to find, and rejecting it would make the config unfixable through
// the tool meant to fix it. Call Verify yourself when you need a usable config.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	cfg := Config{}
	if _, err := toml.DecodeReader(f, &cfg); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &cfg, nil
}

// LoadOrDefault reads the configuration at path, falling back to the built-in
// defaults when no file exists yet.
func LoadOrDefault(path string) (*Config, error) {
	if !Exists(path) {
		cfg := Default()
		return &cfg, nil
	}
	return Load(path)
}

// Save writes the configuration, preserving the descriptive comments that make
// modern-eq-chat.conf self-documenting.
//
// The write goes to a temporary file and is renamed into place, so an
// interrupted save cannot leave an operator with a truncated config and a hub
// that will not start.
func Save(cfg *Config, path string) error {
	if path == "" {
		path = DefaultPath
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".modern-eq-chat-conf-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(cfg); err != nil {
		tmp.Close()
		return fmt.Errorf("encode config: %w", err)
	}

	// The config holds a Discord bot token and, on agents, a relay token.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// Backup copies the current config aside before a wizard rewrites it, so an
// operator who mis-answers a prompt has something to go back to.
func Backup(path string) (string, error) {
	if path == "" {
		path = DefaultPath
	}
	if !Exists(path) {
		return "", nil
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}

	backupPath := path + ".bak"
	if err := os.WriteFile(backupPath, buf, 0o600); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}
	return backupPath, nil
}
