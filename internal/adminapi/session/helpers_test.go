package session

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

var testStart = time.Unix(1763000000, 0).UTC()

type countingReader struct {
	next uint64
}

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], r.next*2654435761+uint64(i)*40503)
		p[i] = buf[(i+int(r.next))%8] ^ byte(r.next>>3)
	}
	r.next++
	return len(p), nil
}

type testEnv struct {
	repo   *adminrepo.Repository
	clock  *clock.Fake
	tokens *Tokens
	random *countingReader
}

func newTestEnv(t *testing.T) testEnv {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Control, filepath.Join(t.TempDir(), "control.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open control db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, schema.Control); err != nil {
		t.Fatalf("apply control schema: %v", err)
	}
	fake := clock.NewFake(testStart)
	repo, err := adminrepo.New(db, fake)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	random := &countingReader{next: 1}
	return testEnv{repo: repo, clock: fake, tokens: NewTokens(random), random: random}
}

func (e testEnv) addDevice(t *testing.T) (token string, deviceID string) {
	t.Helper()
	token, deviceID, hash, err := e.tokens.NewDeviceToken()
	if err != nil {
		t.Fatalf("NewDeviceToken: %v", err)
	}
	err = e.repo.InsertDevice(context.Background(), adminrepo.NewDevice{
		DeviceID: deviceID, Name: "laptop", Platform: "linux/amd64", ClientVersion: "0.1.0",
		CreatedVia: adminrepo.CreatedViaSetup, TokenHash: hash, CreatedAt: e.clock.Now(), LastSeenIP: "203.0.113.7",
	})
	if err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	return token, deviceID
}

func (e testEnv) authenticator(limits *Limiter) *Authenticator {
	return NewAuthenticator(e.repo, limits, e.clock)
}

func do(h http.Handler, token string, addr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/devices", nil)
	req.RemoteAddr = addr + ":4000"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
})

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func dialUnix(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}
