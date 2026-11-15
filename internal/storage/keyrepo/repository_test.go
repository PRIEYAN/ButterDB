package keyrepo

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
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

var testStart = time.Unix(1763000000, 0).UTC()

type testEnv struct {
	repo *Repository
	db   *sqlitedb.DB
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
	repo, err := New(db)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return testEnv{repo: repo, db: db}
}

func testScope(t *testing.T, buckets []string, actions []string) access.Scope {
	t.Helper()
	scope, err := access.NewScope(buckets, actions)
	if err != nil {
		t.Fatalf("new scope: %v", err)
	}
	return scope
}

func testID(n int) access.AccessKeyID {
	return access.AccessKeyID(fmt.Sprintf("BDBK%016d", n))
}

func testRecord(t *testing.T, n int) Record {
	t.Helper()
	created := testStart.Add(time.Duration(n) * time.Second).UnixMilli()
	return Record{
		ID:           testID(n),
		Name:         fmt.Sprintf("key %d", n),
		SecretSealed: bytes.Repeat([]byte{byte(n)}, 80),
		Scope:        testScope(t, []string{"photos", "avatars"}, []string{"read", "list"}),
		CreatedAt:    created,
		CreatedBy:    "device:one",
		UpdatedAt:    created,
	}
}

func TestInsertGetRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	wild := testRecord(t, 2)
	wild.Scope = testScope(t, []string{"*"}, []string{"write", "delete"})
	wild.LastUsedAt = 1234
	tests := []struct {
		name string
		rec  Record
	}{
		{name: "named buckets", rec: testRecord(t, 1)},
		{name: "wildcard bucket with last used", rec: wild},
		{name: "unicode name", rec: func() Record { r := testRecord(t, 3); r.Name = "café サイト"; return r }()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := env.repo.Insert(ctx, test.rec); err != nil {
				t.Fatalf("Insert: %v", err)
			}
			got, err := env.repo.Get(ctx, test.rec.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.ID != test.rec.ID || got.Name != test.rec.Name || !bytes.Equal(got.SecretSealed, test.rec.SecretSealed) {
				t.Fatalf("Get = %+v, want %+v", got, test.rec)
			}
			if !slices.Equal(got.Scope.Buckets(), test.rec.Scope.Buckets()) || !slices.Equal(got.Scope.Actions(), test.rec.Scope.Actions()) {
				t.Fatalf("scope = %v/%v, want %v/%v", got.Scope.Buckets(), got.Scope.Actions(), test.rec.Scope.Buckets(), test.rec.Scope.Actions())
			}
			if got.CreatedAt != test.rec.CreatedAt || got.CreatedBy != test.rec.CreatedBy || got.UpdatedAt != test.rec.UpdatedAt ||
				got.LastUsedAt != test.rec.LastUsedAt || got.RevokedAt != 0 {
				t.Fatalf("timestamps = %+v, want %+v", got, test.rec)
			}
		})
	}
	t.Run("duplicate id", func(t *testing.T) {
		err := env.repo.Insert(ctx, testRecord(t, 1))
		if !errors.Is(err, ErrDuplicateKey) {
			t.Fatalf("Insert duplicate error = %v, want ErrDuplicateKey", err)
		}
	})
	t.Run("unknown id", func(t *testing.T) {
		_, err := env.repo.Get(ctx, testID(99))
		if !errors.Is(err, errs.ErrAccessKeyNotFound) {
			t.Fatalf("Get unknown error = %v, want ErrAccessKeyNotFound", err)
		}
	})
	t.Run("zero scope", func(t *testing.T) {
		rec := testRecord(t, 50)
		rec.Scope = access.Scope{}
		if err := env.repo.Insert(ctx, rec); !errors.Is(err, access.ErrInvalidScope) {
			t.Fatalf("Insert zero scope error = %v, want ErrInvalidScope", err)
		}
	})
}

