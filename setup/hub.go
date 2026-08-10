package setup

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/hub"
	"github.com/xackery/talkeq/sanitize"
)

// RunHub walks an operator through configuring the hub.
//
// It rewrites talkeq.conf in place, keeping any settings it does not ask
// about, so re-running it to change one answer does not discard the rest of an
// operator's tuning.
func RunHub(ctx context.Context, p *Prompter, path string) error {
	if path == "" {
		path = config.DefaultPath
	}

	p.Printf("\nTalkEQ hub setup\n")
	p.Printf("================\n\n")
	p.Note("This box will run the Discord bot and route chat between servers.")
	p.Note("Game servers connect to it; you do not need to open any ports on them.")
	p.Printf("\n")

	isExisting := config.Exists(path)
	if isExisting {
		p.Note("Found an existing %s. Current values are shown in brackets;", path)
		p.Note("press enter to keep one.")
		p.Printf("\n")
	}

	cfg, err := config.LoadOrDefault(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Relay.Mode = config.ModeHub

	if err := hubDiscordStep(p, cfg); err != nil {
		return err
	}
	if err := hubNetworkStep(p, cfg); err != nil {
		return err
	}
	if err := hubChannelStep(p, cfg); err != nil {
		return err
	}
	if err := hubLocalServerStep(p, cfg); err != nil {
		return err
	}

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

	// Verify before writing so a bad answer is reported while the operator is
	// still here to fix it, rather than at the next startup.
	if err := cfg.Verify(); err != nil {
		return fmt.Errorf("the answers do not make a valid config: %w", err)
	}
	if err := config.Save(cfg, path); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	p.Note("Wrote %s", path)

	return hubFinishStep(ctx, p, cfg)
}

func hubDiscordStep(p *Prompter, cfg *config.Config) error {
	p.Section("Discord")
	p.Note("From https://discord.com/developers/ - your application's Bot page.")
	p.Printf("\n")

	cfg.Discord.IsEnabled = true

	token, err := p.Secret("Bot token")
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) != "" {
		cfg.Discord.Token = strings.TrimSpace(token)
	}

	clientID, err := p.String("Application (client) ID", cfg.Discord.ClientID)
	if err != nil {
		return err
	}
	cfg.Discord.ClientID = strings.TrimSpace(clientID)

	serverID, err := p.String("Discord server ID", cfg.Discord.ServerID)
	if err != nil {
		return err
	}
	cfg.Discord.ServerID = strings.TrimSpace(serverID)
	return nil
}

func hubNetworkStep(p *Prompter, cfg *config.Config) error {
	p.Section("Network")

	defaultPort := 34197
	if _, portStr, err := net.SplitHostPort(cfg.Relay.Hub.Listen); err == nil {
		if parsed, err := strconv.Atoi(portStr); err == nil && parsed > 0 {
			defaultPort = parsed
		}
	}

	p.Note("Agents connect inbound to this port. It is the only port that needs")
	p.Note("to be open anywhere in the system.")
	p.Printf("\n")

	port, err := p.Int("Port to listen on", defaultPort, 1, 65535)
	if err != nil {
		return err
	}
	if port < 1024 {
		p.Note("Ports below 1024 need elevated privileges on Linux and macOS.")
	}
	cfg.Relay.Hub.Listen = fmt.Sprintf(":%d", port)

	p.Printf("\n")
	p.Note("Agents need an address they can reach this box at. Use the public")
	p.Note("hostname or IP if servers are elsewhere, or the LAN address if they")
	p.Note("are on the same network.")
	p.Printf("\n")

	fallbackHost := guessLocalAddress()
	if host, _, err := net.SplitHostPort(cfg.Relay.Hub.AdvertiseAddr); err == nil && host != "" {
		fallbackHost = host
	}

	host, err := p.String("Hostname or IP agents should dial", fallbackHost)
	if err != nil {
		return err
	}
	cfg.Relay.Hub.AdvertiseAddr = net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))

	p.Printf("\n")
	choice, err := p.Choose("How should the connection be secured?", []string{
		"Self-signed certificate, agents pin its fingerprint (recommended)",
		"My own certificate files",
		"No encryption - only if the hub is reachable solely over a private network",
	}, tlsChoiceFor(cfg.Relay.Hub.TLSMode))
	if err != nil {
		return err
	}

	switch choice {
	case 0:
		cfg.Relay.Hub.TLSMode = config.TLSSelfSigned
	case 1:
		cfg.Relay.Hub.TLSMode = config.TLSFile
		certPath, err := p.String("Certificate path", cfg.Relay.Hub.TLSCertPath)
		if err != nil {
			return err
		}
		keyPath, err := p.String("Private key path", cfg.Relay.Hub.TLSKeyPath)
		if err != nil {
			return err
		}
		cfg.Relay.Hub.TLSCertPath = certPath
		cfg.Relay.Hub.TLSKeyPath = keyPath
	case 2:
		p.Printf("\n")
		p.Note("Without encryption, agent tokens and all relayed chat travel in")
		p.Note("the clear and anyone on the path can read or alter them.")
		confirmed, err := p.Confirm("Are you sure?", false)
		if err != nil {
			return err
		}
		if !confirmed {
			cfg.Relay.Hub.TLSMode = config.TLSSelfSigned
			p.Note("Keeping the self-signed certificate.")
		} else {
			cfg.Relay.Hub.TLSMode = config.TLSNone
		}
	}
	return nil
}

