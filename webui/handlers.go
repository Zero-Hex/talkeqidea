package webui

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/hub"
	"github.com/Zero-Hex/modern-eq-chat/sanitize"
	"github.com/Zero-Hex/modern-eq-chat/tlog"
)

// agentView is one server as the UI sees it, merging the roster's persistent
// record with whatever the live session knows.
type agentView struct {
	ServerKey   string `json:"server_key"`
	ShortName   string `json:"short_name"`
	IsConnected bool   `json:"is_connected"`
	IsDisabled  bool   `json:"is_disabled"`
	IsSourceUp  bool   `json:"is_source_up"`
	PlayerCount int    `json:"player_count"`
	Dropped     int64  `json:"dropped"`
	ConnectedAt string `json:"connected_at"`
	LastSeen    string `json:"last_seen"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	connected := s.hub.ConnectedServers()

	writeJSON(w, map[string]any{
		"hub_address":  s.hub.AdvertiseAddress(),
		"fingerprint":  s.hub.Fingerprint(),
		"agent_count":  len(s.hub.Roster().Entries()),
		"online_count": len(connected),
		"player_count": s.hub.PlayerCount(),
		"bans":         s.hub.Guard().Bans(),
	})
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	entries := s.hub.Roster().Entries()

	live := make(map[string]hub.ServerStatus, len(entries))
	for _, status := range s.hub.ConnectedServers() {
		live[status.ServerKey] = status
	}

	views := make([]agentView, 0, len(entries))
	for _, entry := range entries {
		view := agentView{
			ServerKey:  entry.ServerKey,
			ShortName:  entry.ShortName,
			IsDisabled: entry.IsDisabled,
			LastSeen:   "never",
		}
		if entry.LastSeenAt > 0 {
			view.LastSeen = time.Since(time.Unix(entry.LastSeenAt, 0)).Round(time.Second).String() + " ago"
		}

		if status, ok := live[entry.ServerKey]; ok {
			view.IsConnected = true
			view.IsSourceUp = status.IsSourceUp
			view.PlayerCount = status.PlayerCount
			view.Dropped = status.Dropped
			view.ConnectedAt = time.Since(status.ConnectedAt).Round(time.Second).String()
			view.ShortName = status.ShortName
		}

		views = append(views, view)
	}

	sort.Slice(views, func(i, j int) bool { return views[i].ServerKey < views[j].ServerKey })
	writeJSON(w, map[string]any{"agents": views})
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ServerKey string `json:"server_key"`
		ShortName string `json:"short_name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	if err := s.hub.RenameAgent(body.ServerKey, body.ShortName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleSetEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ServerKey string `json:"server_key"`
		IsEnabled bool   `json:"is_enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	if err := s.hub.Roster().SetEnabled(body.ServerKey, body.IsEnabled); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tlog.Infof("[web] %s set enabled=%v", sanitize.ServerKey(body.ServerKey), body.IsEnabled)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ServerKey string `json:"server_key"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	if err := s.hub.Roster().Remove(body.ServerKey); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tlog.Infof("[web] removed agent %s", sanitize.ServerKey(body.ServerKey))
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ServerKey string `json:"server_key"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	// A rotation replaces the agent's credentials, so it needs a way back in.
	// An enrollment code is friendlier than a raw token and expires on its own
	// if the operator changes their mind.
	code, err := s.hub.Enroll().Create(body.ServerKey, "", hub.DefaultEnrollTTL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	tlog.Infof("[web] issued a re-enrollment code for %s", sanitize.ServerKey(body.ServerKey))
	writeJSON(w, map[string]any{
		"code":        code,
		"hub_address": s.hub.AdvertiseAddress(),
		"fingerprint": s.hub.Fingerprint(),
		"note":        "Run 'modern-eq-chat-agent setup' on that server with this code. Its old token keeps working until it re-enrolls.",
	})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ServerKey string `json:"server_key"`
		WithEcho  bool   `json:"with_echo"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	// An empty key tests everything, which is the common case after changing
	// something and wanting to know the fleet is still healthy.
	if body.ServerKey == "" {
		writeJSON(w, map[string]any{"results": s.hub.TestAll(body.WithEcho, 10*time.Second)})
		return
	}

	result := s.hub.TestAgent(body.ServerKey, body.WithEcho, 10*time.Second)
	writeJSON(w, map[string]any{"results": []hub.TestResult{result}})
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"pending": s.hub.Enroll().Pending()})

	case http.MethodPost:
		var body struct {
			ServerKey string `json:"server_key"`
			ShortName string `json:"short_name"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request")
			return
		}

		code, err := s.hub.Enroll().Create(body.ServerKey, body.ShortName, hub.DefaultEnrollTTL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		tlog.Infof("[web] issued an enrollment code for %s", sanitize.ServerKey(body.ServerKey))
		writeJSON(w, map[string]any{
			"code":        code,
			"hub_address": s.hub.AdvertiseAddress(),
			"fingerprint": s.hub.Fingerprint(),
			"expires_in":  hub.DefaultEnrollTTL.String(),
		})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleEnrollRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	if err := s.hub.Enroll().Revoke(body.ID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// channelInput is the editable shape of a channel. It deliberately excludes
// anything the UI has no business changing.
type channelInput struct {
	Name             string `json:"name"`
	IsEnabled        bool   `json:"enabled"`
	IsCrossServer    bool   `json:"cross_server"`
	DiscordChannelID string `json:"discord_channel_id"`
	DiscordPattern   string `json:"discord_pattern"`
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		channels := s.hub.Channels()

		out := make([]channelInput, 0, len(channels))
		for _, ch := range channels {
			out = append(out, channelInput{
				Name:             ch.Name,
				IsEnabled:        ch.IsEnabled,
				IsCrossServer:    ch.IsCrossServer,
				DiscordChannelID: ch.DiscordChannelID,
				DiscordPattern:   ch.DiscordPattern,
			})
		}
		writeJSON(w, map[string]any{"channels": out})

	case http.MethodPost:
		var body struct {
			Channels []channelInput `json:"channels"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request")
			return
		}
		if len(body.Channels) == 0 {
			writeError(w, http.StatusBadRequest, "at least one channel is required")
			return
		}

		channels := make([]config.HubChannel, 0, len(body.Channels))
		for _, input := range body.Channels {
			channels = append(channels, config.HubChannel{
				Name:             input.Name,
				IsEnabled:        input.IsEnabled,
				IsCrossServer:    input.IsCrossServer,
				DiscordChannelID: input.DiscordChannelID,
				DiscordPattern:   input.DiscordPattern,
			})
		}

		// Apply to the running hub first. It parses the templates, so an
		// invalid pattern is rejected before it can be written to disk and
		// break the next startup.
		if err := s.hub.ReplaceChannels(channels); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := s.persistChannels(channels); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("applied to the running hub, but saving failed: %s", err))
			return
		}

		tlog.Infof("[web] channel configuration updated and saved")
		writeJSON(w, map[string]any{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// persistChannels writes the new channel list into modern-eq-chat.conf.
//
// The file is re-read rather than held in memory, so an operator who edited it
// by hand since startup does not silently lose those edits.
func (s *Server) persistChannels(channels []config.HubChannel) error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	cfg.Relay.Hub.Channels = channels
	if err := config.Save(cfg, s.configPath); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
