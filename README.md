# TalkEQ

[![GoDoc](https://godoc.org/github.com/xackery/talkeq?status.svg)](https://godoc.org/github.com/xackery/talkeq) [![Go Report Card](https://goreportcard.com/badge/github.com/xackery/talkeq)](https://goreportcard.com/report/github.com/xackery/talkeq)

[![Total alerts](https://img.shields.io/lgtm/alerts/g/xackery/talkeq.svg?logo=lgtm&logoWidth=18)](https://lgtm.com/projects/g/xackery/talkeq/alerts/)

[![Platform Tests & Build](https://github.com/xackery/talkeq/actions/workflows/build_workflow.yml/badge.svg?branch=master)](https://github.com/xackery/talkeq/actions/workflows/build_workflow.yml)

TalkEQ bridges links between everquest and other services. Extends [DiscordEQ](https://github.com/xackery/discordeq).

## Setup

* Go to [releases](https://github.com/xackery/talkeq/releases) and download the latest exe or binary for your operating systsem.
* Go to https://discordapp.com/developers/ and sign in
* Click New Application the top right area
* Write anything you wish for the app name, click Create App
* Start the talkeq executable once. This generates a talkeq.conf file
* Copy the Application ID into your talkeq.conf's discord client_id section
* On the left pane, click Bot
* Click the Reset Token button, Yes, do it!
* Press the copy button in the Token section
* Uncheck the Public Bot option
* Scroll to the bottom of the bot section, and toggle the Message Content Intent option ([Due to this fix](https://discord.com/developers/docs/change-log#sep-1-2022))
* Replace on this link's {CLIENT_ID} field with the client ID you obtained earlier. https://discordapp.com/oauth2/authorize?&client_id={CLIENT_ID}&scope=bot&permissions=268504064
* Open the link and authorize your bot to access your server.
* Ensure the bot now appears offline on your server's general channel

### Configure TalkEQ

* Start talkeq up. The first run, it will say `a new talkeq.conf file was created. Please open this file and configure talkeq, then run it again.`.
* Edit the talkeq.conf, walking through each section and applying it for your situation. There are comments that help you through the process.

## Cross-Server Chat (Relay)

TalkEQ can link several EQEMU servers so that chat on one is visible on the
others, with Discord mirroring all of it. When Soandso says something in OOC on
server 1:

```
Discord     Soandso **OOC** [Vanilla]: Hey does anyone know where Master Claude spawns?
Server 2    Soandso says from Vanilla, 'Hey does anyone know where Master Claude spawns?'
Server 3    Soandso says from Vanilla, 'Hey does anyone know where Master Claude spawns?'
```

Messages typed in Discord travel the same path in reverse, reaching every
connected server.

### How it fits together

One instance runs as the **hub** and every other server runs as an **agent**.
The hub holds the Discord bot, the routing rules, and the list of authorized
agents. An agent holds only its own token, so a compromised game server cannot
read the Discord credentials or impersonate another server.

```
   server 1 (agent) ──┐
   server 2 (agent) ──┼── hub ── Discord
   server 3 (agent) ──┘
```

The hub can also be a game server itself. Set `local_server_key` in
`[relay.hub]` and its own chat joins the relay alongside the agents.

`relay.mode` defaults to `standalone`, which is exactly how TalkEQ behaved
before cross-server chat existed. An existing `talkeq.conf` keeps working with
no changes.

### Setting up the hub

On the box that runs your Discord bot, edit `talkeq.conf`:

```toml
[relay]
  mode = "hub"

  [relay.hub]
    listen = ":9443"
    # Where agents on other boxes should dial this hub.
    advertise_address = "your-hub-host.example.com:9443"

    [[relay.hub.channels]]
      name = "ooc"
      enabled = true
      cross_server = true
      discord_channel_id = "123456789012345678"
      discord_pattern = "**[{{.OriginName}}]** {{.Name}} **OOC**: {{.Message}}"
```

Then authorize each server that will connect:

```
./talkeq agent add server2 "Classic"
```

That prints a **join code** — a single blob containing the hub address, the
agent's token, and the hub's TLS certificate fingerprint. It is shown once and
cannot be recovered; use `talkeq agent rotate` to issue a new one.

```
./talkeq agent list                # show authorized servers
./talkeq agent rotate server2      # new token, invalidates the old one
./talkeq agent disable server2     # temporarily block
./talkeq agent remove server2      # revoke
```

### Setting up an agent

On each game server, paste the join code:

```toml
[relay]
  mode = "agent"

  [relay.agent]
    join_code = "talkeq1_..."
    short_name = "Classic"

    [[relay.agent.channels]]
      name = "ooc"
      enabled = true
      inbound_pattern = "emote world 260 {{.Name}} says from {{.OriginName}}, '{{.Message}}'"
```

Then point a telnet route at the relay so local chat is reported upward:

```toml
[[telnet.routes]]
  enabled = true
  target = "relay"
  channel = "ooc"
  [telnet.routes.trigger]
    telnet_pattern = "(\\w+) says ooc, '(.*)'"
    name_index = 1
    message_index = 2
```

To let Discord users talk into the relay, add a Discord route with the same
target on the hub:

```toml
[[discord.routes]]
  enabled = true
  target = "relay"
  channel = "ooc"
  [discord.routes.discord_trigger]
    channel_id = "123456789012345678"
```

### Message patterns

Relay patterns render with these variables:

Variable|Meaning
---|---
`{{.Name}}`|Who sent the message
`{{.Message}}`|The message body
`{{.OriginName}}`|Display name of the server it came from, e.g. `Vanilla`
`{{.Origin}}`|Routing key of that server, e.g. `server1`
`{{.Channel}}`|Logical channel, e.g. `ooc`

Each destination renders its own wording, so Discord formatting and in-game
wording stay independent.

### Security

* **Per-agent tokens.** Each server gets its own, so one can be revoked without
  re-keying the others. The hub stores argon2id hashes only — a leaked
  `talkeq_agents.json` does not grant access.
* **Identity follows the token.** The hub stamps the originating server from
  whichever token authenticated, ignoring whatever name the agent claims. An
  agent cannot post as another server.
* **TLS by default.** `tls_mode = "self-signed"` generates a certificate on
  first run and agents pin its fingerprint via the join code. If your hub has a
  public DNS name and a real certificate, use `tls_mode = "file"` and leave the
  fingerprint empty. `tls_mode = "none"` sends tokens in the clear and is only
  appropriate when the hub is reachable solely over a private network such as
  WireGuard.
* **Command injection.** Relayed names and messages are stripped of line breaks
  and control characters before they can reach a telnet console, and every
  outgoing line is re-checked immediately before it is written.

### Loop prevention

A relayed message injected into a game server echoes back on that server's own
telnet feed, where it would otherwise look like fresh chat. Three guards stop
this:

1. The hub never sends an event back to the server it came from.
2. Each agent remembers what it just injected and suppresses the echo.
3. Every relay increments a hop counter; the hub rejects anything already
   relayed, as well as any event ID it has recently seen.

### Scale

The hub encodes each message once and fans it out through a bounded per-agent
queue. A slow or wedged server sheds its own backlog without blocking the hub
or any other server, so adding the tenth server costs the same as the second.

Agents reconnect on their own with exponential backoff, and report player
counts upward so the Discord bot status shows a total across every server.

### Configure discord users to talk from Discord to EQ

#### Using Discord Roles

* (Admin-level accounts on Discord can only do the following steps.)
* Inside discord go to Server Settings.
* Go to Roles.
* Create a new role, with the name: `IGN: <username>`. The `IGN:` prefix is required for DiscordEQ to detect a player and is used to identify the player in game, For example, to identify the discord user `Xackery` as `Shin`, Create a role named `IGN: Shin`, right click the user Xackery, and assign the role to them.
* If the above user chats inside the assigned channel, their message will appear in game as `Shin says from discord, 'Their Message Here'`

#### Using Users Database

* When talkeq runs, a users.txt file is generated the same directory as talkeq. Peek at the file to see the layout.
* If you write to this file, talkeq will hot reload the contents and update it's lookup table in memory for mapping users from discord to telnet (eq)
* You can write a website to edit this file, or by hand, to update talkeq and sync your player IGN tags

### Troubleshooting

- **I can talk from in game to discord, but messages in discord to in game fail with "message too small, ignoring, original message:"**: Double check the bot section, and toggle the Message Content Intent option. If this is disabled, the bot just sees empty content messages and fails.

/etc/init.d/talkeq
change APPDIR/APPBIN, user, and group to your set options
```sh
!/bin/sh

### BEGIN INIT INFO
# Provides:          talkeqdaemon
# Required-Start:    $local_fs $network $syslog
# Required-Stop:     $local_fs $network $syslog
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: TalkEQ
# Description:       TalkEQ start-stop-daemon - Debian
### END INIT INFO

NAME="talkeq"
PATH="/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin"
APPDIR="/home/eqemu/talkeq/"
APPBIN="/home/eqemu/talkeq/talkeq"
APPARGS=""
USER="eqemu"
GROUP="eqemu"

# Include functions
set -e
. /lib/lsb/init-functions

start() {
  printf "Starting '$NAME'... "
  start-stop-daemon --start --chuid "$USER:$GROUP" --background --make-pidfile --pidfile /var/run/$NAME.pid --chdir "$APPDIR" --startas /bin/bash -- -c "exec $APPBIN > /var/log/talkeq.log 2>&1"
  printf "done\n"
}
#We need this function to ensure the whole process tree will be killed
killtree() {
    local _pid=$1
    local _sig=${2-TERM}
    for _child in $(ps -o pid --no-headers --ppid ${_pid}); do
        killtree ${_child} ${_sig}
    done
    kill -${_sig} ${_pid}
}

stop() {
  printf "Stopping '$NAME'... "
  [ -z `cat /var/run/$NAME.pid 2>/dev/null` ] || \
  while test -d /proc/$(cat /var/run/$NAME.pid); do
    killtree $(cat /var/run/$NAME.pid) 15
    sleep 0.5
  done
  [ -z `cat /var/run/$NAME.pid 2>/dev/null` ] || rm /var/run/$NAME.pid
  printf "done\n"
}

status() {
  status_of_proc -p /var/run/$NAME.pid "" $NAME && exit 0 || exit $?
}

case "$1" in
  start)
    start
    ;;
  stop)
    stop
    ;;
  restart)
    stop
    start
    ;;
  status)
    status
    ;;
  *)
    echo "Usage: $NAME {start|stop|restart|status}" >&2
    exit 1
    ;;
esac

exit 0
```


### Source Services

Name|Channels
---|---
Telnet|ooc, broadcast
EQLog|ooc, guild, auction, general, shout
PEQEditorSQLLog|peqeditorsqllog

### Broadcast Services

Name|Channels
---|---
Discord|ooc, auction, general, peqeditorsqllog
Telnet|ooc


### Service Descriptions

* Telnet - EQEMU uses this as a way to communicate with the server
* EQLog - Everquest's client generates a log when you type /log, and it logs data the client sees
* PEQEditorSQLLog - EQEMU's PEQ Editor is configured to log sql events, you can relay this info to discord
* Discord - Chat service that lets you relay information to it via bots


### Example of using sql:

```toml
# SQL Report can be used to show stats on discord
# An ideal way to set this up is create a private voice channel
# Then bind it to various queries

[sql_report]
	# Enable SQL Reporting
	enabled = false

	# host for database
	# default: 127.0.0.1:3306
	host = "127.0.0.1:3306"

	# username to connect to database with.
	# default: eqemu
	username = "eqemu"

	# password to connect to database with.
	# default: eqemupass
	password = "eqemupass"

	# database to connect to
	# default: eqemu
	database = "eqemu"


[[sql_report.entries]]
	channel_id = "678525065905831968"
	query = "SELECT level2 FROM character_data cd INNER JOIN account a ON a.id = cd.account_id WHERE a.status = 0 ORDER BY level2 DESC LIMIT 1"
	pattern = "Highest Level: {{.Data}}"
	refresh = "5m"

[[sql_report.entries]]
	channel_id = "676283027361366026"
	query = "SELECT count(id) FROM character_data WHERE zone_id != 386 AND last_login > UNIX_TIMESTAMP()-3600"
	pattern = "In Dungeon: {{.Data}}"
	refresh = "60s"

[[sql_report.entries]]
	channel_id = "678525229672169472"
	query = "SELECT count(id) FROM character_data WHERE zone_id = 386 AND last_login > UNIX_TIMESTAMP()-3600"
	pattern = "In Hub: {{.Data}}"
	refresh = "60s"

[[sql_report.entries]]
	channel_id = "676282331627257856"
	query = "SELECT count(id) FROM account"
	pattern = "Accounts: {{.Data}}"
	refresh = "30m"
```
