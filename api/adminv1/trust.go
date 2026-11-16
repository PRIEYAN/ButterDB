package adminv1

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

type TrustMode string

const (
	TrustPinned TrustMode = "pinned"
	TrustSystem TrustMode = "system"
)

const (
	pinPrefix     = "SHA256:"
	pinDigestSize = sha256.Size
	probeTimeout  = 30 * time.Second
)

type Trust struct {
	Mode       TrustMode `json:"mode"`
	SPKISHA256 string    `json:"spki_sha256,omitempty"`
}

type CertificateInfo struct {
	SPKISHA256              string
	DisplayFingerprint      string
	Subject                 string
	DNSNames                []string
	IPAddresses             []string
	NotAfter                time.Time
	VerifiesWithSystemRoots bool
}

type PinMismatchError struct {
	Expected string
	Got      string
}

func (e *PinMismatchError) Error() string {
	if e == nil {
		return "adminv1: <nil>"
	}
	return "adminv1: server certificate key " + e.Got + " does not match pinned key " + e.Expected
}

func canonicalPin(sum [pinDigestSize]byte) string {
	return pinPrefix + hex.EncodeToString(sum[:])
}

func displayPin(sum [pinDigestSize]byte) string {
	encoded := hex.EncodeToString(sum[:])
	groups := make([]string, 0, len(encoded)/4)
	for i := 0; i < len(encoded); i += 4 {
		groups = append(groups, encoded[i:i+4])
	}
	return strings.Join(groups, " ")
}

func parsePin(raw string) ([pinDigestSize]byte, error) {
	var sum [pinDigestSize]byte
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) >= len(pinPrefix) && strings.EqualFold(trimmed[:len(pinPrefix)], pinPrefix) {
		trimmed = trimmed[len(pinPrefix):]
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil || len(decoded) != pinDigestSize {
		return sum, errors.New("adminv1: pinned key must be SHA256: followed by 64 hex characters")
	}
	copy(sum[:], decoded)
	return sum, nil
}

func leafPin(cert *x509.Certificate) [pinDigestSize]byte {
	return sha256.Sum256(cert.RawSubjectPublicKeyInfo)
}

func newTLSConfig(trust Trust) (*tls.Config, error) {
	switch trust.Mode {
	case TrustSystem:
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	case TrustPinned:
		want, err := parsePin(trust.SPKISHA256)
		if err != nil {
			return nil, err
		}
		return &tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true,
			VerifyConnection:   pinCheck(want),
		}, nil
	default:
		return nil, fmt.Errorf("adminv1: unknown trust mode %q", string(trust.Mode))
	}
}

func pinCheck(want [pinDigestSize]byte) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("adminv1: server presented no certificate")
		}
		got := leafPin(state.PeerCertificates[0])
		if subtle.ConstantTimeCompare(want[:], got[:]) == 1 {
			return nil
		}
		return &PinMismatchError{Expected: canonicalPin(want), Got: canonicalPin(got)}
	}
}

func ProbeCertificate(ctx context.Context, baseURL string) (CertificateInfo, error) {
	base, err := parseBaseURL(baseURL)
	if err != nil {
		return CertificateInfo{}, err
	}
	if base.Scheme != "https" {
		return CertificateInfo{}, errors.New("adminv1: certificate probe needs an https URL")
	}
	host := base.Hostname()
	port := base.Port()
	if port == "" {
		port = "443"
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{},
		Config:    &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true},
	}
	conn, err := dialer.DialContext(probeCtx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return CertificateInfo{}, fmt.Errorf("adminv1: probe %s: %w", net.JoinHostPort(host, port), err)
	}
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return CertificateInfo{}, errors.New("adminv1: probe did not negotiate tls")
	}
	peers := tlsConn.ConnectionState().PeerCertificates
	if len(peers) == 0 {
		return CertificateInfo{}, errors.New("adminv1: server presented no certificate")
	}
	leaf := peers[0]
	sum := leafPin(leaf)
	info := CertificateInfo{
		SPKISHA256:              canonicalPin(sum),
		DisplayFingerprint:      displayPin(sum),
		Subject:                 leaf.Subject.String(),
		DNSNames:                append([]string(nil), leaf.DNSNames...),
		NotAfter:                leaf.NotAfter,
		VerifiesWithSystemRoots: verifiesWithSystemRoots(host, peers),
	}
	for _, ip := range leaf.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, ip.String())
	}
	return info, nil
}

func verifiesWithSystemRoots(host string, peers []*x509.Certificate) bool {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return false
	}
	intermediates := x509.NewCertPool()
	for _, cert := range peers[1:] {
		intermediates.AddCert(cert)
	}
	_, err = peers[0].Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: intermediates})
	return err == nil
}
