package setup

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/Zero-Hex/modern-eq-chat/agent"
	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/relay"
)

// RunAgent walks an operator through connecting this game server to a hub.
//
// The agent enrolls during setup: it exchanges the short code the hub operator
// gave them for a durable token, which is written to the config. That way the
// long credential is never typed by a human and never appears on screen.
func RunAgent(ctx context.Context, p *Prompter, path string) error {
	if path == "" {
		path = config.DefaultPath
	}

	p.Printf("\nModern EQ Chat agent setup\n")
	p.Printf("==================\n\n")
	p.Note("This connects your EQEMU server to a Modern EQ Chat hub.")
	p.Note("The connection is outbound, so you do not need to open any ports")
	p.Note("or have a fixed IP address.")
	p.Printf("\n")

	isExisting := config.Exists(path)
	cfg, err := config.LoadOrDefault(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Relay.Mode = config.ModeAgent

	// An agent runs none of the hub-side services. Discord, the registration
	// API and SQL reporting all depend on the bot, which lives on the hub.
	// Leaving them on would have every agent try to bind the API port and, if
	// hub and agent share a box, collide with each other.
	cfg.Discord.IsEnabled = false
	cfg.API.IsEnabled = false
	cfg.SQLReport.IsEnabled = false

	result, err := agentEnrollStep(ctx, p, cfg)
	if err != nil {
		return err
	}

	if err := telnetStep(p, cfg); err != nil {
		return err
	}
	if err := agentChannelStep(p, cfg); err != nil {
		return err
	}

	cfg.Relay.Agent.ServerKey = result.ServerKey
	cfg.Relay.Agent.ShortName = result.ShortName
	cfg.Relay.Agent.Token = result.Token
	cfg.Relay.Agent.Fingerprint = result.Fingerprint
	// The token and fingerprint are now set directly, so a stale join code
	// from a previous setup must not override them.
	cfg.Relay.Agent.JoinCode = ""

	if isExisting {
		backup, err := config.Backup(path)
		if err != nil {
			return fmt.Errorf("back up config: %w", err)
		}
		if backup != "" {
			p.Printf("\n")
			p.Note("Saved your previous config to %s", backup)
		}
	}

	if err := cfg.Verify(); err != nil {
		return fmt.Errorf("the answers do not make a valid config: %w", err)
	}
	if err := config.Save(cfg, path); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	p.Section("Done")
	p.Note("Enrolled as %s (%s).", result.ServerKey, result.ShortName)
	p.Note("Wrote %s", path)
	p.Printf("\n")
	p.Note("Start the agent with: modern-eq-chat-agent")
	return nil
}

func agentEnrollStep(ctx context.Context, p *Prompter, cfg *config.Config) (*agent.EnrollResult, error) {
	p.Section("Connect to the hub")
	p.Note("Your hub operator runs 'modern-eq-chat-hub enroll' and gives you two things.")
	p.Printf("\n")

	for {
		address, err := p.String("Hub address (host:port)", cfg.Relay.Agent.HubAddress)
		if err != nil {
			return nil, err
		}
		address = normalizeHubAddress(strings.TrimSpace(address))

		code, err := p.String("Enrollment code", "")
		if err != nil {
			return nil, err
		}
		if err := relay.ValidateEnrollCode(code); err != nil {
			p.Note("%s", err)
			continue
		}

		plaintext, err := p.Confirm("Is the hub running without encryption? (almost always no)", cfg.Relay.Agent.IsPlaintext)
		if err != nil {
			return nil, err
		}

		p.Printf("\n")
		p.Note("Contacting %s ...", address)

		result, err := agent.Enroll(ctx, agent.EnrollOptions{
			HubAddress:         address,
			Code:               code,
			IsPlaintext:        plaintext,
			ConfirmFingerprint: confirmFingerprint(p),
		})
		if err == nil {
			cfg.Relay.Agent.HubAddress = address
			cfg.Relay.Agent.IsPlaintext = plaintext
			return result, nil
		}

		p.Printf("\n")
		p.Note("Enrollment failed: %s", err)
		p.Printf("\n")
		p.Note("Common causes: the hub is not running, the address or port is")
		p.Note("wrong, a firewall is blocking the connection, or the code has")
		p.Note("already been used or expired.")
		p.Printf("\n")

		retry, err := p.Confirm("Try again?", true)
		if err != nil {
			return nil, err
		}
		if !retry {
			return nil, ErrAborted
		}
	}
}

// confirmFingerprint shows the certificate the hub presented and asks the
// operator to compare it with what the hub printed.
//
// This is the one moment where trust is established rather than verified, so
// it is a deliberate human check rather than a silent accept. Every later
// connection is pinned to whatever is confirmed here.
func confirmFingerprint(p *Prompter) func(string) (bool, error) {
	return func(fingerprint string) (bool, error) {
		p.Printf("\n")
		p.Note("The hub presented this certificate fingerprint:")
		p.Printf("\n    %s\n\n", fingerprint)
		p.Note("It should match exactly what the hub printed during its setup.")
		p.Note("If it does not, someone may be intercepting the connection.")
		p.Printf("\n")

		return p.Confirm("Does it match?", false)
	}
}

func agentChannelStep(p *Prompter, cfg *config.Config) error {
	p.Section("Relayed chat")
	p.Note("How should chat from other servers appear in your game?")
	p.Printf("\n")

	fallback := "emote world 260 {{.Name}} says from {{.OriginName}}, '{{.Message}}'"
	if ch, ok := cfg.Relay.Agent.Channel("ooc"); ok && ch.InboundPattern != "" {
		fallback = ch.InboundPattern
	}

	p.Note("The default produces:")
	p.Note("  Soandso says from Vanilla, 'hello'")
	p.Printf("\n")

	useDefault, err := p.Confirm("Use the default wording?", true)
	if err != nil {
		return err
	}

	pattern := fallback
	if !useDefault {
		p.Printf("\n")
		p.Note("Variables: {{.Name}} {{.Message}} {{.OriginName}} {{.Origin}}")
		p.Note("260 is the OOC channel number; see the EQEMU chat channel docs.")
		p.Printf("\n")

		pattern, err = p.String("Telnet command", fallback)
		if err != nil {
			return err
		}
	}

	setAgentChannel(cfg, config.AgentChannel{
		Name:           "ooc",
		IsEnabled:      true,
		InboundPattern: pattern,
	})
	return nil
}

func setAgentChannel(cfg *config.Config, channel config.AgentChannel) {
	for i := range cfg.Relay.Agent.Channels {
		if cfg.Relay.Agent.Channels[i].Name == channel.Name {
			cfg.Relay.Agent.Channels[i] = channel
			return
		}
	}
	cfg.Relay.Agent.Channels = append(cfg.Relay.Agent.Channels, channel)
}

// normalizeHubAddress accepts what an operator is likely to paste — a bare
// host, a host:port, or a URL — and returns host:port.
func normalizeHubAddress(address string) string {
	if address == "" {
		return ""
	}

	// Strip a scheme if one was pasted.
	if idx := strings.Index(address, "://"); idx >= 0 {
		address = address[idx+3:]
	}
	address = strings.TrimSuffix(address, "/")
	if idx := strings.Index(address, "/"); idx >= 0 {
		address = address[:idx]
	}

	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	// No port supplied; assume the default.
	return net.JoinHostPort(address, strconv.Itoa(34197))
}
