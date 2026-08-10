package relay

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// Enrollment is how an agent gets its token without anyone typing a 300
// character blob.
//
// The hub operator creates a short code tied to a server key. The agent
// operator types the hub address and that code. The agent connects, presents
// the code, and the hub issues the durable token over that connection. The
// code is single use and expires quickly; the token it buys is never typed by
// a human and never appears on a screen.

// enrollAlphabet is Crockford base32 without I, L, O and U, so a code read
// aloud or copied by hand cannot be ambiguous between 1/I/L or 0/O.
const enrollAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// EnrollCodeLength is the number of significant characters in a code. Twelve
// characters of this alphabet is 60 bits, which is far beyond brute force over
// a network within the code's lifetime.
const EnrollCodeLength = 12

// NewEnrollCode returns a fresh enrollment code, formatted in groups of four.
func NewEnrollCode() (string, error) {
	limit := big.NewInt(int64(len(enrollAlphabet)))

	var b strings.Builder
	for i := 0; i < EnrollCodeLength; i++ {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("read random: %w", err)
		}
		b.WriteByte(enrollAlphabet[n.Int64()])
	}
	return FormatEnrollCode(b.String()), nil
}

// FormatEnrollCode groups a code as XXXX-XXXX-XXXX for display.
func FormatEnrollCode(code string) string {
	code = NormalizeEnrollCode(code)

	var b strings.Builder
	for i, r := range code {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NormalizeEnrollCode strips formatting and fixes the characters people
// commonly mistype, so an operator reading a code aloud does not have to be
// careful about case, spaces, dashes, or O versus zero.
func NormalizeEnrollCode(code string) string {
	var b strings.Builder
	b.Grow(len(code))

	for _, r := range strings.ToUpper(strings.TrimSpace(code)) {
		switch r {
		case '-', ' ', '\t':
			continue
		case 'O':
			b.WriteByte('0')
		case 'I', 'L':
			b.WriteByte('1')
		case 'U':
			b.WriteByte('V')
		default:
			if strings.ContainsRune(enrollAlphabet, r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// ValidateEnrollCode reports whether a typed code is well formed. It says
// nothing about whether the hub will accept it.
func ValidateEnrollCode(code string) error {
	normalized := NormalizeEnrollCode(code)
	if len(normalized) != EnrollCodeLength {
		return fmt.Errorf("an enrollment code is %d characters, got %d", EnrollCodeLength, len(normalized))
	}
	return nil
}

// Enroll is an agent's request to exchange a code for a token.
type Enroll struct {
	ProtocolVersion int    `json:"protocol_version"`
	Code            string `json:"code"`
	Version         string `json:"version,omitempty"`
}

// Enrolled is the hub's response, carrying the durable credentials.
type Enrolled struct {
	ServerKey string `json:"server_key"`
	ShortName string `json:"short_name"`
	Token     string `json:"token"`
}

// Enrollment frame types.
const (
	// FrameEnroll is sent instead of Hello by an agent that has a code but no
	// token yet.
	FrameEnroll FrameType = "enroll"
	// FrameEnrolled carries the issued credentials.
	FrameEnrolled FrameType = "enrolled"
)

// ErrCodeEnrollment is returned when a code is unknown, used or expired.
const ErrCodeEnrollment = "enrollment_failed"
