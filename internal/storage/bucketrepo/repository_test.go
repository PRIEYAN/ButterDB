package bucketrepo

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

const testJobID = "00000000-0000-4000-8000-000000000000"

func newRepo(t *testing.T) (*Repository, *sqlitedb.DB) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Projection, filepath.Join(t.TempDir(), "projection.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open projection db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, schema.Projection); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	repo, err := New(db)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return repo, db
}

func execAll(t *testing.T, db *sqlitedb.DB, statements ...string) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		for _, statement := range statements {
			if _, err := tx.ExecContext(context.Background(), statement); err != nil {
				return fmt.Errorf("%s: %w", statement, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func insertBucket(channelID int64, name string, deletedMsgID int64) string {
	return fmt.Sprintf(`INSERT INTO buckets (channel_id, name, policy, encryption_default, cache_max_age_seconds, created_msg_id,
  created_job_id, created_date, config_msg_id, deleted_msg_id, object_count, stored_bytes, plaintext_bytes)
VALUES (%d, '%s', 'public-read', 'tde1', 60, 5, '%s', 1700000000, 5, %d, 3, 300, 200)`, channelID, name, testJobID, deletedMsgID)
}

func TestNewRejectsNilSource(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("error = %v, want ErrMissingDependency", err)
	}
}

func TestBucketLookupByName(t *testing.T) {
	repo, db := newRepo(t)
	execAll(t, db,
		insertBucket(10, "photos", 0),
		insertBucket(11, "old-name", 9),
		insertBucket(12, "reused", 9),
		insertBucket(13, "reused", 0),
	)
	tests := []struct {
		name        string
		bucket      bucket.BucketName
		wantFound   bool
		wantChannel int64
	}{
		{"live bucket", "photos", true, 10},
		{"deleted bucket is gone", "old-name", false, 0},
		{"reused name finds the live channel", "reused", true, 13},
		{"unknown bucket", "missing", false, 0},
		{"names are case sensitive", "Photos", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, found, err := repo.Lookup(context.Background(), tt.bucket)
			if err != nil {
				t.Fatalf("lookup: %v", err)
			}
			if found != tt.wantFound || info.ChannelID != tt.wantChannel {
				t.Fatalf("found = %v channel = %d, want %v and %d", found, info.ChannelID, tt.wantFound, tt.wantChannel)
			}
		})
	}
	info, _, err := repo.Lookup(context.Background(), "photos")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	want := bucket.Info{Name: "photos", ChannelID: 10, Policy: "public-read", DefaultEncryption: "tde1", CacheMaxAge: 60,
		CreatedDate: 1700000000, ObjectCount: 3, StoredBytes: 300, PlaintextBytes: 200}
	if info != want {
		t.Fatalf("info = %+v, want %+v", info, want)
	}
}

func TestBucketLookupChannel(t *testing.T) {
	repo, db := newRepo(t)
	execAll(t, db, insertBucket(10, "photos", 0), insertBucket(11, "gone", 9))
	if info, found, err := repo.LookupChannel(context.Background(), 10); err != nil || !found || info.Name != "photos" {
		t.Fatalf("live channel = %+v, %v, %v", info, found, err)
	}
	if _, found, err := repo.LookupChannel(context.Background(), 11); err != nil || found {
		t.Fatalf("deleted channel found = %v, %v", found, err)
	}
}

func TestBucketListOrdered(t *testing.T) {
	repo, db := newRepo(t)
	infos, err := repo.ListLive(context.Background())
	if err != nil || len(infos) != 0 {
		t.Fatalf("empty list = %v, %v", infos, err)
	}
	execAll(t, db,
		insertBucket(10, "zeta", 0),
		insertBucket(11, "alpha", 0),
		insertBucket(12, "deleted", 4),
		insertBucket(13, "alpha2", 0),
		insertBucket(14, "mid-bucket", 0),
	)
	infos, err = repo.ListLive(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var names []string
	for _, info := range infos {
		names = append(names, string(info.Name))
	}
	if want := []string{"alpha", "alpha2", "mid-bucket", "zeta"}; !slices.Equal(names, want) {
		t.Fatalf("names = %q, want %q", names, want)
	}
}

func TestBucketListUsesLiveNameIndex(t *testing.T) {
	_, db := newRepo(t)
	rows, err := db.Reader().Query("EXPLAIN QUERY PLAN " + selectLiveOrdered)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		plan = append(plan, detail)
	}
	text := strings.Join(plan, "\n")
	if !strings.Contains(text, "buckets_live_name") || strings.Contains(text, "TEMP B-TREE") {
		t.Fatalf("plan does not read buckets_live_name in order:\n%s", text)
	}
}

func insertKey(channelID int64, keyID, fingerprint string, firstMsgID int64) string {
	return fmt.Sprintf(`INSERT INTO encryption_keys (bucket_channel_id, key_id, wrapped_dek, key_check, kek_fingerprint, config_version, first_msg_id, msg_id, job_id, msg_date)
VALUES (%d, '%s', zeroblob(72), zeroblob(61), '%s', 1, %d, %d, '%s', 0)`, channelID, keyID, fingerprint, firstMsgID, firstMsgID+1, testJobID)
}

func fingerprint(letter string) string {
	return "bkf1_" + strings.Repeat(letter, 32)
}

func TestEncryptionKeyLookups(t *testing.T) {
	repo, db := newRepo(t)
	execAll(t, db,
		insertKey(10, strings.Repeat("a", 16), fingerprint("a"), 100),
		insertKey(10, strings.Repeat("b", 16), fingerprint("a"), 200),
		insertKey(10, strings.Repeat("c", 16), fingerprint("c"), 300),
		insertKey(11, strings.Repeat("d", 16), fingerprint("a"), 400),
	)
	active, found, err := repo.ActiveKey(context.Background(), 10, fingerprint("a"))
	if err != nil || !found {
		t.Fatalf("active key = %v, %v", found, err)
	}
	if active.KeyID != strings.Repeat("b", 16) || active.FirstMsgID != 200 || active.MsgID != 201 || active.ConfigVersion != 1 {
		t.Fatalf("active key = %+v, want the newest first_msg_id", active)
	}
	if !bytes.Equal(active.WrappedDEK, make([]byte, 72)) || !bytes.Equal(active.KeyCheck, make([]byte, 61)) {
		t.Fatalf("key material lengths = %d and %d", len(active.WrappedDEK), len(active.KeyCheck))
	}
	if _, found, err := repo.ActiveKey(context.Background(), 10, fingerprint("z")); err != nil || found {
		t.Fatalf("unknown fingerprint = %v, %v", found, err)
	}
	if _, found, err := repo.ActiveKey(context.Background(), 12, fingerprint("a")); err != nil || found {
		t.Fatalf("other channel = %v, %v", found, err)
	}
	byID, found, err := repo.Key(context.Background(), 10, strings.Repeat("a", 16))
	if err != nil || !found || byID.FirstMsgID != 100 {
		t.Fatalf("key by id = %+v, %v, %v", byID, found, err)
	}
	if _, found, err := repo.Key(context.Background(), 10, strings.Repeat("d", 16)); err != nil || found {
		t.Fatalf("key of another channel = %v, %v", found, err)
	}
}

func TestEncryptionKeyRowValidation(t *testing.T) {
	valid := EncryptionKey{
		KeyID:          strings.Repeat("k", 16),
		WrappedDEK:     make([]byte, 72),
		KeyCheck:       make([]byte, 61),
		KEKFingerprint: fingerprint("f"),
		ConfigVersion:  1,
	}
	tests := []struct {
		name   string
		mutate func(*EncryptionKey)
		ok     bool
	}{
		{"valid", func(*EncryptionKey) {}, true},
		{"short key id", func(k *EncryptionKey) { k.KeyID = "short" }, false},
		{"wrapped key one byte short", func(k *EncryptionKey) { k.WrappedDEK = make([]byte, 71) }, false},
		{"wrapped key one byte long", func(k *EncryptionKey) { k.WrappedDEK = make([]byte, 73) }, false},
		{"key check one byte short", func(k *EncryptionKey) { k.KeyCheck = make([]byte, 60) }, false},
		{"empty key check", func(k *EncryptionKey) { k.KeyCheck = nil }, false},
		{"fingerprint wrong prefix", func(k *EncryptionKey) { k.KEKFingerprint = "bkf2_" + strings.Repeat("f", 32) }, false},
		{"fingerprint wrong length", func(k *EncryptionKey) { k.KEKFingerprint = "bkf1_ff" }, false},
		{"config version zero", func(k *EncryptionKey) { k.ConfigVersion = 0 }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := valid
			tt.mutate(&key)
			err := validateKey(key)
			if tt.ok && err != nil {
				t.Fatalf("validate: %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrCorruptKeyRow) {
				t.Fatalf("error = %v, want ErrCorruptKeyRow", err)
			}
		})
	}
}

func TestCorruptKeyRowRejectedOnRead(t *testing.T) {
	repo, db := newRepo(t)
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), "PRAGMA ignore_check_constraints = ON"); err != nil {
			return err
		}
		statement := strings.Replace(insertKey(10, strings.Repeat("a", 16), fingerprint("a"), 100), "zeroblob(72)", "zeroblob(70)", 1)
		_, err := tx.ExecContext(context.Background(), statement)
		return err
	})
	if err != nil {
		t.Skipf("cannot plant a corrupt row: %v", err)
	}
	_, _, err = repo.ActiveKey(context.Background(), 10, fingerprint("a"))
	if !errors.Is(err, ErrCorruptKeyRow) {
		t.Fatalf("error = %v, want ErrCorruptKeyRow", err)
	}
}
