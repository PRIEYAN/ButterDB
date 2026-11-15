package keys

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/keyrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

type adminKeyService interface {
	List(ctx context.Context, includeRevoked bool) ([]access.KeyInfo, error)
	Create(ctx context.Context, name string, scope access.Scope) (access.KeyInfo, string, error)
	Get(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error)
	Update(ctx context.Context, id access.AccessKeyID, name *string, scope *access.Scope) (access.KeyInfo, error)
	Revoke(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error)
}

var (
	_ sigv4.SecretSource = (*Service)(nil)
	_ adminKeyService    = (*Service)(nil)
)

var testStart = time.Unix(1763000000, 0).UTC()

type countingReader struct {
	next byte
}

func (c *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.next
		c.next += 7
	}
	return len(p), nil
}

type countingRepo struct {
	Repository
	mu       sync.Mutex
	gets     int
	touches  int
	afterGet func()
}

func (r *countingRepo) Get(ctx context.Context, id access.AccessKeyID) (keyrepo.Record, error) {
	rec, err := r.Repository.Get(ctx, id)
	r.mu.Lock()
	r.gets++
	hook := r.afterGet
	r.afterGet = nil
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return rec, err
}

func (r *countingRepo) TouchLastUsed(ctx context.Context, id access.AccessKeyID, now time.Time) error {
	r.mu.Lock()
	r.touches++
	r.mu.Unlock()
	return r.Repository.TouchLastUsed(ctx, id, now)
}

func (r *countingRepo) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gets, r.touches
}

type env struct {
	svc   *Service
	repo  *countingRepo
	db    *sqlitedb.DB
	clock *clock.Fake
	ring  *keyring.Ring
}

