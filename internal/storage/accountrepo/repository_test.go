package accountrepo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

func newRepo(t *testing.T) *Repository {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Control, filepath.Join(t.TempDir(), "control.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open control db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, schema.Control); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	repo, err := New(db)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return repo
}

func primaryRecord() Record {
	return Record{Role: RolePrimary, UserID: 777000123, Username: "ada", PhoneMasked: "+44 ••• •• 34", DCID: 2, BoundAt: 1000, LinkedAt: 1000}
}

func TestAccountRepoRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	if _, found, err := repo.Get(ctx, RolePrimary); err != nil || found {
		t.Fatalf("get on empty table = %v, %v", found, err)
	}
	if err := repo.Link(ctx, primaryRecord()); err != nil {
		t.Fatalf("link: %v", err)
	}
	got, found, err := repo.Get(ctx, RolePrimary)
	want := primaryRecord()
	want.Linked = true
	if err != nil || !found || got != want {
		t.Fatalf("get = %+v, %v, %v; want %+v", got, found, err, want)
	}
	role, found, err := repo.FindByUser(ctx, 777000123)
	if err != nil || !found || role != RolePrimary {
		t.Fatalf("find by user = %q, %v, %v", role, found, err)
	}
	if _, found, _ := repo.FindByUser(ctx, 5); found {
		t.Fatal("find by unknown user succeeded")
	}
	if err := repo.Unlink(ctx, RolePrimary, 2000); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	got, _, _ = repo.Get(ctx, RolePrimary)
	if got.Linked || got.UnlinkedAt != 2000 || got.UserID != 777000123 || got.BoundAt != 1000 {
		t.Fatalf("after unlink = %+v; the row and its user id must stay", got)
	}
	relink := primaryRecord()
	relink.BoundAt = 3000
	relink.LinkedAt = 3000
	if err := repo.Link(ctx, relink); err != nil {
		t.Fatalf("relink: %v", err)
	}
	got, _, _ = repo.Get(ctx, RolePrimary)
	if !got.Linked || got.UnlinkedAt != 0 || got.LinkedAt != 3000 || got.BoundAt != 1000 {
		t.Fatalf("after relink = %+v; bound_at must keep the first binding", got)
	}
}

func TestAccountRepoListAndReplicaReplacement(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	replica := Record{Role: RoleReplica, UserID: 42, BoundAt: 5, LinkedAt: 5}
	for _, rec := range []Record{replica, primaryRecord()} {
		if err := repo.Link(ctx, rec); err != nil {
			t.Fatalf("link %s: %v", rec.Role, err)
		}
	}
	records, err := repo.List(ctx)
	if err != nil || len(records) != 2 || records[0].Role != RolePrimary || records[1].Role != RoleReplica {
		t.Fatalf("list = %+v, %v", records, err)
	}
	replica.UserID = 43
	if err := repo.Link(ctx, replica); err != nil {
		t.Fatalf("replace replica: %v", err)
	}
	got, _, _ := repo.Get(ctx, RoleReplica)
	if got.UserID != 43 {
		t.Fatalf("replica user = %d, want 43", got.UserID)
	}
	if err := repo.Delete(ctx, RoleReplica); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found, _ := repo.Get(ctx, RoleReplica); found {
		t.Fatal("replica row survives delete")
	}
}

func TestAccountRepoRejectsSameUserInTwoRoles(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	if err := repo.Link(ctx, primaryRecord()); err != nil {
		t.Fatalf("link primary: %v", err)
	}
	replica := primaryRecord()
	replica.Role = RoleReplica
	if err := repo.Link(ctx, replica); err == nil {
		t.Fatal("the same user was bound to both roles")
	}
}

func TestAccountRepoValidation(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{"link unknown role", func() error { return repo.Link(ctx, Record{Role: "x", UserID: 1}) }, ErrInvalidRecord},
		{"link zero user", func() error { return repo.Link(ctx, Record{Role: RolePrimary}) }, ErrInvalidRecord},
		{"link negative dc", func() error { return repo.Link(ctx, Record{Role: RolePrimary, UserID: 1, DCID: -1}) }, ErrInvalidRecord},
		{"get unknown role", func() error { _, _, err := repo.Get(ctx, "x"); return err }, ErrInvalidRecord},
		{"unlink unknown role", func() error { return repo.Unlink(ctx, "x", 1) }, ErrInvalidRecord},
		{"unlink missing row", func() error { return repo.Unlink(ctx, RolePrimary, 1) }, ErrNotFound},
		{"delete unknown role", func() error { return repo.Delete(ctx, "x") }, ErrInvalidRecord},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNewRejectsNilDatabase(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("error = %v, want ErrMissingDependency", err)
	}
}
