# Modern EQEMU to Discord Chat

[![Go Report Card](https://goreportcard.com/badge/github.com/Zero-Hex/modern-eq-chat)](https://goreportcard.com/report/github.com/Zero-Hex/modern-eq-chat)
[![Build](https://github.com/Zero-Hex/modern-eq-chat/actions/workflows/build.yml/badge.svg)](https://github.com/Zero-Hex/modern-eq-chat/actions/workflows/build.yml)

Bridges EverQuest server chat to Discord, and links several EQEMU servers to
each other so players on different servers share one conversation.

A fork of [TalkEQ](https://github.com/xackery/talkeq) by xackery, which in turn
extends [DiscordEQ](https://github.com/xackery/discordeq). This fork adds
cross-server chat, a hub-and-agent deployment model, hardening, Windows and
Linux service installation, and a local management interface.

### Upgrading from TalkEQ

Put the new binary in the same directory and run it. It renames `talkeq.conf`
and the `talkeq_*` data files to their new names, rewrites the paths recorded
inside the config, and tells you what it changed. Join codes issued by a TalkEQ
hub are still accepted.

## Setup

* Go to [releases](https://github.com/Zero-Hex/modern-eq-chat/releases) and download the latest exe or binary for your operating systsem.
* Go to https://discordapp.com/developers/ and sign in
* Click New Application the top right area
* Write anything you wish for the app name, click Create App
* Start the modern-eq-chat executable once. This generates a modern-eq-chat.conf file
* Copy the Application ID into your modern-eq-chat.conf's discord client_id section
* On the left pane, click Bot
* Click the Reset Token button, Yes, do it!
* Press the copy button in the Token section
* Uncheck the Public Bot option
* Scroll to the bottom of the bot section, and toggle the Message Content Intent option ([Due to this fix](https://discord.com/developers/docs/change-log#sep-1-2022))
* Replace on this link's {CLIENT_ID} field with the client ID you obtained earlier. https://discordapp.com/oauth2/authorize?&client_id={CLIENT_ID}&scope=bot&permissions=268504064
* Open the link and authorize your bot to access your server.
* Ensure the bot now appears offline on your server's general channel

### Configure Modern EQ Chat

* Start modern-eq-chat up. The first run, it will say `a new modern-eq-chat.conf file was created. Please open this file and configure modern-eq-chat, then run it again.`.
* Edit the modern-eq-chat.conf, walking through each section and applying it for your situation. There are comments that help you through the process.

## Cross-Server Chat (Relay)

Modern EQ Chat can link several EQEMU servers so that chat on one is visible on the
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

There are two programs. **`modern-eq-chat-hub`** runs on one box and holds the Discord
bot, the routing rules, and the list of authorized servers. **`modern-eq-chat-agent`**
runs on each game server, reports its chat, and injects what comes back.

```
   server 1 (modern-eq-chat-agent) --+
   server 2 (modern-eq-chat-agent) --+-- modern-eq-chat-hub -- Discord
   server 3 (modern-eq-chat-agent) --+
```

Agents dial **out** to the hub and hold the connection open. That means:

* No port forwarding on any game server, and no fixed IP needed.
* Agents behind NAT work with no configuration.
* One open port in the whole system, on the hub.
* A compromised game server holds only its own token. No Discord credentials
  and no other server's access.

The hub can also be a game server itself; its setup asks.

The original `modern-eq-chat` binary is unchanged and still runs a single server on its
own. Cross-server chat is opt-in.

### Setting up the hub

Run `modern-eq-chat-hub` on the box that will host your Discord bot. With no config
present it walks you through setup, or run `modern-eq-chat-hub setup` to reconfigure.

It asks for your Discord bot credentials, the OOC channel, which port to
listen on, and the address agents should dial. At the end it prints the
certificate fingerprint:

```
  Certificate fingerprint:

    1d29bcd19333e1d474bf174d2a16f51f26c2150bd3309251aca04b07358f0afe
```

Keep that handy; each agent shows it during setup and asks you to confirm.

### Adding a server

On the hub:

```
$ modern-eq-chat-hub enroll server2 "Classic"

Enrollment code for Classic:

    Hub address:      hub.example.com:34197
    Enrollment code:  T723-EZX2-67VF
    Fingerprint:      1d29bcd193...
```

Then on the game server, run `modern-eq-chat-agent`. It asks for the hub address and
that code, shows the fingerprint it received, and asks you to confirm it
matches what the hub printed. Once confirmed it receives its permanent
credentials, writes its config, and is ready to run.

The code is single use and expires in 15 minutes. The hub must be running for
an agent to enroll.

Other hub commands:

```
modern-eq-chat-hub status                  # configured servers and when they last connected
modern-eq-chat-hub web password            # set up the local management interface
modern-eq-chat-hub enroll list             # outstanding codes
modern-eq-chat-hub enroll revoke <id>      # cancel a code
modern-eq-chat-hub agent list              # authorized servers
modern-eq-chat-hub agent rotate server2    # new token, invalidates the old one
modern-eq-chat-hub agent disable server2   # temporarily block
modern-eq-chat-hub agent remove server2    # revoke
```

For scripted installs, `modern-eq-chat-hub agent add <key> [name]` prints a join code
that can be dropped into a provisioning template instead, skipping the
interactive enrollment.

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
wording stay independent. The hub side is `discord_pattern` under
`[[relay.hub.channels]]`; the agent side is `inbound_pattern` under
`[[relay.agent.channels]]`.

### Security

* **Per-agent tokens.** Each server gets its own, so one can be revoked without
  re-keying the others. The hub stores argon2id hashes only, so a leaked
  `modern-eq-chat-agents.json` grants nothing.
* **Identity follows the token.** The hub stamps the originating server from
  whichever token authenticated, ignoring whatever name the agent claims. An
  agent cannot post as another server.
* **Enrollment codes** are single use, expire in 15 minutes, and burn after a
  handful of failed attempts. The durable token is never typed by a human.
* **Certificate pinning.** The hub generates a self-signed certificate on first
  run and each agent pins its fingerprint after you confirm it. Against an
  attacker who can obtain a certificate from any public CA, a pin is stronger
  than normal chain verification. If your hub has a real certificate, set
  `tls_mode = "file"` and leave the fingerprint empty.
* **Command injection.** Relayed names and messages are stripped of line breaks
  and control characters before they can reach a telnet console, and every
  outgoing line is re-checked immediately before it is written.

`tls_mode = "none"` exists for hubs reachable only over a private network such
as WireGuard. It sends tokens in the clear; the setup wizard asks twice before
accepting it.

### Hardening

The hub is the only internet-facing part of a relay, so it assumes hostile
traffic. Defaults are in `[relay.hub.limits]`:

Setting|Default|What it does
---|---|---
`max_agents`|64|Caps concurrent servers
`connections_per_minute`|30|Per source address; excess gets HTTP 429 with `Retry-After`
`auth_failures_before_ban`|5|Bad tokens or codes from one address before it is blocked
`ban_duration`|15m|First block. Repeat offenders double, up to a day
`messages_per_second`|20|Sustained chat rate per agent
`message_burst`|40|Messages one agent may send at once
`allowed_networks`|(empty)|Optional. Restrict to addresses or CIDRs

Set any numeric limit to `-1` to disable it deliberately; `0` means "unset" and
picks up the default.

`allowed_networks` accepts both forms, e.g.
`["203.0.113.4", "10.0.0.0/8"]`. A malformed entry stops the hub starting
rather than silently allowing everyone — a typo that widened the allowlist to
the internet would be the worst possible failure.

Blocks are held in memory and cleared by restarting the hub.

### Choosing a port

The default is 34197. An unusual port is **not** a security measure — scanners
sweep every port continuously. It only avoids a number people probe by habit.
What protects the hub is the token authentication, TLS, and the limits above.

Ports above 49152 are deliberately avoided: that range is what the OS hands out
for outbound connections, and binding a service there can collide with it.

### Running as a service

Both programs install themselves on Windows and Linux:

```
modern-eq-chat-hub service install     # or modern-eq-chat-agent service install
modern-eq-chat-hub service start
modern-eq-chat-hub service status
modern-eq-chat-hub service stop
modern-eq-chat-hub service uninstall
```

Installing needs `sudo` on Linux and "Run as administrator" on Windows; the
commands say so if you forget.

**Linux** writes a systemd unit to `/etc/systemd/system/`. It restarts on
failure, waits for real network connectivity at boot, and is sandboxed with
`ProtectSystem=strict`, `PrivateTmp`, `NoNewPrivileges` and a restricted set of
address families. Only the working directory is writable.

If `systemctl` exists but systemd is not PID 1 — a container, or WSL1 — the
install refuses with an explanation instead of half-writing a unit file. Use
your container runtime's restart policy there.

**Windows** registers with the Service Control Manager, starts automatically,
and restarts on failure. Because Windows starts services in
`C:\Windows\System32` with no way to configure otherwise, the binary changes
to its own directory at startup; `modern-eq-chat.conf`, the certificate, the roster and
the log all live beside the executable.

**macOS and BSD** have no service integration. The relay runs fine; set up
launchd or rc.d yourself.

### Firewall

Only the hub needs an open port. Agents dial out.

```
modern-eq-chat-hub firewall            # show the command for this machine
modern-eq-chat-hub firewall --apply    # run it
```

It detects `netsh` on Windows and `ufw` or `firewalld` on Linux, and prints
manual guidance when it finds neither. Setup shows the command but never runs
it — changing a firewall should not be a side effect of answering questions. If
the box sits behind a router or a cloud security group, that rule matters too.

### Management interface

The hub can serve a local web interface for day-to-day management:

```
modern-eq-chat-hub web password    # set the admin password and enable it
modern-eq-chat-hub web status
modern-eq-chat-hub web disable
```

It shows connected servers with live player counts and health, and lets you
rename servers, enable, disable or remove them, generate enrollment codes, and
edit channel routing. Channel changes apply to the running hub immediately and
are written to `modern-eq-chat.conf`, so they survive a restart. Changing the Discord
bot token still needs one.

**Test** on a server sends a probe and reports the round trip, whether that
agent can actually reach its game server, and optionally injects a visible line
in game. That distinguishes three cases a connection indicator cannot: server
offline, relay up but game server down, and everything working.

#### Reaching it

It binds to `127.0.0.1:34198` and is meant to stay there. From another machine,
tunnel to it:

```
ssh -L 34198:127.0.0.1:34198 you@hub
```

then open `http://127.0.0.1:34198`.

This is deliberate. The interface edits `inbound_pattern`, which becomes a
telnet command on every connected game server — that makes it a privileged
console, not a settings page. Binding it to a public address would put remote
code execution on your servers behind one password. The hub logs a warning if
you point `listen` at a non-loopback address anyway.

Other protections: argon2id password hash (the password itself is never
written to `modern-eq-chat.conf`), session cookies that are `HttpOnly` and
`SameSite=Strict`, a CSRF token on every mutating request, rate-limited logins
with the same escalating block the relay port uses, and a strict content
security policy. The whole UI is embedded in the binary, so nothing loads from
a CDN and there is no path from a URL to the real filesystem.

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

* When modern-eq-chat runs, a users.txt file is generated the same directory as modern-eq-chat. Peek at the file to see the layout.
* If you write to this file, modern-eq-chat will hot reload the contents and update it's lookup table in memory for mapping users from discord to telnet (eq)
* You can write a website to edit this file, or by hand, to update modern-eq-chat and sync your player IGN tags

### Troubleshooting

- **I can talk from in game to discord, but messages in discord to in game fail with "message too small, ignoring, original message:"**: Double check the bot section, and toggle the Message Content Intent option. If this is disabled, the bot just sees empty content messages and fails.

/etc/init.d/modern-eq-chat
change APPDIR/APPBIN, user, and group to your set options
```sh
!/bin/sh

### BEGIN INIT INFO
# Provides:          modern-eq-chatdaemon
# Required-Start:    $local_fs $network $syslog
# Required-Stop:     $local_fs $network $syslog
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: Modern EQ Chat
# Description:       Modern EQ Chat start-stop-daemon - Debian
### END INIT INFO

NAME="modern-eq-chat"
PATH="/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin"
APPDIR="/home/eqemu/modern-eq-chat/"
APPBIN="/home/eqemu/modern-eq-chat/modern-eq-chat"
APPARGS=""
USER="eqemu"
GROUP="eqemu"

# Include functions
set -e
. /lib/lsb/init-functions

start() {
  printf "Starting '$NAME'... "
  start-stop-daemon --start --chuid "$USER:$GROUP" --background --make-pidfile --pidfile /var/run/$NAME.pid --chdir "$APPDIR" --startas /bin/bash -- -c "exec $APPBIN > /var/log/modern-eq-chat.log 2>&1"
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
