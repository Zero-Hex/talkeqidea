package client

import (
	"context"
	"fmt"
	"time"

	"github.com/xackery/talkeq/agent"
	"github.com/xackery/talkeq/api"
	"github.com/xackery/talkeq/config"
	"github.com/xackery/talkeq/discord"
	"github.com/xackery/talkeq/eqlog"
	"github.com/xackery/talkeq/guilddb"
	"github.com/xackery/talkeq/hub"
	"github.com/xackery/talkeq/peqeditorsql"
	"github.com/xackery/talkeq/relay"
	"github.com/xackery/talkeq/request"
	"github.com/xackery/talkeq/sqlreport"
	"github.com/xackery/talkeq/telnet"
	"github.com/xackery/talkeq/tlog"
	"github.com/xackery/talkeq/userdb"
	"github.com/xackery/talkeq/webui"
)

// Client wraps all talking endpoints
type Client struct {
	ctx          context.Context
	cancel       context.CancelFunc
	config       *config.Config
	discord      *discord.Discord
	telnet       *telnet.Telnet
	eqlog        *eqlog.EQLog
	sqlreport    *sqlreport.SQLReport
	peqeditorsql *peqeditorsql.PEQEditorSQL
	api          *api.API

	// Exactly one of hub or agent is set, depending on relay.mode. Both are
	// nil in standalone mode, where relay-targeted routes are inert.
	hub   *hub.Hub
	agent *agent.Agent
	web   *webui.Server
}

// Hub returns the relay hub, or nil when not running in hub mode.
func (c *Client) Hub() *hub.Hub { return c.hub }

// New creates a new client
func New(ctx context.Context) (*Client, error) {
	var err error
	ctx, cancel := context.WithCancel(ctx)
	c := Client{
		ctx:    ctx,
		cancel: cancel,
	}
	tlog.Debugf("[talkeq] initializing talkeq client")
	c.config, err = config.NewConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	tlog.Debugf("[talkeq] initializing databases")
	err = userdb.New(c.config)
	if err != nil {
		return nil, fmt.Errorf("userdb.New: %w", err)
	}

	err = guilddb.New(c.config)
	if err != nil {
		return nil, fmt.Errorf("guilddb.New: %w", err)
	}

	tlog.Debugf("[talkeq] initializing relay (mode: %s)", c.config.Relay.Mode)
	if err = c.newRelay(ctx); err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}

	tlog.Debugf("[talkeq] initializing 3rd party connections")
	c.discord, err = discord.New(ctx, c.config.Discord)
	if err != nil {
		return nil, fmt.Errorf("discord: %w", err)
	}

	err = c.discord.Subscribe(ctx, c.onMessage)
	if err != nil {
		return nil, fmt.Errorf("discord subscribe: %w", err)
	}

	c.telnet, err = telnet.New(ctx, c.config.Telnet)
	if err != nil {
		return nil, fmt.Errorf("telnet: %w", err)
	}

	c.sqlreport, err = sqlreport.New(ctx, c.config.SQLReport, c.discord)
	if err != nil {
		return nil, fmt.Errorf("sqlreport: %w", err)
	}

	err = c.telnet.Subscribe(ctx, c.onMessage)
	if err != nil {
		return nil, fmt.Errorf("telnet subscribe: %w", err)
	}

	c.eqlog, err = eqlog.New(ctx, c.config.EQLog)
	if err != nil {
		return nil, fmt.Errorf("eqlog: %w", err)
	}

	err = c.eqlog.Subscribe(ctx, c.onMessage)
	if err != nil {
		return nil, fmt.Errorf("eqlog subscribe: %w", err)
	}

	c.peqeditorsql, err = peqeditorsql.New(ctx, c.config.PEQEditor.SQL)
	if err != nil {
		return nil, fmt.Errorf("peqeditorsql: %w", err)
	}

	err = c.peqeditorsql.Subscribe(ctx, c.onMessage)
	if err != nil {
		return nil, fmt.Errorf("peqeditorsql subscribe: %w", err)
	}

	tlog.Debugf("[talkeq] initializing API")
	c.api, err = api.New(ctx, c.config.API, c.discord)
	if err != nil {
		return nil, fmt.Errorf("api subscribe: %w", err)
	}

	err = c.api.Subscribe(ctx, c.onMessage)
	if err != nil {
		return nil, fmt.Errorf("api subscribe: %w", err)
	}

	return &c, nil
}

