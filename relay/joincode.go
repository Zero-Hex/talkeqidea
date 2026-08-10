package relay

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// A join code is the single blob an operator copies from the hub to an agent
// during setup. Bundling the address, the certificate fingerprint and the
// token together means there is no way to configure the token correctly but
// forget the pin, which is the mistake that would silently downgrade the
// connection's security.

const joinCodePrefix = "talkeq1_"

// JoinCode carries everything an agent needs to reach its hub.
type JoinCode struct {
	// Address is the hub's host:port.
	Address string `json:"a"`
	// ServerKey is the identity this agent will connect as.
	ServerKey string `json:"k"`
	// Token authenticates the agent.
	Token string `json:"t"`
	// Fingerprint pins the hub's TLS certificate as a hex SHA-256 of the
	// DER-encoded certificate. Empty means a publicly trusted certificate is
	// expected and normal CA verification applies.
	Fingerprint string `json:"f,omitempty"`
}

// Encode renders a join code as a single copy-pasteable string.
func (j *JoinCode) Encode() (string, error) {
	buf, err := json.Marshal(j)
	if err != nil {
		return "", fmt.Errorf("marshal join code: %w", err)
	}
	return joinCodePrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// ParseJoinCode reads a join code produced by Encode.
func ParseJoinCode(s string) (*JoinCode, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, joinCodePrefix) {
		return nil, fmt.Errorf("not a talkeq join code (expected %s prefix)", joinCodePrefix)
	}
	buf, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, joinCodePrefix))
	if err != nil {
		return nil, fmt.Errorf("decode join code: %w", err)
	}
	j := &JoinCode{}
	if err := json.Unmarshal(buf, j); err != nil {
		return nil, fmt.Errorf("parse join code: %w", err)
	}
	if j.Address == "" {
		return nil, fmt.Errorf("join code has no address")
	}
	if j.ServerKey == "" {
		return nil, fmt.Errorf("join code has no server key")
	}
	if j.Token == "" {
		return nil, fmt.Errorf("join code has no token")
	}
	return j, nil
}
