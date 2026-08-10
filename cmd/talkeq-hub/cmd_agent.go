package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/xackery/talkeq/hub"
)

// runAgentCommand implements the `talkeq-hub agent ...` subcommands used to manage
// which servers may join the relay.
//
// These run against the roster file directly rather than talking to a running
// hub. Adding a server is a rare, deliberate act performed by whoever has
// shell access on the hub box, and keeping it offline means there is no
// remote administrative surface to secure.
func runAgentCommand(args []string) error {
	h, err := openHub()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return usageAgent()
	}

	switch args[0] {
	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: talkeq-hub agent add <server-key> [short-name]")
		}
		shortName := ""
		if len(args) > 2 {
			shortName = args[2]
		}
		return agentAdd(h, args[1], shortName)

	case "list":
		return agentList(h)

	case "rotate":
		if len(args) < 2 {
			return fmt.Errorf("usage: talkeq-hub agent rotate <server-key>")
		}
		return agentRotate(h, args[1])

	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: talkeq-hub agent remove <server-key>")
		}
		if err := h.Roster().Remove(args[1]); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", args[1])
		return nil

	case "disable":
		if len(args) < 2 {
			return fmt.Errorf("usage: talkeq-hub agent disable <server-key>")
		}
		if err := h.Roster().SetEnabled(args[1], false); err != nil {
			return err
		}
		fmt.Printf("disabled %s\n", args[1])
		return nil

	case "enable":
		if len(args) < 2 {
			return fmt.Errorf("usage: talkeq-hub agent enable <server-key>")
		}
		if err := h.Roster().SetEnabled(args[1], true); err != nil {
			return err
		}
		fmt.Printf("enabled %s\n", args[1])
		return nil

	default:
		return usageAgent()
	}
}

func agentAdd(h *hub.Hub, serverKey, shortName string) error {
	if err := h.EnsureCertificate(); err != nil {
		return fmt.Errorf("certificate: %w", err)
	}

	token, err := h.Roster().Add(serverKey, shortName)
	if err != nil {
		return err
	}

	code, err := h.JoinCode(serverKey, token)
	if err != nil {
		return fmt.Errorf("join code: %w", err)
	}

	fmt.Printf("\nAdded agent %s.\n\n", serverKey)
	fmt.Printf("On that server, set this in talkeq.conf:\n\n")
	fmt.Printf("  [relay]\n")
	fmt.Printf("  mode = \"agent\"\n\n")
	fmt.Printf("  [relay.agent]\n")
	fmt.Printf("  join_code = \"%s\"\n", code)
	fmt.Printf("  short_name = \"%s\"\n\n", displayName(shortName, serverKey))
	// A bare ":9443" is what the hub binds to, not somewhere another box can
	// dial. Catching it here saves an operator a confusing connection refused.
	if strings.HasPrefix(h.Addr(), ":") || strings.HasPrefix(h.Addr(), "0.0.0.0:") {
		fmt.Printf("NOTE: the join code points at %q, which agents on other boxes cannot dial.\n", h.Addr())
		fmt.Printf("      Set advertise_address in [relay.hub] to this hub's reachable host:port,\n")
		fmt.Printf("      then re-run 'talkeq-hub agent rotate %s' for a corrected code.\n\n", serverKey)
	}

	fmt.Printf("The join code carries the hub address, this agent's token and the\n")
	fmt.Printf("certificate fingerprint. It is shown once and cannot be recovered -\n")
	fmt.Printf("run 'talkeq-hub agent rotate %s' to issue a new one.\n\n", serverKey)
	return nil
}

func agentRotate(h *hub.Hub, serverKey string) error {
	if err := h.EnsureCertificate(); err != nil {
		return fmt.Errorf("certificate: %w", err)
	}

	token, err := h.Roster().Rotate(serverKey)
	if err != nil {
		return err
	}
	code, err := h.JoinCode(serverKey, token)
	if err != nil {
		return fmt.Errorf("join code: %w", err)
	}

	fmt.Printf("\nRotated %s. The previous token no longer works.\n\n", serverKey)
	fmt.Printf("  join_code = \"%s\"\n\n", code)
	return nil
}

func agentList(h *hub.Hub) error {
	entries := h.Roster().Entries()
	if len(entries) == 0 {
		fmt.Println("no agents registered. add one with: talkeq-hub agent add <server-key> [short-name]")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SERVER KEY\tSHORT NAME\tSTATUS\tLAST SEEN")
	for _, entry := range entries {
		status := "enabled"
		if entry.IsDisabled {
			status = "disabled"
		}
		lastSeen := "never"
		if entry.LastSeenAt > 0 {
			lastSeen = time.Unix(entry.LastSeenAt, 0).Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", entry.ServerKey, entry.ShortName, status, lastSeen)
	}
	return w.Flush()
}

func usageAgent() error {
	fmt.Println("usage: talkeq-hub agent <command>")
	fmt.Println()
	fmt.Println("  add <server-key> [short-name]   authorize a server and print its join code")
	fmt.Println("  list                            show authorized servers")
	fmt.Println("  rotate <server-key>             issue a new token, invalidating the old one")
	fmt.Println("  remove <server-key>             revoke a server")
	fmt.Println("  disable <server-key>            temporarily block a server")
	fmt.Println("  enable <server-key>             restore a disabled server")
	return nil
}

func displayName(shortName, serverKey string) string {
	if shortName != "" {
		return shortName
	}
	return serverKey
}
