package sqlitedb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openTemp(t *testing.T, name Name, opts Options) *DB {
	t.Helper()
	db, err := Open(context.Background(), name, filepath.Join(t.TempDir(), string(name)+".db"), opts, discardLogger())
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db *DB, statement string, args ...any) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(statement, args...)
		return err
	})
	if err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
}

type pragmaWant struct {
	pragma string
	want   int64
}

func pragmaValue(t *testing.T, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, pragma string) int64 {
	t.Helper()
	var got int64
	if err := q.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&got); err != nil {
		t.Fatalf("pragma %s: %v", pragma, err)
	}
	return got
}

func TestPragmasOnEveryConnection(t *testing.T) {
	cases := []struct {
		name   Name
		reader []pragmaWant
		writer []pragmaWant
	}{
		{
			name: Projection,
			reader: []pragmaWant{
				{"synchronous", 1}, {"busy_timeout", 5000}, {"foreign_keys", 1}, {"temp_store", 2},
				{"cache_size", -32768}, {"mmap_size", 134217728}, {"query_only", 1},
			},
			writer: []pragmaWant{
				{"synchronous", 1}, {"busy_timeout", 5000}, {"foreign_keys", 1}, {"temp_store", 2},
				{"cache_size", -32768}, {"journal_size_limit", 67108864}, {"query_only", 0},
			},
		},
		{
			name: Control,
			reader: []pragmaWant{
				{"synchronous", 2}, {"busy_timeout", 5000}, {"foreign_keys", 1}, {"temp_store", 2},
				{"cache_size", -8192}, {"mmap_size", 0}, {"query_only", 1},
			},
			writer: []pragmaWant{
				{"synchronous", 2}, {"busy_timeout", 5000}, {"foreign_keys", 1}, {"temp_store", 2},
				{"cache_size", -8192}, {"journal_size_limit", 16777216}, {"query_only", 0},
			},
		},
	}
	const poolSize = 12
	const connectionsChecked = 10
	for _, tc := range cases {
		t.Run(string(tc.name), func(t *testing.T) {
			ctx := context.Background()
			db := openTemp(t, tc.name, Options{ReadConns: poolSize})
			conns := make([]*sql.Conn, 0, connectionsChecked)
			for i := 0; i < connectionsChecked; i++ {
				conn, err := db.Reader().Conn(ctx)
				if err != nil {
					t.Fatalf("conn %d: %v", i+1, err)
				}
				conns = append(conns, conn)
			}
			t.Cleanup(func() {
				for _, conn := range conns {
					_ = conn.Close()
				}
			})
			for i, conn := range conns {
				for _, want := range tc.reader {
					if got := pragmaValue(t, conn, want.pragma); got != want.want {
						t.Errorf("reader connection %d: %s = %d, want %d", i+1, want.pragma, got, want.want)
					}
				}
			}
			err := db.Write(ctx, func(tx *sql.Tx) error {
				for _, want := range tc.writer {
					if got := pragmaValue(t, tx, want.pragma); got != want.want {
						t.Errorf("writer: %s = %d, want %d", want.pragma, got, want.want)
					}
				}
				var mode string
				if err := tx.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
					return err
				}
				if mode != "wal" {
					t.Errorf("writer journal_mode = %q, want wal", mode)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("write: %v", err)
			}
		})
	}
}

func TestPoolIsQueryOnly(t *testing.T) {
	db := openTemp(t, Projection, Options{})
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY) STRICT")
	if _, err := db.Reader().ExecContext(context.Background(), "INSERT INTO t (id) VALUES (1)"); err == nil {
		t.Fatal("insert through the read pool succeeded")
	}
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO t (id) VALUES (2)")
		return err
	})
	if err == nil {
		t.Fatal("insert in a read transaction succeeded")
	}
	var rows int
	if err := db.Reader().QueryRow("SELECT count(*) FROM t").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows = %d, err = %v, want 0 rows", rows, err)
	}
}

func TestReadsDoNotWaitForWriter(t *testing.T) {
	db := openTemp(t, Projection, Options{})
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY) STRICT")
	mustExec(t, db, "INSERT INTO t (id) VALUES (1)")

	inWrite := make(chan struct{})
	release := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- db.Write(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec("INSERT INTO t (id) VALUES (2)"); err != nil {
				return err
			}
			close(inWrite)
			<-release
			return nil
		})
	}()
	<-inWrite

	readDone := make(chan int, 1)
	go func() {
		var rows int
		err := db.Read(context.Background(), func(tx *sql.Tx) error {
			return tx.QueryRow("SELECT count(*) FROM t").Scan(&rows)
		})
		if err != nil {
			rows = -1
		}
		readDone <- rows
	}()
	select {
	case rows := <-readDone:
		if rows != 1 {
			t.Errorf("read saw %d rows, want the 1 committed row", rows)
		}
	case <-time.After(5 * time.Second):
		t.Error("read blocked behind the open write transaction")
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestVacuumIntoConsistent(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t, Projection, Options{})
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT NOT NULL) STRICT")
	for i := 1; i <= 50; i++ {
		mustExec(t, db, "INSERT INTO t (id, v) VALUES (?, ?)", i, strings.Repeat("x", i))
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := db.VacuumInto(ctx, dst); err != nil {
		t.Fatalf("vacuum into: %v", err)
	}
	mustExec(t, db, "INSERT INTO t (id, v) VALUES (51, 'later')")

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat copy: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("copy mode = %04o, want 0600", info.Mode().Perm())
	}
	copyDB, err := Open(ctx, Projection, dst, Options{}, discardLogger())
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	defer copyDB.Close()
	var check string
	if err := copyDB.Reader().QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("quick_check = %q, err = %v", check, err)
	}
	var rows int
	if err := copyDB.Reader().QueryRow("SELECT count(*) FROM t").Scan(&rows); err != nil || rows != 50 {
		t.Fatalf("copy rows = %d, err = %v, want 50", rows, err)
	}
	if got := pragmaValue(t, db.Reader(), "query_only"); got != 1 {
		t.Errorf("pool query_only = %d after vacuum, want 1", got)
	}
	if err := db.VacuumInto(ctx, dst); err == nil {
		t.Error("vacuum into an existing file succeeded")
	}
}

