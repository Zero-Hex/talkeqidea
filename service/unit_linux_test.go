//go:build linux

package service

import (
	"bytes"
	"strings"
	"testing"
)

// The unit file is the part of Linux support an operator is most likely to
// read and least likely to test, so its contents are asserted directly.
func TestUnitFileContents(t *testing.T) {
	def := Definition{
		Name:             "talkeq-hub",
		DisplayName:      "TalkEQ Hub",
		Description:      "Relays EverQuest chat.",
		ExecutablePath:   "/opt/talkeq/talkeq-hub",
		WorkingDirectory: "/opt/talkeq",
	}

	buf := &bytes.Buffer{}
	if err := unitTemplate.Execute(buf, def); err != nil {
		t.Fatalf("render unit: %s", err)
	}
	unit := buf.String()

	required := []string{
		"Description=Relays EverQuest chat.",
		"ExecStart=/opt/talkeq/talkeq-hub",
		// Without this the service starts in / and finds no talkeq.conf.
		"WorkingDirectory=/opt/talkeq",
		// Without this the hardened ProtectSystem=strict makes the config,
		// roster and log unwritable.
		"ReadWritePaths=/opt/talkeq",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
		// Wait for real connectivity, not just for the network stack to exist,
		// or the hub races DNS on boot.
		"After=network-online.target",
	}
	for _, line := range required {
		if !strings.Contains(unit, line) {
			t.Errorf("unit is missing %q\n\n%s", line, unit)
		}
	}

	hardening := []string{
		"NoNewPrivileges=true",
		"PrivateTmp=true",
		"ProtectSystem=strict",
		"ProtectHome=true",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX",
	}
	for _, line := range hardening {
		if !strings.Contains(unit, line) {
			t.Errorf("unit is missing hardening directive %q", line)
		}
	}
}

func TestUnitFileOmitsUserWhenUnset(t *testing.T) {
	buf := &bytes.Buffer{}
	err := unitTemplate.Execute(buf, Definition{
		Name:             "talkeq-hub",
		ExecutablePath:   "/opt/talkeq/talkeq-hub",
		WorkingDirectory: "/opt/talkeq",
	})
	if err != nil {
		t.Fatalf("render unit: %s", err)
	}

	if strings.Contains(buf.String(), "User=") {
		t.Errorf("unit sets an empty User=, which systemd rejects:\n\n%s", buf.String())
	}
}

func TestUnitFileIncludesUserWhenSet(t *testing.T) {
	buf := &bytes.Buffer{}
	err := unitTemplate.Execute(buf, Definition{
		Name:             "talkeq-hub",
		ExecutablePath:   "/opt/talkeq/talkeq-hub",
		WorkingDirectory: "/opt/talkeq",
		User:             "talkeq",
	})
	if err != nil {
		t.Fatalf("render unit: %s", err)
	}

	if !strings.Contains(buf.String(), "User=talkeq\n") {
		t.Errorf("unit does not set the requested user:\n\n%s", buf.String())
	}
}
