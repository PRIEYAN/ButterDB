package tlscert

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
)

const (
	tlsDirName   = "tls"
	keyFileName  = "admin.key"
	certFileName = "admin.crt"
)

type Material struct {
	Certificate tls.Certificate
	SPKISHA256  [32]byte
	NotAfter    time.Time
}

type Filesystem interface {
	fsx.FS
	Open(name string) (io.ReadCloser, error)
}

type Config struct {
	Dir       string
	ServerID  string
	Hostnames []string
	Now       time.Time
	Random    io.Reader
	Hostname  func() (string, error)
	Files     Filesystem
}

type store struct {
	serverID string
	now      time.Time
	random   io.Reader
	files    Filesystem
	secrets  *secretfile.Writer
	keyPath  string
	certPath string
}

func LoadOrCreate(dir string, serverID string, hostnames []string, now time.Time) (Material, error) {
	return LoadOrCreateWith(Config{
		Dir:       dir,
		ServerID:  serverID,
		Hostnames: hostnames,
		Now:       now,
		Random:    rand.Reader,
		Hostname:  os.Hostname,
		Files:     fsx.OS{},
	})
}

func LoadOrCreateWith(cfg Config) (Material, error) {
	if err := cfg.validate(); err != nil {
		return Material{}, err
	}
	secrets, err := secretfile.New(cfg.Files)
	if err != nil {
		return Material{}, fmt.Errorf("tlscert: %w", err)
	}
	tlsDir := filepath.Join(cfg.Dir, tlsDirName)
	s := &store{
		serverID: cfg.ServerID,
		now:      cfg.Now,
		random:   cfg.Random,
		files:    cfg.Files,
		secrets:  secrets,
		keyPath:  filepath.Join(tlsDir, keyFileName),
		certPath: filepath.Join(tlsDir, certFileName),
	}
	hostname, hostnameErr := cfg.Hostname()
	if hostnameErr != nil {
		hostname = ""
	}
	return s.loadOrCreate(buildNameSet(hostname, cfg.Hostnames))
}

func (c Config) validate() error {
	if c.Random == nil {
		return fmt.Errorf("tlscert: %w: random", errs.ErrMissingDependency)
	}
	if c.Hostname == nil {
		return fmt.Errorf("tlscert: %w: hostname", errs.ErrMissingDependency)
	}
	if c.Files == nil {
		return fmt.Errorf("tlscert: %w: files", errs.ErrMissingDependency)
	}
	if c.Dir == "" {
		return fmt.Errorf("tlscert: %w: directory", errs.ErrInvalidKey)
	}
	if c.ServerID == "" {
		return fmt.Errorf("tlscert: %w: server id", errs.ErrInvalidKey)
	}
	if c.Now.IsZero() {
		return fmt.Errorf("tlscert: %w: time", errs.ErrInvalidKey)
	}
	return nil
}

func (s *store) loadOrCreate(names nameSet) (Material, error) {
	key, err := s.loadKey()
	if err != nil {
		return Material{}, err
	}
	if key == nil {
		if key, err = s.generateKey(); err != nil {
			return Material{}, err
		}
		if err := s.saveKey(key); err != nil {
			return Material{}, err
		}
	}
	cert, found, err := s.loadCertificate()
	if err != nil {
		return Material{}, err
	}
	if found && s.certificateIsCurrent(cert, key, names) {
		return materialOf(cert, key), nil
	}
	der, err := s.issue(key, names)
	if err != nil {
		return Material{}, err
	}
	if err := s.saveCertificate(der); err != nil {
		return Material{}, err
	}
	issued, err := x509.ParseCertificate(der)
	if err != nil {
		return Material{}, fmt.Errorf("tlscert: parse issued certificate: %w", err)
	}
	return materialOf(issued, key), nil
}

func materialOf(cert *x509.Certificate, key *ecdsa.PrivateKey) Material {
	return Material{
		Certificate: tls.Certificate{
			Certificate: [][]byte{cert.Raw},
			PrivateKey:  key,
			Leaf:        cert,
		},
		SPKISHA256: sha256.Sum256(cert.RawSubjectPublicKeyInfo),
		NotAfter:   cert.NotAfter,
	}
}