// newRelay builds whichever side of the relay this instance is configured as.
func (c *Client) newRelay(ctx context.Context) error {
	var err error

	switch c.config.Relay.Mode {
	case config.ModeHub:
		c.hub, err = hub.New(ctx, c.config.Relay.Hub)
		if err != nil {
			return fmt.Errorf("hub: %w", err)
		}
		if err = c.hub.Subscribe(ctx, c.onMessage); err != nil {
			return fmt.Errorf("hub subscribe: %w", err)
		}

		if c.config.Relay.Hub.Web.IsEnabled {
			c.web, err = webui.New(c.config.Relay.Hub.Web, c.hub, config.DefaultPath)
			if err != nil {
				return fmt.Errorf("web interface: %w", err)
			}
		}

	case config.ModeAgent:
		c.agent, err = agent.New(ctx, c.config.Relay.Agent)
		if err != nil {
			return fmt.Errorf("agent: %w", err)
		}
		if err = c.agent.Subscribe(ctx, c.onMessage); err != nil {
			return fmt.Errorf("agent subscribe: %w", err)
		}
		c.disableHubOnlyServices()
	}
	return nil
}

// disableHubOnlyServices switches off the services that cannot work on an
// agent, whatever the config file says.
//
// Discord, the registration API and SQL reporting all need the bot, which only
// the hub has. An agent that left them enabled would bind the API port for
// nothing, and would fail outright when an agent and the hub share a box.
func (c *Client) disableHubOnlyServices() {
	if c.config.Discord.IsEnabled {
		tlog.Infof("[talkeq] discord is disabled in agent mode; the hub owns the bot")
		c.config.Discord.IsEnabled = false
	}
	if c.config.API.IsEnabled {
		tlog.Infof("[talkeq] api is disabled in agent mode; it runs on the hub")
		c.config.API.IsEnabled = false
	}
	if c.config.SQLReport.IsEnabled {
		tlog.Infof("[talkeq] sql_report is disabled in agent mode; it runs on the hub")
		c.config.SQLReport.IsEnabled = false
	}
}

// Connect attempts to connect to all enabled endpoints
func (c *Client) Connect(ctx context.Context) error {
	tlog.Debugf("[talkeq] connecting")

	// The hub listens before anything else so agents reconnecting after a
	// restart are not refused while Discord is still coming up.
	if c.hub != nil {
		err := c.hub.Connect(ctx)
		if err != nil {
			return fmt.Errorf("hub connect: %w", err)
		}
	}
	if c.web != nil {
		if err := c.web.Connect(ctx); err != nil {
			// The relay itself is unaffected by a web interface that cannot
			// bind, so this is a warning rather than a reason to refuse to
			// start and take chat down with it.
			tlog.Warnf("[web] could not start: %s", err)
		}
	}
	if c.agent != nil {
		err := c.agent.Connect(ctx)
		if err != nil {
			return fmt.Errorf("agent connect: %w", err)
		}
	}

	err := c.discord.Connect(ctx)
	if err != nil {
		if !c.config.IsKeepAliveEnabled {
			return fmt.Errorf("discord connect: %w", err)
		}
		tlog.Warnf("[discord] connect failed: %s", err)
	}

	err = c.telnet.Connect(ctx)
	if err != nil {
		if !c.config.IsKeepAliveEnabled {
			return fmt.Errorf("telnet connect: %w", err)
		}
		tlog.Warnf("[telnet] connect failed: %s", err)
	}

	err = c.sqlreport.Connect(ctx)
	if err != nil {
		if !c.config.IsKeepAliveEnabled {
			return fmt.Errorf("sqlreport connect: %w", err)
		}
		tlog.Warnf("[sqlreport] connect failed: %s", err)
	}

	err = c.eqlog.Connect(ctx)
	if err != nil {
		if !c.config.IsKeepAliveEnabled {
			return fmt.Errorf("eqlog connect: %w", err)
		}
		tlog.Warnf("[eqlog] connect failed: %s", err)
	}

	err = c.peqeditorsql.Connect(ctx)
	if err != nil {
		if !c.config.IsKeepAliveEnabled {
			return fmt.Errorf("peqeditorsql connect: %w", err)
		}
		tlog.Warnf("[peqeditorsql] connect failed: %s", err)
	}

	err = c.api.Connect(ctx)
	if err != nil {
		if !c.config.IsKeepAliveEnabled {
			return fmt.Errorf("api connect: %w", err)
		}
		tlog.Warnf("[api] connect failed: %s", err)
	}

	go c.loop(ctx)
	return nil
}