func hubChannelStep(p *Prompter, cfg *config.Config) error {
	p.Section("Chat channels")
	p.Note("Which Discord channel should OOC from every server appear in?")
	p.Note("Right click the channel in Discord and Copy Channel ID.")
	p.Printf("\n")

	existing := ""
	if ch, ok := cfg.Relay.Hub.Channel("ooc"); ok {
		existing = ch.DiscordChannelID
	}
	if existing == "INSERTOOCCHANNELHERE" {
		existing = ""
	}

	channelID, err := p.String("OOC channel ID", existing)
	if err != nil {
		return err
	}
	channelID = strings.TrimSpace(channelID)

	crossServer, err := p.Confirm("Relay OOC between game servers as well as to Discord?", true)
	if err != nil {
		return err
	}

	allowTalkBack, err := p.Confirm("Let Discord users in that channel talk into the game?", true)
	if err != nil {
		return err
	}

	setHubChannel(cfg, config.HubChannel{
		Name:             "ooc",
		IsEnabled:        true,
		IsCrossServer:    crossServer,
		DiscordChannelID: channelID,
		DiscordPattern:   "**[{{.OriginName}}]** {{.Name}} **OOC**: {{.Message}}",
	})

	setDiscordRelayRoute(cfg, "ooc", channelID, allowTalkBack)
	return nil
}

func hubLocalServerStep(p *Prompter, cfg *config.Config) error {
	p.Section("Game server on this box")

	hasLocal, err := p.Confirm("Does this box also run an EQEMU server that should join the relay?", cfg.Relay.Hub.LocalServerKey != "")
	if err != nil {
		return err
	}
	if !hasLocal {
		cfg.Relay.Hub.LocalServerKey = ""
		cfg.Relay.Hub.LocalShortName = ""
		cfg.Telnet.IsEnabled = false
		return nil
	}

	key, err := p.String("Short routing key for it (lowercase, no spaces)", defaultString(cfg.Relay.Hub.LocalServerKey, "server1"))
	if err != nil {
		return err
	}
	cfg.Relay.Hub.LocalServerKey = sanitize.ServerKey(key)

	display, err := p.String("Display name shown in relayed chat", defaultString(cfg.Relay.Hub.LocalShortName, cfg.Relay.Hub.LocalServerKey))
	if err != nil {
		return err
	}
	cfg.Relay.Hub.LocalShortName = strings.TrimSpace(display)

	return telnetStep(p, cfg)
}

func hubFinishStep(ctx context.Context, p *Prompter, cfg *config.Config) error {
	h, err := hub.New(ctx, cfg.Relay.Hub)
	if err != nil {
		return fmt.Errorf("prepare hub: %w", err)
	}
	if err := h.EnsureCertificate(); err != nil {
		return fmt.Errorf("prepare certificate: %w", err)
	}

	p.Section("Done")

	fingerprint := h.Fingerprint()
	if fingerprint != "" {
		p.Note("Certificate fingerprint:")
		p.Printf("\n    %s\n\n", fingerprint)
		p.Note("Each agent shows this during its own setup and asks you to confirm")
		p.Note("it matches. That check is what stops anyone impersonating this hub.")
		p.Printf("\n")
	}

	addAgent, err := p.Confirm("Add your first game server now?", true)
	if err != nil {
		return err
	}
	if !addAgent {
		p.Printf("\n")
		p.Note("Start the hub with: talkeq-hub")
		p.Note("Add servers later with: talkeq-hub enroll <server-key> [display name]")
		return nil
	}

	for {
		if err := hubEnrollOne(p, h); err != nil {
			return err
		}
		more, err := p.Confirm("Add another server?", false)
		if err != nil {
			return err
		}
		if !more {
			break
		}
	}

	p.Printf("\n")
	p.Note("Start the hub with: talkeq-hub")
	return nil
}

