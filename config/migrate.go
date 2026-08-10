package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Migration from the TalkEQ file layout.
//
// The project was renamed, and with it every file it writes. An operator
// upgrading in place should not have to rename anything by hand, and must not
// be met with a setup wizard that ignores their existing configuration - so the
// old names are detected and moved on startup.

// legacyConfigNames are the config filenames this program used to use, newest
// first.
var legacyConfigNames = []string{"talkeq.conf"}

// legacyDataNames maps each old data filename to its replacement. These appear
// both on disk and as values inside the config file, so both are updated.
var legacyDataNames = map[string]string{
	"talkeq_agents.json":   "modern-eq-chat-agents.json",
	"talkeq_enroll.json":   "modern-eq-chat-enroll.json",
	"talkeq_users.txt":     "modern-eq-chat-users.txt",
	"talkeq_guilds.txt":    "modern-eq-chat-guilds.txt",
	"talkeq_register.toml": "modern-eq-chat-register.toml",
	"talkeq_hub_cert.pem":  "modern-eq-chat-cert.pem",
	"talkeq_hub_key.pem":   "modern-eq-chat-key.pem",
}

// Migrate moves a pre-rename installation to the current filenames.
//
// It returns a description of everything it did, so the caller can tell the
// operator rather than silently rearranging their directory. Doing nothing is
// the normal case and returns an empty slice.
func Migrate(path string) ([]string, error) {
	if path == "" {
		path = DefaultPath
	}
	dir := filepath.Dir(path)

	var actions []string

	// The config file itself. Only when there is no current one, so a fresh
	// install that happens to sit beside an old file is never overwritten.
	if !Exists(path) {
		for _, legacy := range legacyConfigNames {
			legacyPath := filepath.Join(dir, legacy)
			if !Exists(legacyPath) {
				continue
			}
			if err := os.Rename(legacyPath, path); err != nil {
				return actions, fmt.Errorf("rename %s to %s: %w", legacy, filepath.Base(path), err)
			}
			actions = append(actions, fmt.Sprintf("renamed %s to %s", legacy, filepath.Base(path)))
			break
		}
	}

	if !Exists(path) {
		// Nothing to migrate; this is a first run.
		return actions, nil
	}

	cfg, err := Load(path)
	if err != nil {
		return actions, fmt.Errorf("read config during migration: %w", err)
	}

	// Every config field that names a file on disk. Renaming the file without
	// updating the field would leave the config pointing at something that no
	// longer exists.
	fields := []*string{
		&cfg.UsersDatabasePath,
		&cfg.GuildsDatabasePath,
		&cfg.API.APIRegister.RegistrationDatabasePath,
		&cfg.Relay.Hub.AgentsDatabase,
		&cfg.Relay.Hub.EnrollDatabase,
		&cfg.Relay.Hub.TLSCertPath,
		&cfg.Relay.Hub.TLSKeyPath,
	}

	isConfigChanged := false
	for _, field := range fields {
		renamed, err := migrateDataFile(dir, *field)
		if err != nil {
			return actions, err
		}
		if renamed == "" {
			continue
		}
		if renamed != *field {
			*field = renamed
			isConfigChanged = true
		}
	}

	// Files that exist on disk under an old name but are not referenced by the
	// config - the certificate on a hub that never customized its paths, say.
	for legacy, current := range legacyDataNames {
		legacyPath := filepath.Join(dir, legacy)
		currentPath := filepath.Join(dir, current)

		if !fileExists(legacyPath) || fileExists(currentPath) {
			continue
		}
		if err := os.Rename(legacyPath, currentPath); err != nil {
			return actions, fmt.Errorf("rename %s to %s: %w", legacy, current, err)
		}
		actions = append(actions, fmt.Sprintf("renamed %s to %s", legacy, current))
	}

	if isConfigChanged {
		if err := Save(cfg, path); err != nil {
			return actions, fmt.Errorf("save migrated config: %w", err)
		}
		actions = append(actions, fmt.Sprintf("updated file paths inside %s", filepath.Base(path)))
	}

	return actions, nil
}

// migrateDataFile renames one referenced file and returns its new name.
//
// An empty return means the field named nothing recognizable and should be
// left alone. A field an operator has pointed somewhere custom is untouched:
// only the exact old defaults are migrated.
func migrateDataFile(dir, value string) (string, error) {
	if value == "" {
		return "", nil
	}

	base := filepath.Base(value)
	current, ok := legacyDataNames[base]
	if !ok {
		return value, nil
	}

	// Preserve any directory the operator configured.
	newValue := current
	if parent := filepath.Dir(value); parent != "." && parent != "" {
		newValue = filepath.Join(parent, current)
	}

	oldPath := value
	if !filepath.IsAbs(oldPath) {
		oldPath = filepath.Join(dir, value)
	}
	newPath := newValue
	if !filepath.IsAbs(newPath) {
		newPath = filepath.Join(dir, newValue)
	}

	if fileExists(oldPath) && !fileExists(newPath) {
		if err := os.Rename(oldPath, newPath); err != nil {
			return "", fmt.Errorf("rename %s to %s: %w", oldPath, newPath, err)
		}
	}
	return newValue, nil
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