func (c *Client) loop(ctx context.Context) {
	var err error
	go func() {
		var err error
		var online int
		for {
			select {
			case <-ctx.Done():
				tlog.Debugf("[talkeq] status loop exit, context done")
				return
			default:
			}
			if c.config.Telnet.IsEnabled {
				online, err = c.telnet.Who(ctx)
				if err != nil {
					tlog.Warnf("[telnet] who failed: %s", err)
				}
				// An agent reports its own count upward so the hub can show a
				// total across every server rather than just its own.
				if c.agent != nil {
					c.agent.SetPlayerCount(online)
					c.agent.SetSourceUp(c.telnet.IsConnected())
				}
			}

			if c.config.Discord.IsEnabled {
				total := online
				if c.hub != nil {
					total += c.hub.PlayerCount()
				}
				err = c.discord.StatusUpdate(ctx, total, "")
				if err != nil {
					tlog.Warnf("[discord] status update failed: %s", err)
				}
			}

			time.Sleep(60 * time.Second)
		}
	}()
	if !c.config.IsKeepAliveEnabled {
		tlog.Debugf("[talkeq] keep_alive disabled in config, exiting client loop")
		return
	}
	for {
		select {
		case <-ctx.Done():
			tlog.Debugf("[talkeq] client loop exit, context done")
			return
		default:
		}
		time.Sleep(c.config.KeepAliveRetryDuration())
		if c.config.Discord.IsEnabled && !c.discord.IsConnected() {
			tlog.Infof("[discord] attempting to reconnect")
			err = c.discord.Connect(ctx)
			if err != nil {
				tlog.Warnf("[discord] reconnect failed: %s", err)
			}
		}
		if c.config.Telnet.IsEnabled && !c.telnet.IsConnected() {
			tlog.Infof("[telnet] attempting to reconnect")
			err = c.telnet.Connect(ctx)
			if err != nil {
				tlog.Warnf("[telnet] reconnect failed: %s", err)
			}
		}
		if c.config.SQLReport.IsEnabled && !c.sqlreport.IsConnected() {
			tlog.Infof("[sqlreport] attempting to reconnect")
			err = c.sqlreport.Connect(ctx)
			if err != nil {
				tlog.Warnf("[sqlreport] connect failed: %s", err)
			}
		}
	}
}

func (c *Client) onMessage(rawReq interface{}) error {
	var err error

	switch req := rawReq.(type) {
	case request.APICommand:
		err = c.api.Command(req)
	case request.DiscordSend:
		err = c.discord.Send(req)
	case request.TelnetSend:
		err = c.telnet.Send(req)
	case request.RelayPublish:
		err = c.onRelayPublish(req)
	default:
		return fmt.Errorf("unknown request type")
	}
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}

// onRelayPublish forwards a locally observed message into the relay.
//
// In hub mode it goes straight into the router. In agent mode it goes up the
// wire. In standalone mode there is nowhere for it to go, which is worth one
// debug line rather than an error, since it means a route was configured for a
// mode this instance is not running in.
func (c *Client) onRelayPublish(req request.RelayPublish) error {
	switch {
	case c.hub != nil:
		event := relay.NewEvent(req.Channel, req.Name, req.Message)
		event.Origin, event.OriginName = c.localRelayIdentity(req.Source)
		c.hub.Publish(event)

	case c.agent != nil:
		c.agent.Publish(req.Channel, req.Name, req.Message)

	default:
		tlog.Debugf("[talkeq] relay route fired on channel %s but relay.mode is %s, ignoring", req.Channel, c.config.Relay.Mode)
	}
	return nil
}

// localRelayIdentity names the source of a message the hub observed itself,
// as opposed to one an agent reported.
func (c *Client) localRelayIdentity(source string) (key string, shortName string) {
	if source == request.RelaySourceDiscord {
		return relay.OriginDiscord, "Discord"
	}
	if c.config.Relay.Hub.LocalServerKey != "" {
		return c.config.Relay.Hub.LocalServerKey, c.config.Relay.Hub.LocalShortName
	}
	// A game server on the hub box that was never given a local_server_key.
	// Naming it "local" keeps it distinguishable from Discord and from every
	// agent, so self-exclusion still works.
	return "local", "Local"
}

// Disconnect attempts to gracefully disconnect all enabled endpoints
func (c *Client) Disconnect(ctx context.Context) error {
	if c.web != nil {
		if err := c.web.Disconnect(ctx); err != nil {
			tlog.Warnf("[web] disconnect failed: %s", err)
		}
	}
	if c.hub != nil {
		if err := c.hub.Disconnect(ctx); err != nil {
			tlog.Warnf("[hub] disconnect failed: %s", err)
		}
	}
	if c.agent != nil {
		if err := c.agent.Disconnect(ctx); err != nil {
			tlog.Warnf("[agent] disconnect failed: %s", err)
		}
	}

	err := c.discord.Disconnect(ctx)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	c.cancel()
	return nil
}
