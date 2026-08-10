package main

import (
	"fmt"
	"strings"

	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/setup"
	"github.com/Zero-Hex/modern-eq-chat/webui"
)

// runWebCommand implements `modern-eq-chat-hub web ...`.
func runWebCommand(args []string) error {
	if len(args) == 0 {
		return usageWeb()
	}

	switch args[0] {
	case "password":
		return webSetPassword()
	case "enable":
		return webSetEnabled(true)
	case "disable":
		return webSetEnabled(false)
	case "status":
		return webStatus()
	default:
		return usageWeb()
	}
}

// webSetPassword sets or replaces the admin password.
//
// The password is hashed here and only the hash is written, so modern-eq-chat.conf
// never holds the secret even though it holds everything else.
func webSetPassword() error {
	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	p := setup.NewPrompter()

	p.Printf("\nSet the web interface admin password.\n\n")
	p.Note("This interface can change the telnet command patterns used on every")
	p.Note("connected game server, so treat the password as a server credential.")
	p.Printf("\n")

	password, err := p.Secret("New password")
	if err != nil {
		return err
	}
	password = strings.TrimSpace(password)

	if len(password) < 10 {
		return fmt.Errorf("use at least 10 characters")
	}

	confirm, err := p.Secret("Repeat it")
	if err != nil {
		return err
	}
	if strings.TrimSpace(confirm) != password {
		return fmt.Errorf("the two entries did not match")
	}

	hash, err := webui.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	cfg.Relay.Hub.Web.PasswordHash = hash
	if cfg.Relay.Hub.Web.Listen == "" {
		cfg.Relay.Hub.Web.Listen = "127.0.0.1:34198"
	}

	wasEnabled := cfg.Relay.Hub.Web.IsEnabled
	if !wasEnabled {
		enable, err := p.Confirm("Enable the web interface now?", true)
		if err != nil {
			return err
		}
		cfg.Relay.Hub.Web.IsEnabled = enable
	}

	if err := config.Save(cfg, config.DefaultPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	p.Printf("\n")
	p.Note("Password saved (only its hash is stored).")
	if cfg.Relay.Hub.Web.IsEnabled {
		p.Note("The interface will be at http://%s once the hub restarts.", cfg.Relay.Hub.Web.Listen)
		p.Printf("\n")
		p.Note("It listens on this machine only. To reach it from your desktop:")
		p.Printf("\n    ssh -L 34198:127.0.0.1:34198 you@this-host\n\n")
		p.Note("then open http://127.0.0.1:34198 in a browser.")
	}
	p.Printf("\n")
	return nil
}

func webSetEnabled(isEnabled bool) error {
	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if isEnabled && cfg.Relay.Hub.Web.PasswordHash == "" {
		return fmt.Errorf("set a password first: modern-eq-chat-hub web password")
	}

	cfg.Relay.Hub.Web.IsEnabled = isEnabled
	if err := config.Save(cfg, config.DefaultPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	state := "disabled"
	if isEnabled {
		state = "enabled"
	}
	fmt.Printf("Web interface %s. Restart the hub to apply.\n", state)
	return nil
}

func webStatus() error {
	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	web := cfg.Relay.Hub.Web
	if !web.IsEnabled {
		fmt.Println("Web interface is disabled.")
		fmt.Println("Enable it with: modern-eq-chat-hub web password")
		return nil
	}

	fmt.Printf("Web interface is enabled on http://%s\n", web.Listen)
	if web.PasswordHash == "" {
		fmt.Println("WARNING: no password is set, so the hub will refuse to serve it.")
		fmt.Println("Set one with: modern-eq-chat-hub web password")
	}
	return nil
}

func usageWeb() error {
	fmt.Println("usage: modern-eq-chat-hub web <command>")
	fmt.Println()
	fmt.Println("The management interface binds to this machine only. Reach it from")
	fmt.Println("elsewhere with an SSH tunnel rather than exposing the port.")
	fmt.Println()
	fmt.Println("  password   set the admin password (and enable the interface)")
	fmt.Println("  enable     turn it on")
	fmt.Println("  disable    turn it off")
	fmt.Println("  status     show whether it is on and where it listens")
	return nil
}
