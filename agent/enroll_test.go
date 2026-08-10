package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Zero-Hex/modern-eq-chat/agent"
	"github.com/Zero-Hex/modern-eq-chat/hub"
	"github.com/Zero-Hex/modern-eq-chat/relay"
)

// The setup path an operator actually walks: the hub issues a short code, the
// agent redeems it for a durable token, and that token then works for a normal
// connection.
func TestEnrollIssuesAWorkingToken(t *testing.T) {
	h, _ := startHub(t)

	code, err := h.Enroll().Create("server2", "Classic", hub.DefaultEnrollTTL)
	if err != nil {
		t.Fatalf("create code: %s", err)
	}

	result, err := agent.Enroll(context.Background(), agent.EnrollOptions{
		HubAddress:  h.Addr(),
		Code:        code,
		IsPlaintext: true,
	})
	if err != nil {
		t.Fatalf("enroll: %s", err)
	}

	if result.ServerKey != "server2" {
		t.Errorf("server key = %q, want server2", result.ServerKey)
	}
	if result.ShortName != "Classic" {
		t.Errorf("short name = %q, want Classic", result.ShortName)
	}
	if result.Token == "" {
		t.Fatal("no token issued")
	}

	// The issued token must authenticate a real session.
	if _, err := h.Roster().Authenticate(result.ServerKey, result.Token); err != nil {
		t.Errorf("issued token does not authenticate: %s", err)
	}
}

// A code is spent by the first agent to use it.
func TestEnrollCodeWorksOnlyOnce(t *testing.T) {
	h, _ := startHub(t)

	code, err := h.Enroll().Create("server2", "Classic", hub.DefaultEnrollTTL)
	if err != nil {
		t.Fatalf("create code: %s", err)
	}

	if _, err := agent.Enroll(context.Background(), agent.EnrollOptions{
		HubAddress: h.Addr(), Code: code, IsPlaintext: true,
	}); err != nil {
		t.Fatalf("first enroll: %s", err)
	}

	_, err = agent.Enroll(context.Background(), agent.EnrollOptions{
		HubAddress: h.Addr(), Code: code, IsPlaintext: true,
	})
	if err == nil {
		t.Fatal("the same code enrolled twice")
	}
}

func TestEnrollRejectsWrongCode(t *testing.T) {
	h, _ := startHub(t)

	if _, err := h.Enroll().Create("server2", "Classic", hub.DefaultEnrollTTL); err != nil {
		t.Fatalf("create code: %s", err)
	}

	_, err := agent.Enroll(context.Background(), agent.EnrollOptions{
		HubAddress:  h.Addr(),
		Code:        "ZZZZ-ZZZZ-ZZZZ",
		IsPlaintext: true,
	})
	if err == nil {
		t.Fatal("a wrong code was accepted")
	}
	// The failure must not reveal whether a code exists or merely expired.
	if strings.Contains(strings.ToLower(err.Error()), "expired") {
		t.Errorf("error distinguishes expiry from an unknown code: %s", err)
	}
}

func TestEnrollRejectsExpiredCode(t *testing.T) {
	h, _ := startHub(t)

	code, err := h.Enroll().Create("server2", "Classic", time.Millisecond)
	if err != nil {
		t.Fatalf("create code: %s", err)
	}
	time.Sleep(1100 * time.Millisecond)

	if _, err := agent.Enroll(context.Background(), agent.EnrollOptions{
		HubAddress: h.Addr(), Code: code, IsPlaintext: true,
	}); err == nil {
		t.Fatal("an expired code was accepted")
	}
}

// Operators mistype codes. The normalizer should absorb case, spacing and the
// character confusions the alphabet was chosen to avoid.
func TestEnrollAcceptsSloppilyTypedCode(t *testing.T) {
	h, _ := startHub(t)

	code, err := h.Enroll().Create("server2", "Classic", hub.DefaultEnrollTTL)
	if err != nil {
		t.Fatalf("create code: %s", err)
	}

	sloppy := "  " + strings.ToLower(strings.ReplaceAll(code, "-", " ")) + "  "

	result, err := agent.Enroll(context.Background(), agent.EnrollOptions{
		HubAddress: h.Addr(), Code: sloppy, IsPlaintext: true,
	})
	if err != nil {
		t.Fatalf("enroll with sloppily typed code: %s", err)
	}
	if result.ServerKey != "server2" {
		t.Errorf("server key = %q, want server2", result.ServerKey)
	}
}

func TestEnrollRejectsMalformedCodeWithoutContactingHub(t *testing.T) {
	_, err := agent.Enroll(context.Background(), agent.EnrollOptions{
		HubAddress:  "127.0.0.1:1",
		Code:        "too-short",
		IsPlaintext: true,
	})
	if err == nil {
		t.Fatal("a malformed code was accepted")
	}
	if !strings.Contains(err.Error(), "characters") {
		t.Errorf("err = %v, want a length complaint before any dial", err)
	}
}

// Repeated guessing burns the outstanding codes rather than grinding forever.
func TestEnrollAttemptsBurnTheCode(t *testing.T) {
	h, _ := startHub(t)

	code, err := h.Enroll().Create("server2", "Classic", hub.DefaultEnrollTTL)
	if err != nil {
		t.Fatalf("create code: %s", err)
	}

	for i := 0; i < 6; i++ {
		_, _ = h.Enroll().Redeem("ZZZZZZZZZZZZ")
	}

	if _, err := h.Enroll().Redeem(code); err == nil {
		t.Error("the real code still worked after repeated failed guesses")
	}
}

func TestEnrollCodeFormatting(t *testing.T) {
	code, err := relay.NewEnrollCode()
	if err != nil {
		t.Fatalf("new code: %s", err)
	}

	if err := relay.ValidateEnrollCode(code); err != nil {
		t.Errorf("generated code failed validation: %s", err)
	}
	if strings.Count(code, "-") != 2 {
		t.Errorf("code %q is not grouped for readability", code)
	}

	// The alphabet must exclude the characters people confuse.
	for _, bad := range []rune{'I', 'L', 'O', 'U'} {
		if strings.ContainsRune(relay.NormalizeEnrollCode(code), bad) {
			t.Errorf("code %q contains ambiguous character %c", code, bad)
		}
	}
}
