package adminrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

var testStart = time.Unix(1763000000, 0).UTC()

type testEnv struct {
	repo  *Repository
	db    *sqlitedb.DB
	clock *clock.Fake
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
	repo, err := New(db, fake)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return testEnv{repo: repo, db: db, clock: fake}
}

func hashOf(b byte) [32]byte {
	var h [32]byte
	for i := range h {
		h[i] = b
	}
	return h
}

func newDevice(id string, hashByte byte, at time.Time) NewDevice {
	return NewDevice{
		DeviceID:      id,
		Name:          "laptop " + id,
		Platform:      "linux/amd64",
		ClientVersion: "0.1.0",
		CreatedVia:    CreatedViaSetup,
		TokenHash:     hashOf(hashByte),
		CreatedAt:     at,
		LastSeenIP:    "203.0.113.7",
	}
}

func tableExists(t *testing.T, db *sqlitedb.DB, name string) bool {
	t.Helper()
	var count int
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?1`, name).Scan(&count)
	})
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	return count == 1
}

func TestAdminTablesPresent(t *testing.T) {
	env := newTestEnv(t)
	for _, name := range []string{"admin_meta", "admin_setup_token", "admin_devices", "admin_pairing_codes", "admin_audit"} {
		t.Run(name, func(t *testing.T) {
			if !tableExists(t, env.db, name) {
				t.Fatalf("table %s missing", name)
			}
		})
	}
}

func TestNewRejectsNilDependencies(t *testing.T) {
	env := newTestEnv(t)
	tests := []struct {
		name  string
		db    Database
		clock clock.Clock
	}{
		{name: "nil database", db: nil, clock: env.clock},
		{name: "nil clock", db: env.db, clock: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.db, tc.clock); err == nil {
				t.Fatal("New succeeded, want error")
			}
		})
	}
}

func TestDeviceRoundTripAndClaimState(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	state, err := env.repo.ClaimState(ctx)
	if err != nil || state != ClaimStateUnclaimed {
		t.Fatalf("ClaimState = %q, %v, want unclaimed", state, err)
	}
	dev := newDevice("aaaaaaaaaaaaaaaa", 1, testStart)
	if err := env.repo.InsertDevice(ctx, dev); err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	got, err := env.repo.Device(ctx, dev.DeviceID)
	if err != nil {
		t.Fatalf("Device: %v", err)
	}
	want := Device{
		DeviceID: dev.DeviceID, Name: dev.Name, Platform: dev.Platform, ClientVersion: dev.ClientVersion,
		CreatedVia: CreatedViaSetup, TokenHash: dev.TokenHash, TokenIssuedAt: testStart, CreatedAt: testStart,
		LastSeenAt: testStart, LastSeenIP: dev.LastSeenIP,
	}
	if got != want {
		t.Fatalf("Device = %+v, want %+v", got, want)
	}
	if state, _ := env.repo.ClaimState(ctx); state != ClaimStateClaimed {
		t.Fatalf("ClaimState after insert = %q, want claimed", state)
	}
	if err := env.repo.InsertDevice(ctx, dev); !errors.Is(err, ErrDeviceExists) {
		t.Fatalf("duplicate InsertDevice error = %v, want ErrDeviceExists", err)
	}
	if _, err := env.repo.Device(ctx, "missing"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("Device(missing) error = %v, want ErrDeviceNotFound", err)
	}
}

func TestRotateKeepsPreviousHashForGrace(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	dev := newDevice("aaaaaaaaaaaaaaaa", 1, testStart)
	if err := env.repo.InsertDevice(ctx, dev); err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	rotatedAt := testStart.Add(time.Hour)
	if err := env.repo.Rotate(ctx, dev.DeviceID, hashOf(2), rotatedAt); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	got, _ := env.repo.Device(ctx, dev.DeviceID)
	if got.TokenHash != hashOf(2) || !got.HasPrevToken || got.PrevTokenHash != hashOf(1) {
		t.Fatalf("after rotate hashes = %+v", got)
	}
	if !got.PrevValidUntil.Equal(rotatedAt.Add(PrevTokenGrace)) || !got.TokenIssuedAt.Equal(rotatedAt) {
		t.Fatalf("after rotate times: prev until %v issued %v", got.PrevValidUntil, got.TokenIssuedAt)
	}
	if err := env.repo.MarkNewTokenUsed(ctx, dev.DeviceID); err != nil {
		t.Fatalf("MarkNewTokenUsed: %v", err)
	}
	got, _ = env.repo.Device(ctx, dev.DeviceID)
	if got.HasPrevToken || !got.PrevValidUntil.IsZero() {
		t.Fatalf("previous token survived first use: %+v", got)
	}
	if err := env.repo.Revoke(ctx, dev.DeviceID, rotatedAt); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := env.repo.Rotate(ctx, dev.DeviceID, hashOf(3), rotatedAt); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("Rotate revoked error = %v, want ErrDeviceRevoked", err)
	}
}

func TestRevokeIsIdempotentAndTouchSeen(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	dev := newDevice("aaaaaaaaaaaaaaaa", 1, testStart)
	if err := env.repo.InsertDevice(ctx, dev); err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	later := testStart.Add(time.Minute)
	if err := env.repo.TouchSeen(ctx, dev.DeviceID, "198.51.100.2", later); err != nil {
		t.Fatalf("TouchSeen: %v", err)
	}
	if err := env.repo.Revoke(ctx, dev.DeviceID, later); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := env.repo.Revoke(ctx, dev.DeviceID, later.Add(time.Hour)); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	got, _ := env.repo.Device(ctx, dev.DeviceID)
	if !got.RevokedAt.Equal(later) || got.LastSeenIP != "198.51.100.2" || !got.LastSeenAt.Equal(later) {
		t.Fatalf("device = %+v", got)
	}
	if err := env.repo.TouchSeen(ctx, "missing", "x", later); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("TouchSeen missing error = %v", err)
	}
	if err := env.repo.Revoke(ctx, "missing", later); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("Revoke missing error = %v", err)
	}
}

func TestRevokeIdleAndPurgeRevoked(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	for i, name := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"} {
		if err := env.repo.InsertDevice(ctx, newDevice(name, byte(i+1), testStart)); err != nil {
			t.Fatalf("InsertDevice %s: %v", name, err)
		}
	}
	if err := env.repo.TouchSeen(ctx, "cccccccccccccccc", "ip", testStart.Add(48*time.Hour)); err != nil {
		t.Fatalf("TouchSeen: %v", err)
	}
	env.clock.Set(testStart.Add(72 * time.Hour))
	n, err := env.repo.RevokeIdle(ctx, testStart.Add(24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("RevokeIdle = %d, %v, want 2", n, err)
	}
	revoked, _ := env.repo.Device(ctx, "aaaaaaaaaaaaaaaa")
	if revoked.RevokedBy != RevokedByIdleSweeper || !revoked.RevokedAt.Equal(env.clock.Now()) {
		t.Fatalf("idle revoked device = %+v", revoked)
	}
	active, _ := env.repo.ActiveDeviceCount(ctx)
	if active != 1 {
		t.Fatalf("ActiveDeviceCount = %d, want 1", active)
	}
	purged, err := env.repo.DeleteRevokedBefore(ctx, testStart.Add(100*time.Hour))
	if err != nil || purged != 2 {
		t.Fatalf("DeleteRevokedBefore = %d, %v, want 2", purged, err)
	}
	list, _ := env.repo.ListDevices(ctx, time.Time{})
	if len(list) != 1 || list[0].DeviceID != "cccccccccccccccc" {
		t.Fatalf("ListDevices = %+v", list)
	}
}

func TestPurgeRevokedKeepsServerClaimed(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	for i, name := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		if err := env.repo.InsertDevice(ctx, newDevice(name, byte(i+1), testStart)); err != nil {
			t.Fatalf("InsertDevice: %v", err)
		}
		if err := env.repo.Revoke(ctx, name, testStart.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
	}
	if _, err := env.repo.DeleteRevokedBefore(ctx, testStart.Add(24*time.Hour)); err != nil {
		t.Fatalf("DeleteRevokedBefore: %v", err)
	}
	if state, _ := env.repo.ClaimState(ctx); state != ClaimStateClaimed {
		t.Fatalf("ClaimState = %q, want claimed after purge", state)
	}
	list, _ := env.repo.ListDevices(ctx, time.Time{})
	if len(list) != 1 || list[0].DeviceID != "bbbbbbbbbbbbbbbb" {
		t.Fatalf("remaining devices = %+v, want newest revoked kept", list)
	}
}

func TestListDevicesIncludesRecentlyRevoked(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	for i, name := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"} {
		if err := env.repo.InsertDevice(ctx, newDevice(name, byte(i+1), testStart.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("InsertDevice: %v", err)
		}
	}
	_ = env.repo.Revoke(ctx, "aaaaaaaaaaaaaaaa", testStart.Add(time.Hour))
	_ = env.repo.Revoke(ctx, "bbbbbbbbbbbbbbbb", testStart.Add(10*time.Hour))
	list, err := env.repo.ListDevices(ctx, testStart.Add(5*time.Hour))
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	var ids []string
	for _, d := range list {
		ids = append(ids, d.DeviceID)
	}
	if fmt.Sprint(ids) != "[bbbbbbbbbbbbbbbb cccccccccccccccc]" {
		t.Fatalf("ids = %v", ids)
	}
	if err := env.repo.RenameDevice(ctx, "cccccccccccccccc", "renamed"); err != nil {
		t.Fatalf("RenameDevice: %v", err)
	}
	if got, _ := env.repo.Device(ctx, "cccccccccccccccc"); got.Name != "renamed" {
		t.Fatalf("name = %q", got.Name)
	}
	if err := env.repo.RenameDevice(ctx, "nope", "x"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("RenameDevice missing error = %v", err)
	}
}

func TestSetupTokenClaim(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		prepare func(t *testing.T, env testEnv)
		hash    [32]byte
		now     time.Time
		wantErr error
	}{
		{name: "valid token claims", hash: hashOf(9), now: testStart, prepare: func(t *testing.T, env testEnv) {
			mustReplaceSetup(t, env, hashOf(9), testStart.Add(time.Hour))
		}},
		{name: "wrong token", hash: hashOf(8), now: testStart, wantErr: ErrSetupTokenInvalid, prepare: func(t *testing.T, env testEnv) {
			mustReplaceSetup(t, env, hashOf(9), testStart.Add(time.Hour))
		}},
		{name: "expired token", hash: hashOf(9), now: testStart.Add(2 * time.Hour), wantErr: ErrSetupTokenInvalid, prepare: func(t *testing.T, env testEnv) {
			mustReplaceSetup(t, env, hashOf(9), testStart.Add(time.Hour))
		}},
		{name: "no token minted", hash: hashOf(9), now: testStart, wantErr: ErrSetupTokenInvalid, prepare: func(t *testing.T, env testEnv) {}},
		{name: "already claimed", hash: hashOf(9), now: testStart, wantErr: ErrAlreadyClaimed, prepare: func(t *testing.T, env testEnv) {
			mustReplaceSetup(t, env, hashOf(9), testStart.Add(time.Hour))
			if err := env.repo.InsertDevice(ctx, newDevice("zzzzzzzzzzzzzzzz", 7, testStart)); err != nil {
				t.Fatalf("seed device: %v", err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			tc.prepare(t, env)
			dev := newDevice("aaaaaaaaaaaaaaaa", 1, tc.now)
			dev.CreatedVia = CreatedViaPairingHost
			err := env.repo.ConsumeSetupTokenAndAddDevice(ctx, tc.hash, tc.now, dev)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			got, _ := env.repo.Device(ctx, dev.DeviceID)
			if got.CreatedVia != CreatedViaSetup {
				t.Fatalf("created via = %q, want setup", got.CreatedVia)
			}
			again := env.repo.ConsumeSetupTokenAndAddDevice(ctx, tc.hash, tc.now, newDevice("bbbbbbbbbbbbbbbb", 2, tc.now))
			if !errors.Is(again, ErrAlreadyClaimed) {
				t.Fatalf("second claim error = %v, want ErrAlreadyClaimed", again)
			}
		})
	}
}

func mustReplaceSetup(t *testing.T, env testEnv, hash [32]byte, expires time.Time) {
	t.Helper()
	if err := env.repo.ReplaceSetupToken(context.Background(), hash, expires); err != nil {
		t.Fatalf("ReplaceSetupToken: %v", err)
	}
}

func TestReplaceSetupTokenInvalidatesPrevious(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	mustReplaceSetup(t, env, hashOf(1), testStart.Add(time.Hour))
	mustReplaceSetup(t, env, hashOf(2), testStart.Add(time.Hour))
	err := env.repo.ConsumeSetupTokenAndAddDevice(ctx, hashOf(1), testStart, newDevice("aaaaaaaaaaaaaaaa", 1, testStart))
	if !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatalf("old token error = %v, want ErrSetupTokenInvalid", err)
	}
	if err := env.repo.ConsumeSetupTokenAndAddDevice(ctx, hashOf(2), testStart, newDevice("aaaaaaaaaaaaaaaa", 1, testStart)); err != nil {
		t.Fatalf("new token: %v", err)
	}
}

func seedClaimed(t *testing.T, env testEnv) {
	t.Helper()
	if err := env.repo.InsertDevice(context.Background(), newDevice("zzzzzzzzzzzzzzzz", 7, testStart)); err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

func TestConsumePairingCodeAtomic(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedClaimed(t, env)
	if err := env.repo.ReplacePairingCode(ctx, "saved_messages", hashOf(5), testStart.Add(10*time.Minute)); err != nil {
		t.Fatalf("ReplacePairingCode: %v", err)
	}
	clash := newDevice("zzzzzzzzzzzzzzzz", 8, testStart)
	err := env.repo.ConsumePairingCode(ctx, hashOf(5), testStart, clash)
	if !errors.Is(err, ErrDeviceExists) {
		t.Fatalf("clashing consume error = %v, want ErrDeviceExists", err)
	}
	fresh := newDevice("aaaaaaaaaaaaaaaa", 1, testStart)
	if err := env.repo.ConsumePairingCode(ctx, hashOf(5), testStart, fresh); err != nil {
		t.Fatalf("code survived failed consume? retry error: %v", err)
	}
	got, _ := env.repo.Device(ctx, fresh.DeviceID)
	if got.CreatedVia != CreatedViaPairingSavedMessages {
		t.Fatalf("created via = %q", got.CreatedVia)
	}
	if err := env.repo.ConsumePairingCode(ctx, hashOf(5), testStart, newDevice("bbbbbbbbbbbbbbbb", 2, testStart)); !errors.Is(err, ErrPairingCodeInvalid) {
		t.Fatalf("second consume error = %v, want ErrPairingCodeInvalid", err)
	}
}

func TestConsumePairingCodeRules(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		claimed  bool
		delivery string
		hash     [32]byte
		now      time.Time
		wantErr  error
		wantVia  CreatedVia
	}{
		{name: "saved messages code", claimed: true, delivery: "saved_messages", hash: hashOf(5), now: testStart, wantVia: CreatedViaPairingSavedMessages},
		{name: "host code", claimed: true, delivery: "host", hash: hashOf(5), now: testStart, wantVia: CreatedViaPairingHost},
		{name: "wrong code", claimed: true, delivery: "host", hash: hashOf(6), now: testStart, wantErr: ErrPairingCodeInvalid},
		{name: "expired code", claimed: true, delivery: "host", hash: hashOf(5), now: testStart.Add(11 * time.Minute), wantErr: ErrPairingCodeInvalid},
		{name: "unclaimed server", claimed: false, delivery: "host", hash: hashOf(5), now: testStart, wantErr: ErrNotClaimed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			if tc.claimed {
				seedClaimed(t, env)
			}
			if err := env.repo.ReplacePairingCode(ctx, tc.delivery, hashOf(5), testStart.Add(10*time.Minute)); err != nil {
				t.Fatalf("ReplacePairingCode: %v", err)
			}
			dev := newDevice("aaaaaaaaaaaaaaaa", 1, tc.now)
			err := env.repo.ConsumePairingCode(ctx, tc.hash, tc.now, dev)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil {
				got, _ := env.repo.Device(ctx, dev.DeviceID)
				if got.CreatedVia != tc.wantVia {
					t.Fatalf("created via = %q, want %q", got.CreatedVia, tc.wantVia)
				}
			}
		})
	}
}

func TestPairingCodeReplacedPerDeliveryAndFailures(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedClaimed(t, env)
	expires := testStart.Add(10 * time.Minute)
	_ = env.repo.ReplacePairingCode(ctx, "saved_messages", hashOf(1), expires)
	_ = env.repo.ReplacePairingCode(ctx, "saved_messages", hashOf(2), expires)
	_ = env.repo.ReplacePairingCode(ctx, "host", hashOf(3), expires)
	if err := env.repo.ConsumePairingCode(ctx, hashOf(1), testStart, newDevice("aaaaaaaaaaaaaaaa", 1, testStart)); !errors.Is(err, ErrPairingCodeInvalid) {
		t.Fatalf("replaced code error = %v", err)
	}
	if err := env.repo.ReplacePairingCode(ctx, "other", hashOf(1), expires); err == nil {
		t.Fatal("unknown delivery accepted")
	}
	for want := 1; want <= 3; want++ {
		got, err := env.repo.RecordPairingFailure(ctx)
		if err != nil || got != want {
			t.Fatalf("RecordPairingFailure = %d, %v, want %d", got, err, want)
		}
	}
	if err := env.repo.DeletePairingCode(ctx, "host"); err != nil {
		t.Fatalf("DeletePairingCode: %v", err)
	}
	if err := env.repo.ConsumePairingCode(ctx, hashOf(3), testStart, newDevice("bbbbbbbbbbbbbbbb", 2, testStart)); !errors.Is(err, ErrPairingCodeInvalid) {
		t.Fatalf("deleted code error = %v", err)
	}
	if err := env.repo.InvalidatePairingCodes(ctx); err != nil {
		t.Fatalf("InvalidatePairingCodes: %v", err)
	}
	if err := env.repo.ConsumePairingCode(ctx, hashOf(2), testStart, newDevice("bbbbbbbbbbbbbbbb", 2, testStart)); !errors.Is(err, ErrPairingCodeInvalid) {
		t.Fatalf("invalidated code error = %v", err)
	}
	if got, _ := env.repo.RecordPairingFailure(ctx); got != 1 {
		t.Fatalf("failures after invalidate = %d, want counter reset to 1", got)
	}
}

func TestSuccessfulPairingResetsFailureCounter(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedClaimed(t, env)
	_, _ = env.repo.RecordPairingFailure(ctx)
	_, _ = env.repo.RecordPairingFailure(ctx)
	_ = env.repo.ReplacePairingCode(ctx, "host", hashOf(3), testStart.Add(time.Minute))
	if err := env.repo.ConsumePairingCode(ctx, hashOf(3), testStart, newDevice("aaaaaaaaaaaaaaaa", 1, testStart)); err != nil {
		t.Fatalf("ConsumePairingCode: %v", err)
	}
	if got, _ := env.repo.RecordPairingFailure(ctx); got != 1 {
		t.Fatalf("failures = %d, want 1", got)
	}
}

func TestDeleteExpiredSecrets(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	mustReplaceSetup(t, env, hashOf(1), testStart.Add(time.Hour))
	_ = env.repo.ReplacePairingCode(ctx, "host", hashOf(2), testStart.Add(time.Minute))
	_ = env.repo.ReplacePairingCode(ctx, "saved_messages", hashOf(3), testStart.Add(3*time.Hour))
	n, err := env.repo.DeleteExpiredSecrets(ctx, testStart.Add(2*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("DeleteExpiredSecrets = %d, %v, want 2", n, err)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if _, found, err := env.repo.Meta(ctx, MetaServerID); err != nil || found {
		t.Fatalf("Meta empty = %v, %v", found, err)
	}
	if err := env.repo.SetMeta(ctx, MetaServerID, "abc"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := env.repo.SetMeta(ctx, MetaServerID, "def"); err != nil {
		t.Fatalf("SetMeta overwrite: %v", err)
	}
	if got, err := env.repo.ServerID(ctx); err != nil || got != "def" {
		t.Fatalf("ServerID = %q, %v", got, err)
	}
}

func TestAuditInsertAndPaging(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.repo.InsertDevice(ctx, newDevice("aaaaaaaaaaaaaaaa", 1, testStart)); err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	actions := []string{"setup.claim", "bucket.create", "bucket.delete", "bucketx.other", "access_key.create", "device.rotate"}
	for i, action := range actions {
		entry := AuditEntry{
			At: testStart.Add(time.Duration(i/2) * time.Second), ActorType: "device", ActorDeviceID: "aaaaaaaaaaaaaaaa",
			Action: action, Target: "t", Outcome: AuditOutcomeSuccess, RemoteAddr: "203.0.113.7", RequestID: fmt.Sprint(i),
		}
		if _, err := env.repo.InsertAudit(ctx, entry); err != nil {
			t.Fatalf("InsertAudit: %v", err)
		}
	}
	collect := func(f AuditFilter) []string {
		var ids []string
		for {
			page, err := env.repo.PageAudit(ctx, f)
			if err != nil {
				t.Fatalf("PageAudit: %v", err)
			}
			for _, e := range page.Entries {
				ids = append(ids, e.RequestID)
			}
			if !page.HasMore {
				return ids
			}
			f.After = page.Next
		}
	}
	tests := []struct {
		name   string
		filter AuditFilter
		want   string
	}{
		{name: "all newest first", filter: AuditFilter{Limit: 10}, want: "[5 4 3 2 1 0]"},
		{name: "keyset pages of two", filter: AuditFilter{Limit: 2}, want: "[5 4 3 2 1 0]"},
		{name: "keyset pages of one", filter: AuditFilter{Limit: 1}, want: "[5 4 3 2 1 0]"},
		{name: "exact action", filter: AuditFilter{Action: "bucket.create"}, want: "[1]"},
		{name: "action prefix", filter: AuditFilter{Action: "bucket."}, want: "[2 1]"},
		{name: "since", filter: AuditFilter{Since: testStart.Add(time.Second)}, want: "[5 4 3 2]"},
		{name: "until", filter: AuditFilter{Until: testStart}, want: "[1 0]"},
		{name: "no match", filter: AuditFilter{Action: "nothing."}, want: "[]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmt.Sprint(collect(tc.filter)); got != tc.want {
				t.Fatalf("ids = %s, want %s", got, tc.want)
			}
		})
	}
	page, _ := env.repo.PageAudit(ctx, AuditFilter{Action: "setup.claim"})
	if len(page.Entries) != 1 || page.Entries[0].ActorDeviceName != "laptop aaaaaaaaaaaaaaaa" {
		t.Fatalf("actor device name = %+v", page.Entries)
	}
}

func TestPruneAudit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		entry := AuditEntry{At: testStart.Add(time.Duration(i) * time.Hour), ActorType: "system", Action: "x", Outcome: AuditOutcomeSuccess}
		if _, err := env.repo.InsertAudit(ctx, entry); err != nil {
			t.Fatalf("InsertAudit: %v", err)
		}
	}
	n, err := env.repo.PruneAudit(ctx, testStart.Add(time.Hour), 4)
	if err != nil || n != 2 {
		t.Fatalf("PruneAudit = %d, %v, want 2", n, err)
	}
	page, _ := env.repo.PageAudit(ctx, AuditFilter{})
	if len(page.Entries) != 4 {
		t.Fatalf("entries after prune = %d, want 4", len(page.Entries))
	}
}
