package config

import (
	"fmt"
	"text/template"
	"time"

	"github.com/xackery/talkeq/relay"
	"github.com/xackery/talkeq/sanitize"
)

// Relay modes.
const (
	// ModeStandalone is the original single-server behavior: telnet on this
	// box, Discord from this box, no cross-server traffic. It is the default
	// so an existing talkeq.conf keeps working untouched.
	ModeStandalone = "standalone"
	// ModeHub runs the central router. Discord lives here.
	ModeHub = "hub"
	// ModeAgent watches one game server and reports to a hub.
	ModeAgent = "agent"
)

// TLS modes for the hub listener.
const (
	// TLSSelfSigned generates a certificate on first run and expects agents to
	// pin its fingerprint. Right choice when the hub has no DNS name.
	TLSSelfSigned = "self-signed"
	// TLSLetsEncrypt obtains a certificate automatically. Requires a public
	// DNS name resolving to the hub and inbound port 443.
	TLSLetsEncrypt = "letsencrypt"
	// TLSFile uses an operator-supplied certificate and key.
	TLSFile = "file"
	// TLSNone serves plaintext. Only safe when the hub is reachable solely
	// over a private network such as WireGuard.
	TLSNone = "none"
)

// Relay configures cross-server chat.
type Relay struct {
	Mode  string    `toml:"mode" desc:"How this instance participates in cross-server chat.\n# standalone = original behavior, one server, no relay (default)\n# hub        = central router, holds the Discord bot and every agent's token\n# agent      = watches one game server and reports to a hub"`
	Hub   HubConfig `toml:"hub" desc:"Settings used when mode = \"hub\""`
	Agent AgentConf `toml:"agent" desc:"Settings used when mode = \"agent\""`
}

// HubConfig configures the central router.
type HubConfig struct {
	Listen         string       `toml:"listen" desc:"Address to accept agent connections on. Default :34197"`
	AgentsDatabase string       `toml:"agents_database" desc:"Where authorized agents and their hashed tokens are stored.\n# Managed with 'talkeq-hub agent add/list/rotate/remove' - not meant to be hand edited"`
	EnrollDatabase string       `toml:"enroll_database" desc:"Where outstanding enrollment codes are held until they are used or expire.\n# Managed with 'talkeq-hub enroll' - not meant to be hand edited"`
	TLSMode        string       `toml:"tls_mode" desc:"self-signed (default, agents pin the fingerprint), letsencrypt, file, or none\n# Use none ONLY if the hub is reachable exclusively over a private network"`
	TLSCertPath    string       `toml:"tls_cert" desc:"Certificate path when tls_mode = \"file\", or where the self-signed cert is cached"`
	TLSKeyPath     string       `toml:"tls_key" desc:"Key path when tls_mode = \"file\", or where the self-signed key is cached"`
	TLSDomain      string       `toml:"tls_domain" desc:"Public DNS name when tls_mode = \"letsencrypt\""`
	AdvertiseAddr  string       `toml:"advertise_address" desc:"Address agents should dial, used when generating join codes. Defaults to listen"`
	LocalServerKey string       `toml:"local_server_key" desc:"Optional. Set when the hub box also runs a game server, so its own chat joins the relay. e.g. server1"`
	LocalShortName string       `toml:"local_short_name" desc:"Display name for the hub's own game server, e.g. Vanilla"`
	HeartbeatSecs  int          `toml:"heartbeat_seconds" desc:"How often agents report health. Default 30"`
	QueueSize      int          `toml:"queue_size" desc:"Messages buffered per agent before the slowest agent starts dropping. Default 256"`
	Limits         HubLimits    `toml:"limits" desc:"Abuse controls. The hub is the only internet-facing part of a relay"`
	Web            WebConfig    `toml:"web" desc:"Local management interface"`
	Channels       []HubChannel `toml:"channels" desc:"Which chat channels are relayed, and where they go"`
}

