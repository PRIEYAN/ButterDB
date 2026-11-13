package phase02

import (
	"context"
	"database/sql"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

func TestSchemaMigrationsBuildAndIntegrityCheck(t *testing.T) {
	cases := []struct {
		name string
		db   sqlitedb.Name
		set  schema.Set
	}{
		{"projection database", sqlitedb.Projection, schema.Projection},
		{"control database", sqlitedb.Control, schema.Control},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openMigrated(t, tc.db, tc.set)

			if schema.Latest(tc.set) < 1 {
				t.Fatalf("Latest(%s) = %d", tc.set, schema.Latest(tc.set))
			}
			current, err := schema.Current(ctx, db)
			if err != nil || current != schema.Latest(tc.set) {
				t.Fatalf("Current = %d, %v; want %d", current, err, schema.Latest(tc.set))
			}
			pending, err := schema.Pending(ctx, db, tc.set)
			if err != nil || len(pending) != 0 {
				t.Fatalf("Pending = %v, %v; want none", pending, err)
			}
			again, err := schema.Apply(ctx, db, tc.set)
			if err != nil || len(again) != 0 {
				t.Fatalf("second Apply = %v, %v; want no migrations", again, err)
			}

			migrations, err := schema.Migrations(tc.set)
			if err != nil {
				t.Fatalf("Migrations: %v", err)
			}
			var rows int
			if err := db.Reader().QueryRow("SELECT count(*) FROM schema_version").Scan(&rows); err != nil {
				t.Fatalf("count schema_version: %v", err)
			}
			if rows != len(migrations) {
				t.Fatalf("schema_version rows = %d, want %d", rows, len(migrations))
			}

			assertPragmaRows(t, db, "PRAGMA quick_check", "ok")
			assertPragmaRows(t, db, "PRAGMA integrity_check", "ok")
			assertNoRows(t, db, "PRAGMA foreign_key_check")
		})
	}
}

func TestSchemaSetsAreNotInterchangeable(t *testing.T) {
	db := openMigrated(t, sqlitedb.Projection, schema.Projection)
	if _, err := schema.Pending(context.Background(), db, schema.Control); err == nil {
		t.Fatal("control migrations were accepted by a projection database")
	}
}

func assertPragmaRows(t *testing.T, db *sqlitedb.DB, query, want string) {
	t.Helper()
	var got []string
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				return err
			}
			got = append(got, value)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("%s = %v, want [%s]", query, got, want)
	}
}

func assertNoRows(t *testing.T, db *sqlitedb.DB, query string) {
	t.Helper()
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			t.Errorf("%s returned a row", query)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