func TestListOrderNewestFirst(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	for _, n := range []int{2, 5, 1, 4, 3} {
		if err := env.repo.Insert(ctx, testRecord(t, n)); err != nil {
			t.Fatalf("Insert %d: %v", n, err)
		}
	}
	tied := testRecord(t, 6)
	tied.CreatedAt = testRecord(t, 5).CreatedAt
	if err := env.repo.Insert(ctx, tied); err != nil {
		t.Fatalf("Insert tied: %v", err)
	}
	if _, err := env.repo.Revoke(ctx, testID(5), testStart.Add(time.Hour)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	tests := []struct {
		name           string
		includeRevoked bool
		want           []access.AccessKeyID
	}{
		{name: "active only", want: []access.AccessKeyID{testID(6), testID(4), testID(3), testID(2), testID(1)}},
		{name: "include revoked", includeRevoked: true, want: []access.AccessKeyID{testID(5), testID(6), testID(4), testID(3), testID(2), testID(1)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records, err := env.repo.List(ctx, test.includeRevoked)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			got := make([]access.AccessKeyID, 0, len(records))
			for _, rec := range records {
				got = append(got, rec.ID)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("List ids = %v, want %v", got, test.want)
			}
		})
	}
	t.Run("empty table", func(t *testing.T) {
		records, err := newTestEnv(t).repo.List(ctx, true)
		if err != nil || records == nil || len(records) != 0 {
			t.Fatalf("List empty = %v, %v", records, err)
		}
	})
}

func TestRevokeSetsRevokedAt(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.repo.Insert(ctx, testRecord(t, 1)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	first := testStart.Add(time.Hour)
	revoked, err := env.repo.Revoke(ctx, testID(1), first)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if revoked.RevokedAt != first.UnixMilli() || revoked.UpdatedAt != first.UnixMilli() {
		t.Fatalf("revoked = %+v, want revoked_at %d", revoked, first.UnixMilli())
	}
	again, err := env.repo.Revoke(ctx, testID(1), first.Add(time.Hour))
	if err != nil || again.RevokedAt != first.UnixMilli() {
		t.Fatalf("second Revoke = %+v, %v; revoked_at must stay %d", again, err, first.UnixMilli())
	}
	if _, err := env.repo.Revoke(ctx, testID(9), first); !errors.Is(err, errs.ErrAccessKeyNotFound) {
		t.Fatalf("Revoke unknown error = %v", err)
	}
	if err := env.repo.TouchLastUsed(ctx, testID(1), first); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	got, err := env.repo.Get(ctx, testID(1))
	if err != nil || got.LastUsedAt != first.UnixMilli() || got.RevokedAt != first.UnixMilli() {
		t.Fatalf("Get after touch = %+v, %v", got, err)
	}
	if err := env.repo.TouchLastUsed(ctx, testID(9), first); !errors.Is(err, errs.ErrAccessKeyNotFound) {
		t.Fatalf("TouchLastUsed unknown error = %v", err)
	}
}

func TestUpdateChangesNameAndScope(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := env.repo.Insert(ctx, testRecord(t, 1)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	now := testStart.Add(time.Minute)
	newName := "renamed"
	newScope := testScope(t, []string{"*"}, []string{"write"})
	tests := []struct {
		name      string
		newName   *string
		newScope  *access.Scope
		wantName  string
		wantAll   bool
		wantStamp int64
	}{
		{name: "name only", newName: &newName, wantName: "renamed", wantStamp: now.UnixMilli()},
		{name: "scope only", newScope: &newScope, wantName: "renamed", wantAll: true, wantStamp: now.UnixMilli()},
		{name: "neither", wantName: "renamed", wantAll: true, wantStamp: now.UnixMilli()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := env.repo.Update(ctx, testID(1), test.newName, test.newScope, now)
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if got.Name != test.wantName || got.Scope.AllBuckets() != test.wantAll || got.UpdatedAt != test.wantStamp {
				t.Fatalf("Update = %+v", got)
			}
			stored, err := env.repo.Get(ctx, testID(1))
			if err != nil || stored.Name != got.Name || stored.Scope.AllBuckets() != got.Scope.AllBuckets() {
				t.Fatalf("stored = %+v, %v", stored, err)
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		if _, err := env.repo.Update(ctx, testID(9), &newName, nil, now); !errors.Is(err, errs.ErrAccessKeyNotFound) {
			t.Fatalf("Update unknown error = %v", err)
		}
	})
	t.Run("revoked", func(t *testing.T) {
		if _, err := env.repo.Revoke(ctx, testID(1), now); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		if _, err := env.repo.Update(ctx, testID(1), &newName, nil, now.Add(time.Second)); !errors.Is(err, errs.ErrAccessKeyRevoked) {
			t.Fatalf("Update revoked error = %v", err)
		}
	})
}

func TestCheckConstraintsEnforced(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	tests := []struct {
		name   string
		mutate func(rec *Record)
	}{
		{name: "id wrong prefix", mutate: func(rec *Record) { rec.ID = access.AccessKeyID("AKIA" + strings.Repeat("A", 16)) }},
		{name: "id too short", mutate: func(rec *Record) { rec.ID = access.AccessKeyID("BDBKSHORT") }},
		{name: "name empty", mutate: func(rec *Record) { rec.Name = "" }},
		{name: "name too long", mutate: func(rec *Record) { rec.Name = strings.Repeat("n", 65) }},
		{name: "sealed too short", mutate: func(rec *Record) { rec.SecretSealed = make([]byte, 79) }},
		{name: "sealed too long", mutate: func(rec *Record) { rec.SecretSealed = make([]byte, 81) }},
		{name: "sealed nil", mutate: func(rec *Record) { rec.SecretSealed = nil }},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := testRecord(t, 10+i)
			test.mutate(&rec)
			if err := env.repo.Insert(ctx, rec); err == nil || errors.Is(err, ErrDuplicateKey) {
				t.Fatalf("Insert error = %v, want a check constraint failure", err)
			}
		})
	}
	rawTests := []struct {
		name    string
		buckets string
		actions string
	}{
		{name: "buckets not json", buckets: "not json", actions: `["read"]`},
		{name: "buckets object", buckets: `{"a":1}`, actions: `["read"]`},
		{name: "actions not json", buckets: `["*"]`, actions: "[read"},
		{name: "actions string", buckets: `["*"]`, actions: `"read"`},
	}
	for i, test := range rawTests {
		t.Run(test.name, func(t *testing.T) {
			id := string(testID(40 + i))
			err := env.db.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, insertKey, id, "raw", make([]byte, 80), test.buckets, test.actions, 1, "", 1, 0, nil)
				return err
			})
			if err == nil {
				t.Fatal("raw insert accepted invalid scope json")
			}
		})
	}
	if records, err := env.repo.List(ctx, true); err != nil || len(records) != 0 {
		t.Fatalf("rows after rejected inserts = %d, %v", len(records), err)
	}
}

func TestNewRejectsNilDatabase(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("New(nil) error = %v", err)
	}
}
