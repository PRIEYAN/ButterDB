package credstore

import (
	"fmt"
	"sync"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type Memory struct {
	mu     sync.Mutex
	tokens map[string]string
}

func NewMemory() *Memory {
	return &Memory{tokens: make(map[string]string)}
}

func (m *Memory) Get(serverID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	token, ok := m.tokens[serverID]
	if !ok {
		return "", fmt.Errorf("credstore: memory get: %w", model.ErrTokenNotFound)
	}
	return token, nil
}

func (m *Memory) Set(serverID, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[serverID] = token
	return nil
}

func (m *Memory) Delete(serverID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tokens[serverID]; !ok {
		return fmt.Errorf("credstore: memory delete: %w", model.ErrTokenNotFound)
	}
	delete(m.tokens, serverID)
	return nil
}

func (m *Memory) Kind() model.TokenStorage {
	return model.TokenMemory
}
