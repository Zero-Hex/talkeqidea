package relay

import (
	"strings"
	"testing"
)

func TestTokenRoundTrip(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatalf("new token: %s", err)
	}
	if len(token) < 40 {
		t.Errorf("token is only %d characters, want a 256-bit secret", len(token))
	}

	hash, err := HashToken(token)
	if err != nil {
		t.Fatalf("hash token: %s", err)
	}
	if strings.Contains(hash, token) {
		t.Fatal("hash contains the token in the clear")
	}

	if !VerifyToken(token, hash) {
		t.Error("correct token failed to verify")
	}
	if VerifyToken(token+"x", hash) {
		t.Error("altered token verified")
	}
	if VerifyToken("", hash) {
		t.Error("empty token verified")
	}
}

func TestTokensAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		token, err := NewToken()
		if err != nil {
			t.Fatalf("new token: %s", err)
		}
		if seen[token] {
			t.Fatal("NewToken returned a duplicate")
		}
		seen[token] = true
	}
}

func TestVerifyTokenRejectsMalformedHashes(t *testing.T) {
	malformed := []string{
		"", "not-a-hash", "argon2id$", "argon2id$1$2$3$4",
		"bcrypt$1$65536$4$c2FsdA$a2V5",
		"argon2id$x$65536$4$c2FsdA$a2V5",
		"argon2id$1$65536$4$!!!$a2V5",
	}
	for _, hash := range malformed {
		if VerifyToken("anything", hash) {
			t.Errorf("malformed hash %q verified", hash)
		}
	}
}

func TestJoinCodeRoundTrip(t *testing.T) {
	want := &JoinCode{
		Address:     "hub.example.com:9443",
		ServerKey:   "server2",
		Token:       "abc123",
		Fingerprint: "deadbeef",
	}

	encoded, err := want.Encode()
	if err != nil {
		t.Fatalf("encode: %s", err)
	}
	if !strings.HasPrefix(encoded, "talkeq1_") {
		t.Errorf("join code %q lacks the version prefix", encoded)
	}
	if strings.ContainsAny(encoded, " \t\n\"") {
		t.Errorf("join code %q contains characters that break copy-paste into TOML", encoded)
	}

	got, err := ParseJoinCode(encoded)
	if err != nil {
		t.Fatalf("parse: %s", err)
	}
	if *got != *want {
		t.Errorf("round trip changed the code: %+v vs %+v", got, want)
	}
}

func TestParseJoinCodeRejectsBadInput(t *testing.T) {
	tests := []string{
		"",
		"hello",
		"talkeq1_not-base64!!",
		"talkeq1_" + encodeForTest(t, &JoinCode{ServerKey: "s", Token: "t"}), // no address
		"talkeq1_" + encodeForTest(t, &JoinCode{Address: "a:1", Token: "t"}), // no server key
		"talkeq1_" + encodeForTest(t, &JoinCode{Address: "a:1", ServerKey: "s"}),
	}
	for _, in := range tests {
		if _, err := ParseJoinCode(in); err == nil {
			t.Errorf("ParseJoinCode(%q) accepted an invalid code", in)
		}
	}
}

// encodeForTest builds the base64 body of a join code without the validation
// that Encode's callers normally supply.
func encodeForTest(t *testing.T, j *JoinCode) string {
	t.Helper()
	encoded, err := j.Encode()
	if err != nil {
		t.Fatalf("encode: %s", err)
	}
	return strings.TrimPrefix(encoded, "talkeq1_")
}

func TestEventValidate(t *testing.T) {
	valid := NewEvent("ooc", "Soandso", "hello")
	if err := valid.Validate(); err != nil {
		t.Errorf("valid event rejected: %s", err)
	}

	tests := map[string]*Event{
		"no channel": {Name: "a", Message: "b"},
		"no name":    {Channel: "ooc", Message: "b"},
		"no message": {Channel: "ooc", Name: "a"},
		"long name":  {Channel: "ooc", Name: strings.Repeat("x", 65), Message: "b"},
		"long body":  {Channel: "ooc", Name: "a", Message: strings.Repeat("x", 4001)},
		"loop":       {Channel: "ooc", Name: "a", Message: "b", Hop: 5},
	}
	for name, e := range tests {
		if err := e.Validate(); err == nil {
			t.Errorf("%s: event was accepted", name)
		}
	}
}

// The echo key must ignore which server a message came from, since the agent
// matching an echo has no idea what the original origin was by then.
func TestEchoKeyIgnoresOrigin(t *testing.T) {
	a := &Event{Origin: "server1", Name: "Soandso", Message: "hi"}
	b := &Event{Origin: "server9", Name: "Soandso", Message: "hi"}
	if a.EchoKey() != b.EchoKey() {
		t.Error("echo key depends on origin")
	}

	c := &Event{Name: "Soandso", Message: "hi there"}
	if a.EchoKey() == c.EchoKey() {
		t.Error("different messages share an echo key")
	}

	// Separator must not be forgeable from name/message content.
	d := &Event{Name: "Soand", Message: "sohi"}
	if a.EchoKey() == d.EchoKey() {
		t.Error("echo key is ambiguous across the name/message boundary")
	}
}

func TestFrameRoundTrip(t *testing.T) {
	want := NewEvent("ooc", "Soandso", "hello")
	want.Origin = "server1"

	frame, err := NewFrame(FrameEvent, want)
	if err != nil {
		t.Fatalf("new frame: %s", err)
	}
	if frame.Type != FrameEvent {
		t.Errorf("type = %s, want %s", frame.Type, FrameEvent)
	}

	got := &Event{}
	if err := frame.Decode(got); err != nil {
		t.Fatalf("decode: %s", err)
	}
	if got.Name != want.Name || got.Message != want.Message || got.Origin != want.Origin {
		t.Errorf("round trip changed the event: %+v vs %+v", got, want)
	}
}

func TestDecodeEmptyPayloadFails(t *testing.T) {
	frame := &Frame{Type: FrameEvent}
	if err := frame.Decode(&Event{}); err == nil {
		t.Error("decoded a frame with no payload")
	}
}