// HubLimits are the hub's abuse controls.
type HubLimits struct {
	MaxAgents             int      `toml:"max_agents" desc:"Most agents that may be connected at once. 0 for no limit. Default 64"`
	ConnectionsPerMinute  int      `toml:"connections_per_minute" desc:"Connection attempts allowed from one address per minute. 0 disables. Default 30"`
	AuthFailuresBeforeBan int      `toml:"auth_failures_before_ban" desc:"Rejected tokens from one address before it is temporarily blocked. 0 disables. Default 5\n# Repeat offenders are blocked for twice as long each time, up to a day"`
	BanDuration           string   `toml:"ban_duration" desc:"How long the first block lasts. Default 15m"`
	MessagesPerSecond     float64  `toml:"messages_per_second" desc:"Sustained chat rate accepted from one agent. 0 disables. Default 20"`
	MessageBurst          int      `toml:"message_burst" desc:"Messages one agent may send at once before the sustained rate applies. Default 40"`
	AllowedNetworks       []string `toml:"allowed_networks" desc:"Optional. Only accept agents from these addresses or CIDRs, e.g. [\"203.0.113.4\", \"10.0.0.0/8\"]\n# Empty accepts any source. A malformed entry is a startup error, never a silent allow-all"`
}

// BanDurationValue parses the configured ban duration.
func (c *HubLimits) BanDurationValue() time.Duration {
	d, err := time.ParseDuration(c.BanDuration)
	if err != nil || d <= 0 {
		return 15 * time.Minute
	}
	return d
}

// WebConfig configures the local management interface.
type WebConfig struct {
	IsEnabled    bool   `toml:"enabled" desc:"Serve the management interface?"`
	Listen       string `toml:"listen" desc:"Address to serve on. Default 127.0.0.1:34198\n# This interface can rewrite the telnet command patterns used on every connected\n# server, so it is bound to this machine only. To reach it from elsewhere, use an\n# SSH tunnel: ssh -L 34198:127.0.0.1:34198 you@hub"`
	PasswordHash string `toml:"password_hash" desc:"argon2id hash of the admin password. Set it with 'talkeq-hub web password'\n# Never store the password itself here"`
}

// Verify checks the web interface configuration.
func (c *WebConfig) Verify() error {
	if !c.IsEnabled {
		return nil
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:34198"
	}
	if c.PasswordHash == "" {
		return fmt.Errorf("no admin password is set; run 'talkeq-hub web password' or set enabled = false")
	}
	return nil
}

// HubChannel is one logical chat channel the hub knows how to route.
type HubChannel struct {
	Name             string `toml:"name" desc:"Logical channel name agents report against, e.g. ooc"`
	IsEnabled        bool   `toml:"enabled" desc:"Is this channel relayed?"`
	IsCrossServer    bool   `toml:"cross_server" desc:"Relay this channel between game servers?"`
	DiscordChannelID string `toml:"discord_channel_id" desc:"Discord channel to mirror this into. Empty disables the Discord leg"`
	DiscordPattern   string `toml:"discord_pattern" desc:"How the message reads in Discord.\n# Variables: {{.Name}} {{.Message}} {{.Origin}} {{.OriginName}} {{.Channel}}"`
	discordTemplate  *template.Template
}

// AgentConf configures a game server reporting to a hub.
type AgentConf struct {
	ServerKey   string         `toml:"server_key" desc:"This server's routing key, e.g. server1. Lowercase, no spaces"`
	ShortName   string         `toml:"short_name" desc:"How this server is named in relayed chat, e.g. Vanilla.\n# Produces: Soandso says from Vanilla, 'hello'"`
	JoinCode    string         `toml:"join_code" desc:"The single blob printed by 'talkeq agent add' on the hub.\n# Setting this fills in hub_address, token and fingerprint automatically"`
	HubAddress  string         `toml:"hub_address" desc:"Hub host:port. Ignored when join_code is set"`
	Token       string         `toml:"token" desc:"This agent's token. Ignored when join_code is set"`
	Fingerprint string         `toml:"fingerprint" desc:"Hex SHA-256 of the hub's TLS certificate. Ignored when join_code is set.\n# Empty means the hub uses a publicly trusted certificate"`
	IsPlaintext bool           `toml:"plaintext" desc:"Connect without TLS. Only for hubs on a private network with tls_mode = none"`
	Channels    []AgentChannel `toml:"channels" desc:"How chat arriving from other servers is injected into this one"`
}

