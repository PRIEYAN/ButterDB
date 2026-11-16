package setup

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const testServerID = "k3v9m2q8t4w7x1z5a6b2c3d4e5"

type memClaimStore struct {
	mu        sync.Mutex
	devices   []adminrepo.NewDevice
	tokenHash [32]byte
	hasToken  bool
	expiresAt time.Time
	replaced  int
}

func (m *memClaimStore) ClaimState(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked(), nil
}

func (m *memClaimStore) stateLocked() string {
	if len(m.devices) > 0 {
		return ClaimStateClaimed
	}
	return ClaimStateUnclaimed
}

func (m *memClaimStore) ReplaceSetupToken(_ context.Context, hash [32]byte, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenHash = hash
	m.hasToken = true
	m.expiresAt = expiresAt
	m.replaced++
	return nil
}

func (m *memClaimStore) ConsumeSetupTokenAndAddDevice(_ context.Context, hash [32]byte, now time.Time, dev adminrepo.NewDevice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateLocked() == ClaimStateClaimed {
		return ErrAlreadyClaimed
	}
	if !m.hasToken || subtle.ConstantTimeCompare(m.tokenHash[:], hash[:]) != 1 || !now.Before(m.expiresAt) {
		return ErrSetupTokenInvalid
	}
	m.hasToken = false
	m.tokenHash = [32]byte{}
	m.devices = append(m.devices, dev)
	return nil
}

func (m *memClaimStore) deviceCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.devices)
}

func (m *memClaimStore) tokenStored() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasToken
}

type countingIssuer struct {
	mu    sync.Mutex
	issue int
}

func (c *countingIssuer) NewDeviceToken() (string, string, [32]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.issue++
	deviceID := "devaaaaaaaaaaa" + string(rune('a'+c.issue%26)) + "2"
	token := "bdt1_" + deviceID + "_secret"
	return token, deviceID, [32]byte{byte(c.issue)}, nil
}

type sequenceReader struct {
	mu   sync.Mutex
	next uint64
}

func (r *sequenceReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		var word [8]byte
		binary.BigEndian.PutUint64(word[:], r.next*2654435761+uint64(i)*40503)
		p[i] = word[7] ^ word[6]
	}
	r.next++
	return len(p), nil
}

type stubRelay struct{ LoginRelay }

type claimFixture struct {
	svc    *Service
	store  *memClaimStore
	issuer *countingIssuer
	clock  *clock.Fake
	logs   *bytes.Buffer
}

func newClaimFixture(t *testing.T) *claimFixture {
	t.Helper()
	return newClaimFixtureOn(t, &memClaimStore{}, &sequenceReader{})
}

func newClaimFixtureOn(t *testing.T, store *memClaimStore, random io.Reader) *claimFixture {
	t.Helper()
	f := &claimFixture{
		store:  store,
		issuer: &countingIssuer{},
		clock:  clock.NewFake(time.Date(2026, 11, 12, 9, 41, 0, 0, time.UTC)),
		logs:   &bytes.Buffer{},
	}
	svc, err := New(Dependencies{
		Relay:    stubRelay{},
		Claims:   store,
		Tokens:   f.issuer,
		Clock:    f.clock,
		Random:   random,
		Logger:   slog.New(slog.NewJSONHandler(f.logs, nil)),
		TTL:      24 * time.Hour,
		ServerID: testServerID,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	f.svc = svc
	return f
}
