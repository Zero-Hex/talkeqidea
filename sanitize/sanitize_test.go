package sanitize

import "testing"

func TestTelnetLineStripsInjection(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"newline becomes space", "hello\nworld", "hello world"},
		{"carriage return becomes space", "hello\r\nworld", "hello world"},
		{"command injection attempt", "hi\nzoneshutdown", "hi zoneshutdown"},
		{"null byte dropped", "hi\x00there", "hithere"},
		{"item link delimiter dropped", "look \x12ABCDEF\x12", "look ABCDEF"},
		{"tab dropped", "a\tb", "ab"},
		{"escape sequence dropped", "a\x1b[31mb", "a[31mb"},
		{"trims", "  spaced  ", "spaced"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TelnetLine(tt.in, MaxMessageLen)
			if got != tt.want {
				t.Errorf("TelnetLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !IsSafeTelnetLine(got) {
				t.Errorf("TelnetLine(%q) = %q, which is not a safe telnet line", tt.in, got)
			}
		})
	}
}

// The whole point of the package: nothing that comes out of TelnetLine may
// carry a line break, whatever went in.
func TestTelnetLineNeverEmitsLineBreak(t *testing.T) {
	inputs := []string{
		"\n", "\r", "\r\n", "a\nb\nc",
		"emote world 260 x\nquit",
		"\n\n\n\n",
		"unicode \u2028 line separator",
		"\u0085 next line",
	}
	for _, in := range inputs {
		got := TelnetLine(in, MaxMessageLen)
		if !IsSafeTelnetLine(got) {
			t.Errorf("TelnetLine(%q) = %q is unsafe", in, got)
		}
	}
}

func TestName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Soandso", "Soandso"},
		{"Soandso the Brave", "SoandsotheBrave"},
		{"Soandso\nquit", "Soandsoquit"},
		{"<@1234>", "1234"},
		{"O'Brien", "O'Brien"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Name(tt.in); got != tt.want {
			t.Errorf("Name(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestServerKey(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Server One", "server-one"},
		{"  PEQ_Test  ", "peq_test"},
		{"Vanilla!!", "vanilla"},
	}
	for _, tt := range tests {
		if got := ServerKey(tt.in); got != tt.want {
			t.Errorf("ServerKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTruncateDoesNotSplitRunes(t *testing.T) {
	// Five 3-byte runes truncated to 8 bytes must cut at a rune boundary.
	got := TelnetLine("日本語日本", 8)
	if !isValidUTF8(got) {
		t.Errorf("truncate split a rune: %q", got)
	}
	if len(got) > 8 {
		t.Errorf("truncate exceeded max: %d bytes", len(got))
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}
