package tlscert

import (
	"crypto/ecdsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"path/filepath"
	"time"
)

const (
	certValidity      = 825 * 24 * time.Hour
	renewWindow       = 30 * 24 * time.Hour
	notBeforeSkew     = time.Hour
	serialBytes       = 16
	certFileMode      = fs.FileMode(0o644)
	tlsDirMode        = fs.FileMode(0o700)
	commonNamePrefix  = "butterdbd admin "
	certTempPattern   = ".admin.crt-*.tmp"
	serialTopBitClear = 0x7f
)

func commonName(serverID string) string {
	return commonNamePrefix + serverID
}

func (s *store) loadCertificate() (*x509.Certificate, bool, error) {
	data, err := s.readFile(s.certPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("tlscert: read certificate file: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != pemTypeCertificate {
		return nil, false, nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, false, nil
	}
	return cert, true, nil
}

func (s *store) certificateIsCurrent(cert *x509.Certificate, key *ecdsa.PrivateKey, wanted nameSet) bool {
	if cert.Subject.CommonName != commonName(s.serverID) {
		return false
	}
	if cert.NotBefore.After(s.now) || !cert.NotAfter.After(s.now.Add(renewWindow)) {
		return false
	}
	certKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !certKey.Equal(&key.PublicKey) {
		return false
	}
	return nameSetOf(cert).equal(wanted)
}

func nameSetOf(cert *x509.Certificate) nameSet {
	candidates := append([]string{}, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		candidates = append(candidates, ip.String())
	}
	return buildNameSet("", candidates)
}

func (s *store) issue(key *ecdsa.PrivateKey, names nameSet) ([]byte, error) {
	serial, err := s.newSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName(s.serverID)},
		NotBefore:             s.now.Add(-notBeforeSkew),
		NotAfter:              s.now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              names.dns,
		IPAddresses:           names.ips,
	}
	der, err := x509.CreateCertificate(s.random, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("tlscert: sign certificate: %w", err)
	}
	return der, nil
}

func (s *store) newSerial() (*big.Int, error) {
	raw := make([]byte, serialBytes)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return nil, fmt.Errorf("tlscert: read serial: %w", err)
	}
	raw[0] &= serialTopBitClear
	serial := new(big.Int).SetBytes(raw)
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	return serial, nil
}

func (s *store) saveCertificate(der []byte) error {
	encoded := pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: der})
	dirPath := filepath.Dir(s.certPath)
	if err := s.files.MkdirAll(dirPath, tlsDirMode); err != nil {
		return fmt.Errorf("tlscert: create tls directory: %w", err)
	}
	if err := s.publishPublicFile(dirPath, encoded); err != nil {
		return err
	}
	if err := s.files.SyncDir(dirPath); err != nil {
		return fmt.Errorf("tlscert: sync tls directory: %w", err)
	}
	return nil
}

func (s *store) publishPublicFile(dirPath string, data []byte) (retErr error) {
	temp, err := s.files.CreateTemp(dirPath, certTempPattern)
	if err != nil {
		return fmt.Errorf("tlscert: create certificate temp file: %w", err)
	}
	tempPath := temp.Name()
	tempOpen := true
	defer func() {
		if tempOpen {
			retErr = errors.Join(retErr, temp.Close())
		}
		if retErr != nil {
			if err := s.files.Remove(tempPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				retErr = errors.Join(retErr, err)
			}
		}
	}()
	if err := temp.Chmod(certFileMode); err != nil {
		return fmt.Errorf("tlscert: set certificate mode: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("tlscert: write certificate: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("tlscert: sync certificate: %w", err)
	}
	tempOpen = false
	if err := temp.Close(); err != nil {
		return fmt.Errorf("tlscert: close certificate: %w", err)
	}
	if err := s.files.Rename(tempPath, s.certPath); err != nil {
		return fmt.Errorf("tlscert: replace certificate: %w", err)
	}
	return nil
}
