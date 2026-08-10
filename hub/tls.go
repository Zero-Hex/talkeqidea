package hub

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/Zero-Hex/modern-eq-chat/config"
	"github.com/Zero-Hex/modern-eq-chat/tlog"
)

// certificateFor builds the TLS configuration for the hub listener.
//
// The self-signed path is the default because most hubs are an IP address on
// someone's home server, where there is no DNS name to get a real certificate
// for. A self-signed certificate whose fingerprint agents pin is not a
// downgrade: pinning is strictly stronger than CA verification against an
// attacker who can obtain a certificate from any public CA.
func certificateFor(cfg *config.HubConfig) (*tls.Config, string, error) {
	switch cfg.TLSMode {
	case config.TLSNone:
		return nil, "", nil

	case config.TLSFile:
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertPath, cfg.TLSKeyPath)
		if err != nil {
			return nil, "", fmt.Errorf("load key pair: %w", err)
		}
		return baseTLSConfig(cert), fingerprintOf(cert), nil

	case config.TLSLetsEncrypt:
		// Deliberately not auto-managing certificates in-process: an operator
		// running letsencrypt already has a renewal mechanism, and a second
		// one competing for port 80 is a support burden. Point tls_cert and
		// tls_key at the issued files.
		return nil, "", fmt.Errorf("tls_mode letsencrypt: point tls_cert and tls_key at your issued certificate and use tls_mode = \"file\"")

	case config.TLSSelfSigned:
		cert, isNew, err := loadOrCreateSelfSigned(cfg)
		if err != nil {
			return nil, "", err
		}
		fp := fingerprintOf(cert)
		if isNew {
			tlog.Infof("[hub] generated a self-signed certificate, fingerprint %s", fp)
		}
		return baseTLSConfig(cert), fp, nil

	default:
		return nil, "", fmt.Errorf("unknown tls_mode %q", cfg.TLSMode)
	}
}

func baseTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
}

// fingerprintOf returns the hex SHA-256 of the leaf certificate, which is what
// agents pin.
func fingerprintOf(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}

func loadOrCreateSelfSigned(cfg *config.HubConfig) (tls.Certificate, bool, error) {
	cert, err := tls.LoadX509KeyPair(cfg.TLSCertPath, cfg.TLSKeyPath)
	if err == nil {
		return cert, false, nil
	}
	if !os.IsNotExist(err) {
		// A present but unreadable or corrupt pair is worth reporting rather
		// than silently replacing: regenerating would invalidate every agent's
		// pinned fingerprint at once.
		if _, statErr := os.Stat(cfg.TLSCertPath); statErr == nil {
			return tls.Certificate{}, false, fmt.Errorf("existing certificate at %s is unusable: %w", cfg.TLSCertPath, err)
		}
	}

	certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		return tls.Certificate{}, false, err
	}
	if err := os.WriteFile(cfg.TLSCertPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, false, fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(cfg.TLSKeyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, false, fmt.Errorf("write key: %w", err)
	}

	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, false, fmt.Errorf("parse generated key pair: %w", err)
	}
	return cert, true, nil
}

func generateSelfSigned() (certPEM []byte, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "modern-eq-chat-hub"},
		NotBefore:    time.Now().Add(-time.Hour),
		// Ten years: agents pin the fingerprint, so expiry buys nothing here
		// and a surprise expiry would take the whole relay down at once.
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"modern-eq-chat-hub", "localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}
