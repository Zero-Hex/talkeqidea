package request

import (
	"context"
)

// DiscordSend Request
type DiscordSend struct {
	Ctx       context.Context
	ChannelID string
	Message   string
}

// DiscordEdit Request
type DiscordEdit struct {
	Ctx       context.Context
	ChannelID string
	Message   string
}

// APICommand Request
type APICommand struct {
	Ctx                  context.Context
	FromDiscordName      string
	FromDiscordChannelID string
	FromDiscordNameID    string
	FromDiscordIGN       string
	Message              string
}

// EQLog originated from EQLog
type EQLog struct {
	Ctx                context.Context
	Action             string
	Target             int
	FromName           string
	Message            string
	ToDiscordChannelID string
	ToName             string
}

// TelnetSend request
type TelnetSend struct {
	Ctx     context.Context
	Message string
}

// RelayPublish reports a locally observed message to the cross-server relay.
//
// It carries the sender and the channel but not the origin server: that is
// stamped by the agent from its own identity, and re-stamped by the hub from
// whichever token authenticated, so it cannot be spoofed along the way.
type RelayPublish struct {
	Ctx context.Context
	// Source is which local endpoint observed the message, e.g. "telnet" or
	// "discord". The hub uses it to tell its own game server's chat apart from
	// chat typed in Discord.
	Source  string
	Channel string
	Name    string
	Message string
}

// Relay publish sources.
const (
	RelaySourceTelnet  = "telnet"
	RelaySourceDiscord = "discord"
)

// PEQEditorSQL originated from PEQ Editor
type PEQEditorSQL struct {
	Ctx            context.Context
	Action         string
	Target         int
	FromName       string
	Message        string
	ChannelKeyword string
	ToName         string
}