func newRing(t *testing.T) *keyring.Ring {
	t.Helper()
	root := make([]byte, 32)
	for i := range root {
		root[i] = byte(i + 1)
	}
	path := filepath.Join(t.TempDir(), "server.key")
	if err := os.WriteFile(path, root, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	ring, err := keyring.LoadWithRand(path, &countingReader{next: 3})
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	t.Cleanup(ring.Close)
	return ring
}

func newEnv(t *testing.T) env {
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
	inner, err := keyrepo.New(db)
	if err != nil {
		t.Fatalf("new keyrepo: %v", err)
	}
	repo := &countingRepo{Repository: inner}
	clk := clock.NewFake(testStart)
	ring := newRing(t)
	svc, err := NewService(Dependencies{Repo: repo, Sealer: ring, Clock: clk, Random: &countingReader{}})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return env{svc: svc, repo: repo, db: db, clock: clk, ring: ring}
}

func scopeOf(t *testing.T, buckets []string, actions []string) access.Scope {
	t.Helper()
	scope, err := access.NewScope(buckets, actions)
	if err != nil {
		t.Fatalf("new scope: %v", err)
	}
	return scope
}

func (e env) create(t *testing.T, name string) (access.KeyInfo, string) {
	t.Helper()
	info, secret, err := e.svc.Create(context.Background(), name, scopeOf(t, []string{"photos"}, []string{"read", "list"}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return info, secret
}

func TestCreateFormat(t *testing.T) {
	idPattern := regexp.MustCompile(`^BDBK[A-Z0-9]{16}$`)
	tests := []struct {
		name   string
		random io.Reader
	}{
		{name: "deterministic reader", random: &countingReader{}},
		{name: "reader starting high", random: &countingReader{next: 250}},
		{name: "all ones reader", random: bytes.NewReader(bytes.Repeat([]byte{0xff, 0x01}, 200))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t)
			svc, err := NewService(Dependencies{Repo: e.repo, Sealer: e.ring, Clock: e.clock, Random: test.random})
			if err != nil {
				t.Fatal(err)
			}
			info, secret, err := svc.Create(context.Background(), "website prod", scopeOf(t, []string{"*"}, []string{"read"}))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if !idPattern.MatchString(info.ID.String()) {
				t.Fatalf("id %q does not match BDBK[A-Z0-9]{16}", info.ID)
			}
			if len(secret) != 40 {
				t.Fatalf("secret length = %d, want 40", len(secret))
			}
			raw, err := base64.StdEncoding.DecodeString(secret)
			if err != nil || len(raw) != 30 {
				t.Fatalf("secret is not 30 bytes of standard base64: %v", err)
			}
			if info.Name != "website prod" || info.CreatedAt != testStart.UnixMilli() || info.UpdatedAt != info.CreatedAt || info.Revoked() {
				t.Fatalf("info = %+v", info)
			}
		})
	}
	t.Run("two creates differ", func(t *testing.T) {
		e := newEnv(t)
		first, firstSecret := e.create(t, "one")
		second, secondSecret := e.create(t, "two")
		if first.ID == second.ID || firstSecret == secondSecret {
			t.Fatal("two creates produced identical credentials")
		}
	})
	t.Run("id is rejection sampled", func(t *testing.T) {
		id, err := generateID(bytes.NewReader(append(bytes.Repeat([]byte{255}, 16), bytes.Repeat([]byte{0}, 16)...)))
		if err != nil || id.String() != "BDBK"+strings.Repeat("A", 16) {
			t.Fatalf("generateID = %q, %v", id, err)
		}
	})
	t.Run("random exhausted", func(t *testing.T) {
		e := newEnv(t)
		svc, _ := NewService(Dependencies{Repo: e.repo, Sealer: e.ring, Clock: e.clock, Random: bytes.NewReader(nil)})
		if _, _, err := svc.Create(context.Background(), "x", scopeOf(t, []string{"*"}, []string{"read"})); err == nil {
			t.Fatal("Create succeeded without randomness")
		}
	})
	t.Run("invalid input", func(t *testing.T) {
		e := newEnv(t)
		if _, _, err := e.svc.Create(context.Background(), "", scopeOf(t, []string{"*"}, []string{"read"})); !errors.Is(err, access.ErrInvalidKeyName) {
			t.Fatalf("empty name error = %v", err)
		}
		if _, _, err := e.svc.Create(context.Background(), "ok", access.Scope{}); !errors.Is(err, access.ErrInvalidScope) {
			t.Fatalf("zero scope error = %v", err)
		}
	})
}

func TestSecretShownOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	info, secret := e.create(t, "once")
	if _, err := e.svc.Update(ctx, info.ID, ptr("renamed"), nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := e.svc.Get(ctx, info.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	list, err := e.svc.List(ctx, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	revoked, err := e.svc.Revoke(ctx, info.ID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	rendered := fmt.Sprintf("%+v %#v %+v %+v %+v", info, got, list, revoked, e.svc.cache.size())
	if strings.Contains(rendered, secret) {
		t.Fatal("secret appears in key info output")
	}
	rec, err := e.repo.Repository.Get(ctx, info.ID)
	if err != nil {
		t.Fatalf("repo Get: %v", err)
	}
	if bytes.Contains(rec.SecretSealed, []byte(secret)) || bytes.Contains(rec.SecretSealed, []byte(secret[:12])) {
		t.Fatal("stored row contains the plaintext secret")
	}
}

func TestSealedSecretIs80Bytes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	info, secret := e.create(t, "sealed")
	rec, err := e.repo.Repository.Get(ctx, info.ID)
	if err != nil {
		t.Fatalf("repo Get: %v", err)
	}
	if len(rec.SecretSealed) != 80 {
		t.Fatalf("sealed length = %d, want 80", len(rec.SecretSealed))
	}
	opened, err := e.ring.OpenSecret(info.ID.String(), rec.SecretSealed)
	if err != nil || string(opened) != secret {
		t.Fatalf("OpenSecret mismatch: %v", err)
	}
	got, _, err := e.svc.Secret(ctx, info.ID)
	if err != nil || string(got) != secret {
		t.Fatalf("Secret mismatch: %v", err)
	}
}

func TestSealBindsKeyID(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, _ := e.create(t, "first")
	second, _ := e.create(t, "second")
	firstRec, err := e.repo.Repository.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondRec, err := e.repo.Repository.Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = e.db.Write(ctx, func(tx *sql.Tx) error {
		for _, swap := range []struct {
			id     access.AccessKeyID
			sealed []byte
		}{{first.ID, secondRec.SecretSealed}, {second.ID, firstRec.SecretSealed}} {
			if _, err := tx.ExecContext(ctx, "UPDATE access_keys SET secret_sealed = ?2 WHERE access_key_id = ?1", string(swap.id), swap.sealed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("swap sealed values: %v", err)
	}
	for _, id := range []access.AccessKeyID{first.ID, second.ID} {
		secret, _, err := e.svc.Secret(ctx, id)
		if !errors.Is(err, keyring.ErrSecretUnseal) || secret != nil {
			t.Fatalf("Secret(%s) after swap = %q, %v; want ErrSecretUnseal", id, secret, err)
		}
	}
}

func TestRevokeInvalidatesCache(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	info, _ := e.create(t, "revoked")
	for i := 0; i < 3; i++ {
		if _, _, err := e.svc.Secret(ctx, info.ID); err != nil {
			t.Fatalf("Secret: %v", err)
		}
	}
	if gets, _ := e.repo.counts(); gets != 1 {
		t.Fatalf("repo gets after three cached lookups = %d, want 1", gets)
	}
	if _, err := e.svc.Revoke(ctx, info.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	secret, got, err := e.svc.Secret(ctx, info.ID)
	if !errors.Is(err, errs.ErrAccessKeyRevoked) || secret != nil || !got.Revoked() {
		t.Fatalf("Secret after revoke = %q, %+v, %v", secret, got, err)
	}
	if gets, _ := e.repo.counts(); gets != 2 {
		t.Fatalf("repo gets after revoke = %d, want 2", gets)
	}
}

func TestStaleReadNotCachedAfterRevoke(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	info, _ := e.create(t, "racing")
	fetched := make(chan struct{})
	release := make(chan struct{})
	e.repo.mu.Lock()
	e.repo.afterGet = func() {
		close(fetched)
		<-release
	}
	e.repo.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, _, err := e.svc.Secret(ctx, info.ID)
		done <- err
	}()
	<-fetched
	if _, err := e.svc.Revoke(ctx, info.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("in flight Secret: %v", err)
	}
	if _, _, err := e.svc.Secret(ctx, info.ID); !errors.Is(err, errs.ErrAccessKeyRevoked) {
		t.Fatalf("Secret after racing revoke error = %v, want ErrAccessKeyRevoked", err)
	}
}

func TestUpdateScopeInvalidatesCache(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	info, _ := e.create(t, "scoped")
	_, before, err := e.svc.Secret(ctx, info.ID)
	if err != nil || before.Scope.AllBuckets() || !before.Scope.Allows("photos", access.ActionRead) {
		t.Fatalf("initial scope = %+v, %v", before.Scope, err)
	}
	e.clock.Advance(time.Minute)
	wide := scopeOf(t, []string{"*"}, []string{"write"})
	updated, err := e.svc.Update(ctx, info.ID, nil, &wide)
	if err != nil || !updated.Scope.AllBuckets() || updated.UpdatedAt != e.clock.Now().UnixMilli() {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	_, after, err := e.svc.Secret(ctx, info.ID)
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if !after.Scope.AllBuckets() || after.Scope.HasAction(access.ActionRead) || !after.Scope.HasAction(access.ActionWrite) {
		t.Fatalf("scope served stale after update: %+v", after.Scope)
	}
	tests := []struct {
		name    string
		name_   *string
		scope   *access.Scope
		wantErr error
	}{
		{name: "empty name", name_: ptr(""), wantErr: access.ErrInvalidKeyName},
		{name: "long name", name_: ptr(strings.Repeat("n", 65)), wantErr: access.ErrInvalidKeyName},
		{name: "zero scope", scope: &access.Scope{}, wantErr: access.ErrInvalidScope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := e.svc.Update(ctx, info.ID, test.name_, test.scope); !errors.Is(err, test.wantErr) {
				t.Fatalf("Update error = %v, want %v", err, test.wantErr)
			}
		})
	}
	t.Run("unknown key", func(t *testing.T) {
		if _, err := e.svc.Update(ctx, "BDBKZZZZZZZZZZZZZZZZ", ptr("x"), nil); !errors.Is(err, errs.ErrAccessKeyNotFound) {
			t.Fatalf("Update unknown error = %v", err)
		}
	})
}

func TestLastUsedCoalesced60s(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, _ := e.create(t, "first")
	second, _ := e.create(t, "second")
	steps := []struct {
		name        string
		advance     time.Duration
		id          access.AccessKeyID
		wantTouches int
	}{
		{name: "first use writes", id: first.ID, wantTouches: 1},
		{name: "same instant coalesced", id: first.ID, wantTouches: 1},
		{name: "59 seconds coalesced", advance: 59 * time.Second, id: first.ID, wantTouches: 1},
		{name: "other key writes", id: second.ID, wantTouches: 2},
		{name: "60 seconds after first writes again", advance: time.Second, id: first.ID, wantTouches: 3},
		{name: "immediately coalesced again", id: first.ID, wantTouches: 3},
	}
	for _, step := range steps {
		e.clock.Advance(step.advance)
		if _, _, err := e.svc.Secret(ctx, step.id); err != nil {
			t.Fatalf("%s: Secret: %v", step.name, err)
		}
		if _, touches := e.repo.counts(); touches != step.wantTouches {
			t.Fatalf("%s: touches = %d, want %d", step.name, touches, step.wantTouches)
		}
	}
	got, err := e.svc.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastUsedAt != testStart.Add(60*time.Second).UnixMilli() {
		t.Fatalf("last_used_at = %d, want %d", got.LastUsedAt, testStart.Add(60*time.Second).UnixMilli())
	}
}

func TestUnknownKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tests := []struct {
		name string
		id   access.AccessKeyID
	}{
		{name: "well formed absent", id: "BDBKZZZZZZZZZZZZZZZZ"},
		{name: "malformed", id: "not a key"},
		{name: "empty", id: ""},
		{name: "wrong prefix", id: "AKIAZZZZZZZZZZZZZZZZ"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			secret, info, err := e.svc.Secret(ctx, test.id)
			if !errors.Is(err, errs.ErrAccessKeyNotFound) || secret != nil || info.ID != "" {
				t.Fatalf("Secret = %q, %+v, %v", secret, info, err)
			}
			if _, err := e.svc.Get(ctx, test.id); !errors.Is(err, errs.ErrAccessKeyNotFound) {
				t.Fatalf("Get error = %v", err)
			}
			if _, err := e.svc.Revoke(ctx, test.id); !errors.Is(err, errs.ErrAccessKeyNotFound) {
				t.Fatalf("Revoke error = %v", err)
			}
		})
	}
	if e.svc.cache.size() != 0 {
		t.Fatal("unknown keys were cached")
	}
}

func TestRevokedKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	info, _ := e.create(t, "revoked")
	revoked, err := e.svc.Revoke(ctx, info.ID)
	if err != nil || !revoked.Revoked() {
		t.Fatalf("Revoke = %+v, %v", revoked, err)
	}
	secret, got, err := e.svc.Secret(ctx, info.ID)
	if !errors.Is(err, errs.ErrAccessKeyRevoked) || secret != nil || got.ID != info.ID || !got.Revoked() {
		t.Fatalf("Secret = %q, %+v, %v", secret, got, err)
	}
	if _, touches := e.repo.counts(); touches != 0 {
		t.Fatalf("revoked key touched last_used_at %d times", touches)
	}
	active, err := e.svc.List(ctx, false)
	if err != nil || len(active) != 0 {
		t.Fatalf("List active = %+v, %v", active, err)
	}
	all, err := e.svc.List(ctx, true)
	if err != nil || len(all) != 1 {
		t.Fatalf("List all = %+v, %v", all, err)
	}
	if _, err := e.svc.Update(ctx, info.ID, ptr("again"), nil); !errors.Is(err, errs.ErrAccessKeyRevoked) {
		t.Fatalf("Update revoked error = %v", err)
	}
}

type spySealer struct {
	inner      Sealer
	sawSecret  []byte
	sawBuffer  []byte
	sealFailed error
}

func (s *spySealer) SealSecret(id string, secret []byte) ([]byte, error) {
	s.sawBuffer = secret
	s.sawSecret = bytes.Clone(secret)
	if s.sealFailed != nil {
		return nil, s.sealFailed
	}
	return s.inner.SealSecret(id, secret)
}

func (s *spySealer) OpenSecret(id string, sealed []byte) ([]byte, error) {
	return s.inner.OpenSecret(id, sealed)
}

type failingRepo struct {
	Repository
	err error
}

func (r failingRepo) Insert(context.Context, keyrepo.Record) error { return r.err }

type bufferHandler struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (h *bufferHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *bufferHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.buf.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		h.buf.WriteString(a.String())
		return true
	})
	return nil
}
func (h *bufferHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *bufferHandler) WithGroup(string) slog.Handler      { return h }

func TestSecretNeverLogged(t *testing.T) {
	captured := &bufferHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(captured))
	t.Cleanup(func() { slog.SetDefault(previous) })

	e := newEnv(t)
	ctx := context.Background()
	spy := &spySealer{inner: e.ring, sealFailed: errors.New("seal backend down")}
	failSeal, err := NewService(Dependencies{Repo: e.repo, Sealer: spy, Clock: e.clock, Random: &countingReader{}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := failSeal.Create(ctx, "x", scopeOf(t, []string{"*"}, []string{"read"}))
	if err == nil || secret != "" {
		t.Fatalf("Create with failing sealer = %q, %v", secret, err)
	}
	if strings.Contains(err.Error(), string(spy.sawSecret)) {
		t.Fatal("seal failure error contains the secret")
	}

	spy.sealFailed = nil
	failInsert, err := NewService(Dependencies{Repo: failingRepo{Repository: e.repo, err: errors.New("disk full")}, Sealer: spy, Clock: e.clock, Random: &countingReader{}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err = failInsert.Create(ctx, "x", scopeOf(t, []string{"*"}, []string{"read"}))
	if err == nil || secret != "" || strings.Contains(err.Error(), string(spy.sawSecret)) {
		t.Fatalf("Create with failing insert = %q, %v", secret, err)
	}

	info, created := e.create(t, "logged")
	plain, _, err := e.svc.Secret(ctx, info.ID)
	if err != nil || string(plain) != created {
		t.Fatalf("Secret: %v", err)
	}
	clear(plain)
	if _, err := e.svc.Revoke(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	_, _, revokedErr := e.svc.Secret(ctx, info.ID)
	if revokedErr == nil || strings.Contains(revokedErr.Error(), created) {
		t.Fatalf("revoked error = %v", revokedErr)
	}

	captured.mu.Lock()
	logged := captured.buf.String()
	captured.mu.Unlock()
	if logged != "" {
		t.Fatalf("service wrote to the default logger: %q", logged)
	}
}

func TestCreateClearsSecretBuffer(t *testing.T) {
	e := newEnv(t)
	spy := &spySealer{inner: e.ring}
	svc, err := NewService(Dependencies{Repo: e.repo, Sealer: spy, Clock: e.clock, Random: &countingReader{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Create(context.Background(), "x", scopeOf(t, []string{"*"}, []string{"read"})); err != nil {
		t.Fatal(err)
	}
	if len(spy.sawBuffer) != 40 || !bytes.Equal(spy.sawBuffer, make([]byte, 40)) {
		t.Fatal("unsealed secret buffer was not zeroed after Create")
	}
}

type cyclingReader struct {
	pattern []byte
	pos     int
}

func (c *cyclingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.pattern[c.pos%len(c.pattern)]
		c.pos++
	}
	return len(p), nil
}

func TestCreateRetriesDuplicateID(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	scope := scopeOf(t, []string{"*"}, []string{"read"})
	first, err := NewService(Dependencies{Repo: e.repo, Sealer: e.ring, Clock: e.clock, Random: &countingReader{}})
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := first.Create(ctx, "a", scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("collision then fresh randomness", func(t *testing.T) {
		again, err := NewService(Dependencies{Repo: e.repo, Sealer: e.ring, Clock: e.clock, Random: &countingReader{}})
		if err != nil {
			t.Fatal(err)
		}
		info, _, err := again.Create(ctx, "b", scope)
		if err != nil || info.ID == original.ID {
			t.Fatalf("Create after collision = %+v, %v", info, err)
		}
	})
	t.Run("persistent collision gives up", func(t *testing.T) {
		pattern := make([]byte, 16)
		for i := range pattern {
			pattern[i] = byte(i*11 + 5)
		}
		pattern = append(pattern, make([]byte, 30)...)
		stuck, err := NewService(Dependencies{Repo: e.repo, Sealer: e.ring, Clock: e.clock, Random: &cyclingReader{pattern: pattern}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := stuck.Create(ctx, "c", scope); err != nil {
			t.Fatalf("first stuck Create: %v", err)
		}
		again, err := NewService(Dependencies{Repo: e.repo, Sealer: e.ring, Clock: e.clock, Random: &cyclingReader{pattern: pattern}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := again.Create(ctx, "d", scope); !errors.Is(err, keyrepo.ErrDuplicateKey) {
			t.Fatalf("Create error = %v, want ErrDuplicateKey", err)
		}
	})
}

func TestCacheIsBoundedLRU(t *testing.T) {
	cache := newRecordCache(3)
	record := func(n int) keyrepo.Record {
		return keyrepo.Record{ID: access.AccessKeyID(fmt.Sprintf("BDBK%016d", n))}
	}
	for n := 1; n <= 3; n++ {
		cache.putIfEpoch(record(n), 0)
	}
	if _, ok := cache.get(record(1).ID); !ok {
		t.Fatal("entry 1 missing")
	}
	cache.putIfEpoch(record(4), 0)
	if cache.size() != 3 {
		t.Fatalf("size = %d, want 3", cache.size())
	}
	if _, ok := cache.get(record(2).ID); ok {
		t.Fatal("least recently used entry 2 was not evicted")
	}
	for _, n := range []int{1, 3, 4} {
		if _, ok := cache.get(record(n).ID); !ok {
			t.Fatalf("entry %d evicted", n)
		}
	}
	cache.invalidate(record(1).ID)
	cache.putIfEpoch(record(1), 0)
	if _, ok := cache.get(record(1).ID); ok {
		t.Fatal("stale epoch insert was accepted")
	}
	if cacheCapacity != 1024 {
		t.Fatalf("cacheCapacity = %d, want 1024", cacheCapacity)
	}
}

func TestNewServiceRejectsNilDependencies(t *testing.T) {
	e := newEnv(t)
	full := Dependencies{Repo: e.repo, Sealer: e.ring, Clock: e.clock, Random: &countingReader{}}
	tests := []struct {
		name   string
		mutate func(d *Dependencies)
	}{
		{name: "repo", mutate: func(d *Dependencies) { d.Repo = nil }},
		{name: "sealer", mutate: func(d *Dependencies) { d.Sealer = nil }},
		{name: "clock", mutate: func(d *Dependencies) { d.Clock = nil }},
		{name: "random", mutate: func(d *Dependencies) { d.Random = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := full
			test.mutate(&deps)
			if _, err := NewService(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewService error = %v, want ErrMissingDependency", err)
			}
		})
	}
	if _, err := NewService(full); err != nil {
		t.Fatalf("NewService(full): %v", err)
	}
}

func TestListNewestFirst(t *testing.T) {
	e := newEnv(t)
	var want []access.AccessKeyID
	for i := 0; i < 3; i++ {
		info, _ := e.create(t, fmt.Sprintf("key %d", i))
		want = append([]access.AccessKeyID{info.ID}, want...)
		e.clock.Advance(time.Second)
	}
	infos, err := e.svc.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]access.AccessKeyID, 0, len(infos))
	for _, info := range infos {
		got = append(got, info.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("List order = %v, want %v", got, want)
	}
}

func ptr[T any](v T) *T { return &v }
