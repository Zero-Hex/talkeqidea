package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Zero-Hex/modern-eq-chat/relay"
	"github.com/gorilla/websocket"
)

// EnrollResult is what an agent receives in exchange for a valid code.
type EnrollResult struct {
	ServerKey string
	ShortName string
	Token     string
	// Fingerprint is the hub certificate this agent should pin from now on.
	// Empty when the connection was plaintext.
	Fingerprint string
}

// EnrollOptions configures a one-shot enrollment attempt.
type EnrollOptions struct {
	// HubAddress is host:port.
	HubAddress string
	// Code is the enrollment code as typed by the operator.
	Code string
	// IsPlaintext skips TLS entirely.
	IsPlaintext bool
	// ExpectFingerprint pins the hub certificate up front. When empty,
	// enrollment is trust-on-first-use and ConfirmFingerprint decides.
	ExpectFingerprint string
	// ConfirmFingerprint is called with the certificate the hub presented, so
	// a wizard can show it and ask the operator to compare it against what the
	// hub printed. Returning false aborts.
	//
	// This is the one moment in the agent's life where its trust in the hub is
	// established rather than checked, so it is a human decision by default.
	ConfirmFingerprint func(fingerprint string) (bool, error)
}

// Enroll exchanges an enrollment code for durable credentials.
//
// It opens its own short-lived connection and does not touch the agent's
// normal session handling: an enrolling agent has no token yet, so there is
// nothing to reconnect with if this fails.
func Enroll(ctx context.Context, opts EnrollOptions) (*EnrollResult, error) {
	if err := relay.ValidateEnrollCode(opts.Code); err != nil {
		return nil, err
	}

	address := opts.HubAddress
	if strings.HasPrefix(address, ":") {
		address = "127.0.0.1" + address
	}
	if address == "" {
		return nil, fmt.Errorf("hub address is required")
	}

	scheme := "wss"
	if opts.IsPlaintext {
		scheme = "ws"
	}
	endpoint := url.URL{Scheme: scheme, Host: address, Path: "/relay/v1"}

	// seen records whatever certificate the hub presents, so it can be
	// reported back to the caller and stored as the pin.
	var seen string

	dialer := &websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	if !opts.IsPlaintext {
		want := normalizeFingerprint(opts.ExpectFingerprint)
		dialer.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// The hub is typically self-signed, so chain verification cannot
			// apply. Either the caller pinned a fingerprint, or the operator
			// confirms the one we saw below.
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return fmt.Errorf("hub presented no certificate")
				}
				sum := sha256.Sum256(rawCerts[0])
				seen = hex.EncodeToString(sum[:])

				if want != "" && seen != want {
					return fmt.Errorf("hub certificate fingerprint %s does not match the expected %s", seen, want)
				}
				return nil
			},
		}
	}

	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	conn, resp, err := dialer.DialContext(dialCtx, endpoint.String(), nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("connect to hub at %s: %w (http %d)", address, err, resp.StatusCode)
		}
		return nil, fmt.Errorf("connect to hub at %s: %w", address, err)
	}
	defer conn.Close()

	// Confirm the certificate before sending the code: the code is a bearer
	// credential, and handing it to the wrong host is the failure we are
	// guarding against.
	if seen != "" && normalizeFingerprint(opts.ExpectFingerprint) == "" && opts.ConfirmFingerprint != nil {
		ok, err := opts.ConfirmFingerprint(seen)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("certificate not confirmed, enrollment aborted")
		}
	}

	request, err := relay.NewFrame(relay.FrameEnroll, &relay.Enroll{
		ProtocolVersion: relay.ProtocolVersion,
		Code:            relay.NormalizeEnrollCode(opts.Code),
	})
	if err != nil {
		return nil, fmt.Errorf("encode enroll: %w", err)
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(request); err != nil {
		return nil, fmt.Errorf("send enroll: %w", err)
	}

	conn.SetReadLimit(relay.MaxFrameSize)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	frame := &relay.Frame{}
	if err := conn.ReadJSON(frame); err != nil {
		return nil, fmt.Errorf("read enrollment response: %w", err)
	}

	if frame.Type == relay.FrameError {
		relayErr := &relay.Error{}
		if decodeErr := frame.Decode(relayErr); decodeErr != nil {
			return nil, fmt.Errorf("hub rejected the enrollment")
		}
		return nil, fmt.Errorf("hub rejected the enrollment: %s", relayErr.Message)
	}
	if frame.Type != relay.FrameEnrolled {
		return nil, fmt.Errorf("expected an enrollment response, got %s", frame.Type)
	}

	enrolled := &relay.Enrolled{}
	if err := frame.Decode(enrolled); err != nil {
		return nil, fmt.Errorf("decode enrollment response: %w", err)
	}
	if enrolled.Token == "" || enrolled.ServerKey == "" {
		return nil, fmt.Errorf("hub returned incomplete credentials")
	}

	return &EnrollResult{
		ServerKey:   enrolled.ServerKey,
		ShortName:   enrolled.ShortName,
		Token:       enrolled.Token,
		Fingerprint: seen,
	}, nil
}

func normalizeFingerprint(fp string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fp), ":", ""))
}
