package profilestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	fileVersion = 1
	fileMode    = 0o600
	dirMode     = 0o700
	appDirName  = "butterdb-admin"
	fileName    = "servers.json"
)

var ErrUnsupportedVersion = errors.New("servers file version is not supported")

type document struct {
	Version      int             `json:"version"`
	LastServerID string          `json:"last_server_id"`
	Servers      []model.Profile `json:"servers"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func PathIn(configDir string) string {
	return filepath.Join(configDir, appDirName, fileName)
}

func New(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("profilestore: path is empty")
	}
	return &Store{path: path}, nil
}

func (s *Store) List() ([]model.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	return append([]model.Profile(nil), doc.Servers...), nil
}

func (s *Store) Get(serverID string) (model.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return model.Profile{}, err
	}
	index := indexOf(doc.Servers, serverID)
	if index < 0 {
		return model.Profile{}, fmt.Errorf("profilestore: get %q: %w", serverID, model.ErrProfileNotFound)
	}
	return doc.Servers[index], nil
}

func (s *Store) Put(p model.Profile) error {
	if p.ServerID == "" {
		return errors.New("profilestore: put: server id is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	if index := indexOf(doc.Servers, p.ServerID); index >= 0 {
		doc.Servers[index] = p
	} else {
		doc.Servers = append(doc.Servers, p)
	}
	return s.save(doc)
}

func (s *Store) Delete(serverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	index := indexOf(doc.Servers, serverID)
	if index < 0 {
		return fmt.Errorf("profilestore: delete %q: %w", serverID, model.ErrProfileNotFound)
	}
	doc.Servers = append(doc.Servers[:index], doc.Servers[index+1:]...)
	if doc.LastServerID == serverID {
		doc.LastServerID = ""
	}
	return s.save(doc)
}

func (s *Store) LastUsed() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return "", err
	}
	return doc.LastServerID, nil
}

func (s *Store) SetLastUsed(serverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	if indexOf(doc.Servers, serverID) < 0 {
		return fmt.Errorf("profilestore: set last used %q: %w", serverID, model.ErrProfileNotFound)
	}
	doc.LastServerID = serverID
	return s.save(doc)
}

func indexOf(profiles []model.Profile, serverID string) int {
	for i, p := range profiles {
		if p.ServerID == serverID {
			return i
		}
	}
	return -1
}

func (s *Store) load() (document, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return document{Version: fileVersion}, nil
	}
	if err != nil {
		return document{}, fmt.Errorf("profilestore: read: %w", err)
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return document{}, fmt.Errorf("profilestore: decode: %w", err)
	}
	if doc.Version != fileVersion {
		return document{}, fmt.Errorf("profilestore: version %d: %w", doc.Version, ErrUnsupportedVersion)
	}
	return doc, nil
}

func (s *Store) save(doc document) error {
	doc.Version = fileVersion
	if doc.Servers == nil {
		doc.Servers = []model.Profile{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("profilestore: encode: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("profilestore: create directory: %w", err)
	}
	if err := writeAtomic(dir, s.path, data); err != nil {
		return fmt.Errorf("profilestore: write: %w", err)
	}
	return nil
}

func writeAtomic(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".servers-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := writeTemp(tmp, data); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return syncDir(dir)
}

func writeTemp(tmp *os.File, data []byte) error {
	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	return tmp.Close()
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