func TestVacuumIntoLeavesPoolQueryOnly(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t, Control, Options{ReadConns: 1})
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY) STRICT")
	if err := db.VacuumInto(ctx, filepath.Join(t.TempDir(), "a.db")); err != nil {
		t.Fatalf("vacuum into: %v", err)
	}
	if _, err := db.Reader().ExecContext(ctx, "INSERT INTO t (id) VALUES (1)"); err == nil {
		t.Fatal("pool accepted a write after vacuum into")
	}
}

func TestOpenTightensLooseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projection.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), Projection, path, Options{}, discardLogger())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestOpenWrongApplicationID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")
	control, err := Open(ctx, Control, path, Options{}, discardLogger())
	if err != nil {
		t.Fatalf("open control: %v", err)
	}
	mustExec(t, control, "PRAGMA application_id = 1650745923")
	if err := control.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := Open(ctx, Projection, path, Options{}, discardLogger()); !errors.Is(err, ErrWrongDatabase) {
		t.Fatalf("open as projection: err = %v, want ErrWrongDatabase", err)
	}
	reopened, err := Open(ctx, Control, path, Options{}, discardLogger())
	if err != nil {
		t.Fatalf("reopen control: %v", err)
	}
	reopened.Close()
}

func TestOpenRejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	cases := []struct {
		name string
		call func() error
		want error
	}{
		{"nil logger", func() error {
			_, err := Open(context.Background(), Projection, path, Options{}, nil)
			return err
		}, errs.ErrMissingDependency},
		{"nil clock", func() error {
			_, err := OpenWith(context.Background(), Projection, path, Options{}, discardLogger(), Dependencies{FS: fsx.OS{}})
			return err
		}, errs.ErrMissingDependency},
		{"nil filesystem", func() error {
			_, err := OpenWith(context.Background(), Projection, path, Options{}, discardLogger(), Dependencies{Clock: clock.System{}})
			return err
		}, errs.ErrMissingDependency},
		{"unknown name", func() error {
			_, err := Open(context.Background(), Name("other"), path, Options{}, discardLogger())
			return err
		}, nil},
		{"empty path", func() error {
			_, err := Open(context.Background(), Projection, "", Options{}, discardLogger())
			return err
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("open succeeded")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestWriteRollsBackOnErrorAndPanic(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t, Projection, Options{})
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY) STRICT")
	boom := errors.New("boom")
	err := db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO t (id) VALUES (1)"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_ = db.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.Exec("INSERT INTO t (id) VALUES (2)"); err != nil {
				return err
			}
			panic("stop")
		})
	}()
	var rows int
	if err := db.Reader().QueryRow("SELECT count(*) FROM t").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows = %d, err = %v, want 0", rows, err)
	}
	mustExec(t, db, "INSERT INTO t (id) VALUES (3)")
}

func TestWritesAreSerialised(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t, Projection, Options{})
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER NOT NULL) STRICT")
	mustExec(t, db, "INSERT INTO t (id, n) VALUES (1, 0)")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := db.Write(ctx, func(tx *sql.Tx) error {
				var n int
				if err := tx.QueryRow("SELECT n FROM t WHERE id = 1").Scan(&n); err != nil {
					return err
				}
				_, err := tx.Exec("UPDATE t SET n = ? WHERE id = 1", n+1)
				return err
			})
			if err != nil {
				t.Errorf("write: %v", err)
			}
		}()
	}
	wg.Wait()
	var n int
	if err := db.Reader().QueryRow("SELECT n FROM t WHERE id = 1").Scan(&n); err != nil || n != 20 {
		t.Fatalf("n = %d, err = %v, want 20", n, err)
	}
}

func TestSlowWriteLogsWarning(t *testing.T) {
	cases := []struct {
		name     string
		advance  time.Duration
		wantWarn bool
	}{
		{"under threshold", 100 * time.Millisecond, false},
		{"at threshold", 250 * time.Millisecond, false},
		{"over threshold", 251 * time.Millisecond, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			fake := clock.NewFake(time.Unix(1700000000, 0))
			db, err := OpenWith(context.Background(), Projection, filepath.Join(t.TempDir(), "p.db"), Options{}, logger, Dependencies{Clock: fake, FS: fsx.OS{}})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer db.Close()
			err = db.Write(context.Background(), func(tx *sql.Tx) error {
				fake.Advance(tc.advance)
				return nil
			})
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			warned := strings.Contains(logs.String(), "slow write transaction")
			if warned != tc.wantWarn {
				t.Fatalf("warned = %v, want %v; log: %s", warned, tc.wantWarn, logs.String())
			}
			if warned && !strings.Contains(logs.String(), "sqlitedb_test.go") {
				t.Errorf("warning lacks the caller: %s", logs.String())
			}
		})
	}
}

func TestCloseIsIdempotentAndTruncatesWal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := Open(context.Background(), Projection, path, Options{}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY) STRICT")
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
		t.Errorf("wal size = %d after close, want 0", info.Size())
	}
}

func TestPathWithURICharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a?b#c%d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), Control, filepath.Join(dir, "control.db"), Options{}, discardLogger())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY) STRICT")
	if _, err := os.Stat(filepath.Join(dir, "control.db")); err != nil {
		t.Fatalf("database file missing: %v", err)
	}
}