// AgentChannel says how inbound relay traffic is written into the local game
// server for one channel.
type AgentChannel struct {
	Name           string `toml:"name" desc:"Logical channel name, matching a hub channel, e.g. ooc"`
	IsEnabled      bool   `toml:"enabled" desc:"Inject messages for this channel into the local server?"`
	InboundPattern string `toml:"inbound_pattern" desc:"Telnet command used to inject a relayed message.\n# Variables: {{.Name}} {{.Message}} {{.Origin}} {{.OriginName}} {{.Channel}}\n# e.g. emote world 260 {{.Name}} says from {{.OriginName}}, '{{.Message}}'"`
	inboundTmpl    *template.Template
}

// DiscordTemplate returns the parsed discord pattern for a channel.
func (c *HubChannel) DiscordTemplate() *template.Template { return c.discordTemplate }

// InboundTemplate returns the parsed inbound pattern for a channel.
func (c *AgentChannel) InboundTemplate() *template.Template { return c.inboundTmpl }

// Verify checks relay configuration and parses templates once, so a bad
// pattern is reported at startup instead of the first time someone talks.
func (c *Relay) Verify() error {
	if c.Mode == "" {
		c.Mode = ModeStandalone
	}
	switch c.Mode {
	case ModeStandalone:
		return nil
	case ModeHub:
		return c.Hub.verify()
	case ModeAgent:
		return c.Agent.verify()
	default:
		return fmt.Errorf("unknown mode %q (want standalone, hub or agent)", c.Mode)
	}
}

func (c *HubConfig) verify() error {
	if c.Listen == "" {
		c.Listen = ":34197"
	}
	if c.AgentsDatabase == "" {
		c.AgentsDatabase = "talkeq_agents.json"
	}
	if c.EnrollDatabase == "" {
		c.EnrollDatabase = "talkeq_enroll.json"
	}
	if c.TLSMode == "" {
		c.TLSMode = TLSSelfSigned
	}
	if c.HeartbeatSecs < 5 {
		c.HeartbeatSecs = 30
	}
	if c.QueueSize < 16 {
		c.QueueSize = 256
	}
	c.Limits.applyDefaults()
	if err := c.Web.Verify(); err != nil {
		return fmt.Errorf("web: %w", err)
	}
	if c.AdvertiseAddr == "" {
		c.AdvertiseAddr = c.Listen
	}
	c.LocalServerKey = sanitize.ServerKey(c.LocalServerKey)
	if c.LocalServerKey == relay.OriginDiscord {
		return fmt.Errorf("local_server_key %q is reserved", relay.OriginDiscord)
	}
	if c.LocalServerKey != "" && c.LocalShortName == "" {
		c.LocalShortName = c.LocalServerKey
	}

	switch c.TLSMode {
	case TLSSelfSigned:
		if c.TLSCertPath == "" {
			c.TLSCertPath = "talkeq_hub_cert.pem"
		}
		if c.TLSKeyPath == "" {
			c.TLSKeyPath = "talkeq_hub_key.pem"
		}
	case TLSFile:
		if c.TLSCertPath == "" || c.TLSKeyPath == "" {
			return fmt.Errorf("tls_mode file requires tls_cert and tls_key")
		}
	case TLSLetsEncrypt:
		if c.TLSDomain == "" {
			return fmt.Errorf("tls_mode letsencrypt requires tls_domain")
		}
	case TLSNone:
	default:
		return fmt.Errorf("unknown tls_mode %q", c.TLSMode)
	}

	seen := map[string]bool{}
	for i := range c.Channels {
		ch := &c.Channels[i]
		ch.Name = sanitize.ServerKey(ch.Name)
		if ch.Name == "" {
			return fmt.Errorf("channel %d: name is empty", i)
		}
		if seen[ch.Name] {
			return fmt.Errorf("channel %d: %q is defined twice", i, ch.Name)
		}
		seen[ch.Name] = true

		if ch.DiscordPattern == "" {
			ch.DiscordPattern = "**[{{.OriginName}}]** {{.Name}}: {{.Message}}"
		}
		tmpl, err := template.New(ch.Name).Parse(ch.DiscordPattern)
		if err != nil {
			return fmt.Errorf("channel %s: discord_pattern: %w", ch.Name, err)
		}
		ch.discordTemplate = tmpl
	}
	return nil
}