func hubEnrollOne(p *Prompter, h *hub.Hub) error {
	p.Printf("\n")

	key, err := p.String("Routing key for the server (lowercase, no spaces)", "")
	if err != nil {
		return err
	}
	display, err := p.String("Display name shown in relayed chat", sanitize.ServerKey(key))
	if err != nil {
		return err
	}

	code, err := h.Enroll().Create(key, display, hub.DefaultEnrollTTL)
	if err != nil {
		return fmt.Errorf("create enrollment code: %w", err)
	}

	p.Printf("\n")
	p.Note("Run 'talkeq-agent' on %s and give it:", display)
	p.Printf("\n")
	p.Printf("    Hub address:      %s\n", h.AdvertiseAddress())
	p.Printf("    Enrollment code:  %s\n", code)
	p.Printf("\n")
	p.Note("The code works once and expires in %s.", hub.DefaultEnrollTTL)
	p.Note("The hub must be running for the agent to enroll.")
	return nil
}

// telnetStep collects local EQEMU telnet settings, shared by both wizards.
func telnetStep(p *Prompter, cfg *config.Config) error {
	p.Section("Local EQEMU server")
	p.Note("TalkEQ reads chat from your server's telnet console.")
	p.Printf("\n")

	cfg.Telnet.IsEnabled = true

	host, err := p.String("Telnet address", defaultString(cfg.Telnet.Host, "127.0.0.1:9000"))
	if err != nil {
		return err
	}
	cfg.Telnet.Host = strings.TrimSpace(host)

	p.Printf("\n")
	p.Note("Newer EQEMU builds accept local connections without credentials.")
	p.Note("Leave these blank unless your server asks for a login.")
	p.Printf("\n")

	username, err := p.Optional("Telnet username", cfg.Telnet.Username)
	if err != nil {
		return err
	}
	cfg.Telnet.Username = strings.TrimSpace(username)

	if cfg.Telnet.Username != "" {
		password, err := p.Secret("Telnet password")
		if err != nil {
			return err
		}
		cfg.Telnet.Password = strings.TrimSpace(password)
	}

	setTelnetRelayRoute(cfg, "ooc", `(\w+) says ooc, '(.*)'`)
	return nil
}

// setHubChannel adds or replaces a channel by name.
func setHubChannel(cfg *config.Config, channel config.HubChannel) {
	for i := range cfg.Relay.Hub.Channels {
		if cfg.Relay.Hub.Channels[i].Name == channel.Name {
			cfg.Relay.Hub.Channels[i] = channel
			return
		}
	}
	cfg.Relay.Hub.Channels = append(cfg.Relay.Hub.Channels, channel)
}

// setTelnetRelayRoute makes local chat matching a pattern feed the relay,
// replacing any existing relay route for the same channel.
func setTelnetRelayRoute(cfg *config.Config, channel, pattern string) {
	route := config.Route{
		IsEnabled: true,
		Trigger: config.Trigger{
			Regex:        pattern,
			NameIndex:    1,
			MessageIndex: 2,
		},
		Target:    "relay",
		ChannelID: channel,
		Channel:   channel,
	}

	for i := range cfg.Telnet.Routes {
		if cfg.Telnet.Routes[i].Target == "relay" && cfg.Telnet.Routes[i].RelayChannel() == channel {
			cfg.Telnet.Routes[i] = route
			return
		}
	}
	cfg.Telnet.Routes = append(cfg.Telnet.Routes, route)
}

// setDiscordRelayRoute lets a Discord channel feed the relay.
func setDiscordRelayRoute(cfg *config.Config, channel, discordChannelID string, isEnabled bool) {
	route := config.DiscordRoute{
		IsEnabled: isEnabled,
		Trigger: config.DiscordTrigger{
			ChannelID: discordChannelID,
		},
		Target:    "relay",
		ChannelID: channel,
		Channel:   channel,
	}

	for i := range cfg.Discord.Routes {
		if cfg.Discord.Routes[i].Target == "relay" && cfg.Discord.Routes[i].ChannelID == channel {
			cfg.Discord.Routes[i] = route
			return
		}
	}
	cfg.Discord.Routes = append(cfg.Discord.Routes, route)
}

func tlsChoiceFor(mode string) int {
	switch mode {
	case config.TLSFile:
		return 1
	case config.TLSNone:
		return 2
	default:
		return 0
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

// guessLocalAddress finds this box's outward-facing address on the local
// network, as a starting suggestion the operator can correct.
//
// The UDP "connection" sends nothing; it only asks the routing table which
// interface would be used to reach the internet.
func guessLocalAddress() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:53", 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return addr.IP.String()
}
