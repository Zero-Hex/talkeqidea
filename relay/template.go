package relay

import (
	"bytes"
	"fmt"
	"text/template"
)

// TemplateData is what every message_pattern in the config renders against.
//
// It is a single shared struct rather than one per direction so an operator
// can move a pattern between the hub and an agent without discovering that
// half the fields silently render empty.
type TemplateData struct {
	// Name is who sent the message.
	Name string
	// Message is the body.
	Message string
	// Origin is the routing key of the server it came from, e.g. "server1".
	Origin string
	// OriginName is the display name for that server, e.g. "Vanilla".
	OriginName string
	// Channel is the logical channel, e.g. "ooc".
	Channel string
	// ChannelID is the destination-specific channel identifier: a Discord
	// channel ID, or an EQEMU channel number such as 260 for OOC.
	ChannelID string
}

// TemplateDataFor builds render data from an event.
func TemplateDataFor(e *Event, channelID string) TemplateData {
	originName := e.OriginName
	if originName == "" {
		originName = e.Origin
	}
	return TemplateData{
		Name:       e.Name,
		Message:    e.Message,
		Origin:     e.Origin,
		OriginName: originName,
		Channel:    e.Channel,
		ChannelID:  channelID,
	}
}

// Render executes a pattern template against an event.
func Render(tmpl *template.Template, e *Event, channelID string) (string, error) {
	if tmpl == nil {
		return "", fmt.Errorf("no template")
	}
	buf := &bytes.Buffer{}
	if err := tmpl.Execute(buf, TemplateDataFor(e, channelID)); err != nil {
		return "", fmt.Errorf("execute: %w", err)
	}
	return buf.String(), nil
}
