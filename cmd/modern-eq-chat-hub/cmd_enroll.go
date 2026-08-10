package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/hub"
)

// openHub loads the configuration and prepares a hub for administrative use,
// without starting the listener.
//
// The roster and enrollment store are files, so these commands work whether or
// not the hub is running; both sides reload when the other writes.
func openHub() (*hub.Hub, error) {
	if !config.Exists(config.DefaultPath) {
		return nil, fmt.Errorf("no %s found - run 'modern-eq-chat-hub setup' first", config.DefaultPath)
	}

	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := cfg.Relay.Verify(); err != nil {
		return nil, fmt.Errorf("relay config: %w", err)
	}
	if cfg.Relay.Mode != config.ModeHub {
		return nil, fmt.Errorf("this command runs on the hub, but relay.mode is %q in modern-eq-chat.conf", cfg.Relay.Mode)
	}

	h, err := hub.New(context.Background(), cfg.Relay.Hub)
	if err != nil {
		return nil, fmt.Errorf("hub: %w", err)
	}
	return h, nil
}

// runEnrollCommand implements `modern-eq-chat-hub enroll ...`.
func runEnrollCommand(args []string) error {
	h, err := openHub()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return usageEnroll()
	}

	switch args[0] {
	case "list":
		return enrollList(h)

	case "revoke":
		if len(args) < 2 {
			return fmt.Errorf("usage: modern-eq-chat-hub enroll revoke <id>")
		}
		if err := h.Enroll().Revoke(args[1]); err != nil {
			return err
		}
		fmt.Printf("revoked %s\n", args[1])
		return nil

	case "help", "--help", "-h":
		return usageEnroll()

	default:
		// Anything else is a server key to issue a code for.
		shortName := ""
		if len(args) > 1 {
			shortName = args[1]
		}
		return enrollCreate(h, args[0], shortName)
	}
}

func enrollCreate(h *hub.Hub, serverKey, shortName string) error {
	if err := h.EnsureCertificate(); err != nil {
		return fmt.Errorf("certificate: %w", err)
	}

	code, err := h.Enroll().Create(serverKey, shortName, hub.DefaultEnrollTTL)
	if err != nil {
		return err
	}

	display := shortName
	if display == "" {
		display = serverKey
	}

	fmt.Printf("\nEnrollment code for %s:\n\n", display)
	fmt.Printf("    Hub address:      %s\n", h.AdvertiseAddress())
	fmt.Printf("    Enrollment code:  %s\n", code)
	if fingerprint := h.Fingerprint(); fingerprint != "" {
		fmt.Printf("    Fingerprint:      %s\n", fingerprint)
	}
	fmt.Printf("\n")
	fmt.Printf("On that server run 'modern-eq-chat-agent' and enter the address and code.\n")
	fmt.Printf("It will show the fingerprint and ask you to confirm it matches.\n")
	fmt.Printf("\nThe code works once and expires in %s. The hub must be running\n", hub.DefaultEnrollTTL)
	fmt.Printf("for the agent to enroll.\n\n")
	return nil
}

func enrollList(h *hub.Hub) error {
	pending := h.Enroll().Pending()
	if len(pending) == 0 {
		fmt.Println("no outstanding enrollment codes")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSERVER KEY\tDISPLAY NAME\tEXPIRES IN")
	for _, entry := range pending {
		remaining := time.Until(time.Unix(entry.ExpiresAt, 0)).Round(time.Second)
		if remaining < 0 {
			remaining = 0
		}
		// Only a prefix of the ID is needed to revoke, and it keeps the table
		// readable.
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", entry.ID[:8], entry.ServerKey, entry.ShortName, remaining)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("The codes themselves are not recoverable. Issue a new one if lost.")
	return nil
}

func usageEnroll() error {
	fmt.Println("usage: modern-eq-chat-hub enroll <server-key> [display name]")
	fmt.Println()
	fmt.Println("Issues a short, single-use code that a new server enters during its")
	fmt.Println("own setup to receive its permanent credentials.")
	fmt.Println()
	fmt.Println("  enroll <server-key> [display]   issue a code")
	fmt.Println("  enroll list                     show outstanding codes")
	fmt.Println("  enroll revoke <id>              cancel an outstanding code")
	return nil
}

// runStatusCommand prints what the hub knows about its servers.
func runStatusCommand() error {
	h, err := openHub()
	if err != nil {
		return err
	}

	entries := h.Roster().Entries()
	if len(entries) == 0 {
		fmt.Println("no servers authorized yet")
		fmt.Println()
		fmt.Println("add one with: modern-eq-chat-hub enroll <server-key> [display name]")
		return nil
	}

	fmt.Printf("Hub address: %s\n", h.AdvertiseAddress())
	if err := h.EnsureCertificate(); err == nil {
		if fingerprint := h.Fingerprint(); fingerprint != "" {
			fmt.Printf("Fingerprint: %s\n", fingerprint)
		}
	}
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SERVER KEY\tDISPLAY NAME\tSTATUS\tLAST SEEN")
	for _, entry := range entries {
		status := "enabled"
		if entry.IsDisabled {
			status = "disabled"
		}
		lastSeen := "never"
		if entry.LastSeenAt > 0 {
			lastSeen = time.Since(time.Unix(entry.LastSeenAt, 0)).Round(time.Second).String() + " ago"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", entry.ServerKey, entry.ShortName, status, lastSeen)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Live connection state is in the hub's log while it runs.")
	return nil
}