func (c *AgentConf) verify() error {
	if c.JoinCode != "" {
		code, err := relay.ParseJoinCode(c.JoinCode)
		if err != nil {
			return fmt.Errorf("join_code: %w", err)
		}
		c.HubAddress = code.Address
		c.Token = code.Token
		c.Fingerprint = code.Fingerprint
		if c.ServerKey == "" {
			c.ServerKey = code.ServerKey
		}
	}

	c.ServerKey = sanitize.ServerKey(c.ServerKey)
	if c.ServerKey == "" {
		return fmt.Errorf("server_key is required")
	}
	if c.ShortName == "" {
		c.ShortName = c.ServerKey
	}
	if c.HubAddress == "" {
		return fmt.Errorf("hub_address is required (or set join_code)")
	}
	if c.Token == "" {
		return fmt.Errorf("token is required (or set join_code)")
	}
	if !c.IsPlaintext && c.Fingerprint == "" {
		// Not an error: a hub with a Let's Encrypt certificate needs no pin.
		// Normal CA verification applies in that case.
		_ = c.Fingerprint
	}

	seen := map[string]bool{}
	for i := range c.Channels {
		ch := &c.Channels[i]
		ch.Name = sanitize.ServerKey(ch.Name)
		if ch.Name == "" {
			return fmt.Errorf("channel %d: name is empty", i)
		}
		if seen[ch.Name] {
			return fmt.Errorf("channel %d: %q is defined twice", i, ch.Name)
		}
		seen[ch.Name] = true

		if !ch.IsEnabled {
			continue
		}
		if ch.InboundPattern == "" {
			return fmt.Errorf("channel %s: inbound_pattern is required when enabled", ch.Name)
		}
		tmpl, err := template.New(ch.Name).Parse(ch.InboundPattern)
		if err != nil {
			return fmt.Errorf("channel %s: inbound_pattern: %w", ch.Name, err)
		}
		ch.inboundTmpl = tmpl
	}
	return nil
}

// applyDefaults fills in unset limits.
//
// A negative value means "off" and is normalized to zero; only an unset field
// picks up the default. That distinction matters because an operator who
// deliberately disables a limit must not have it silently switched back on.
func (c *HubLimits) applyDefaults() {
	if c.MaxAgents == 0 {
		c.MaxAgents = 64
	} else if c.MaxAgents < 0 {
		c.MaxAgents = 0
	}

	if c.ConnectionsPerMinute == 0 {
		c.ConnectionsPerMinute = 30
	} else if c.ConnectionsPerMinute < 0 {
		c.ConnectionsPerMinute = 0
	}

	if c.AuthFailuresBeforeBan == 0 {
		c.AuthFailuresBeforeBan = 5
	} else if c.AuthFailuresBeforeBan < 0 {
		c.AuthFailuresBeforeBan = 0
	}

	if c.MessagesPerSecond == 0 {
		c.MessagesPerSecond = 20
	} else if c.MessagesPerSecond < 0 {
		c.MessagesPerSecond = 0
	}

	if c.MessageBurst <= 0 {
		c.MessageBurst = 40
	}

	if c.BanDuration == "" {
		c.BanDuration = "15m"
	}
}

// Channel finds a hub channel by name.
func (c *HubConfig) Channel(name string) (*HubChannel, bool) {
	for i := range c.Channels {
		if c.Channels[i].Name == name {
			return &c.Channels[i], true
		}
	}
	return nil, false
}

// Channel finds an agent channel by name.
func (c *AgentConf) Channel(name string) (*AgentChannel, bool) {
	for i := range c.Channels {
		if c.Channels[i].Name == name {
			return &c.Channels[i], true
		}
	}
	return nil, false
}
