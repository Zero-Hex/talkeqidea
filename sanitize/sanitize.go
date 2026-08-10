// Package sanitize hardens untrusted text before it is used to build a telnet
// command line.
//
// This matters more than it looks. A relayed message ends up interpolated into
// an EQEMU console command such as:
//
//	emote world 260 Soandso says from Vanilla, 'hello'
//
// The telnet console is line oriented, so a newline surviving anywhere in that
// string ends the emote and starts a second command that the server will
// happily execute. Every field arriving from a remote peer — a Discord user, a
// hub, an agent — passes through here before it reaches the wire.
package sanitize

import (
	"strings"
	"unicode"
)

// MaxNameLen bounds a character or user name.
const MaxNameLen = 64

// MaxMessageLen bounds a relayed chat message.
const MaxMessageLen = 1000

// TelnetLine makes s safe to embed in a telnet command.
//
// Control characters are removed rather than escaped: there is no escaping
// convention the EQEMU console honors, so anything that could terminate or
// redirect the command simply must not be there.
func TelnetLine(s string, max int) string {
	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		switch {
		case r == '\n' || r == '\r':
			// A line break would split this into a second console command.
			b.WriteRune(' ')
		case r == 0x12:
			// EQ item-link delimiter. Links are decoded upstream; a stray
			// delimiter here would corrupt the client's parsing.
			continue
		case r == unicode.ReplacementChar:
			continue
		case unicode.IsControl(r):
			continue
		case !unicode.IsPrint(r) && r != ' ':
			continue
		default:
			b.WriteRune(r)
		}
	}

	out := strings.TrimSpace(collapseSpaces(b.String()))
	return truncate(out, max)
}

// Name reduces s to something usable as an in-game character name.
//
// Names are held to a stricter standard than message bodies because they are
// frequently placed in command position, where a space alone can shift the
// meaning of the command.
func Name(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.' || r == '\'':
			b.WriteRune(r)
		default:
			// Drop everything else, including spaces.
		}
	}

	return truncate(b.String(), MaxNameLen)
}

// Message makes a chat body safe to relay.
func Message(s string) string {
	return TelnetLine(s, MaxMessageLen)
}

// ServerKey reduces s to a routing identifier: lowercase alphanumerics,
// dashes and underscores. Server keys appear in config, in log lines and in
// the agents database, so keeping them boring avoids quoting problems.
func ServerKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return truncate(b.String(), MaxNameLen)
}

// IsSafeTelnetLine reports whether s can be sent as a telnet command without
// modification. Used as a last-line assertion immediately before writing, so a
// future code path that forgets to sanitize fails loudly instead of executing
// an injected command.
func IsSafeTelnetLine(s string) bool {
	if strings.ContainsAny(s, "\r\n\x00") {
		return false
	}
	return len(s) <= 8192
}

func collapseSpaces(s string) string {
	if !strings.Contains(s, "  ") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	wasSpace := false
	for _, r := range s {
		if r == ' ' {
			if wasSpace {
				continue
			}
			wasSpace = true
		} else {
			wasSpace = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// truncate cuts s to max bytes without splitting a multi-byte rune.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	for max > 0 && !isRuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func isRuneStart(b byte) bool {
	return b&0xC0 != 0x80
}
