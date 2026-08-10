package config

import (
	"fmt"
	"text/template"
)

// Route is how to route telnet messages
type Route struct {
	IsEnabled              bool    `toml:"enabled" desc:"Is route enabled?"`
	Trigger                Trigger `toml:"trigger" desc:"condition to trigger route"`
	Target                 string  `toml:"target" desc:"target service: discord, telnet, or relay (send to the hub for cross-server chat)"`
	ChannelID              string  `toml:"channel_id" desc:"Destination channel ID"`
	Channel                string  `toml:"channel,omitempty" desc:"Logical relay channel name when target = \"relay\", e.g. ooc. Defaults to channel_id"`
	GuildID                string  `toml:"guild_id,omitempty" desc:"Optional, Destination guild ID"`
	MessagePattern         string  `toml:"message_pattern" desc:"Destination message in. E.g. {{.Name}} says {{.ChannelName}}, '{{.Message}}"`
	messagePatternTemplate *template.Template
}

// MessagePatternTemplate returns a template for provided route
func (r *Route) MessagePatternTemplate() *template.Template {
	if r.messagePatternTemplate == nil {
		// fallback logic
		r.messagePatternTemplate, _ = template.New("root").Parse(r.MessagePattern)
	}
	return r.messagePatternTemplate
}

// RelayChannel returns the logical channel a relay-targeted route publishes
// to, falling back to ChannelID so the field can be omitted in simple configs.
func (r *Route) RelayChannel() string {
	if r.Channel != "" {
		return r.Channel
	}
	return r.ChannelID
}

// LoadMessagePattern is called after config is loaded, and verified patterns are valid
func (r *Route) LoadMessagePattern() error {
	if !r.IsEnabled {
		return nil
	}
	var err error
	r.messagePatternTemplate, err = template.New("root").Parse(r.MessagePattern)
	if err != nil {
		return fmt.Errorf("failed to parse: %w", err)
	}
	return nil
}
