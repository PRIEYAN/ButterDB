//go:build unix

package sqlitedb

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFileMode0600(t *testing.T) {
	for _, name := range []Name{Projection, Control} {
		t.Run(string(name), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, string(name)+".db")
			old := syscall.Umask(0)
			defer syscall.Umask(old)
			db, err := Open(context.Background(), name, path, Options{}, discardLogger())
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer db.Close()
			mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY) STRICT")
			mustExec(t, db, "INSERT INTO t (id) VALUES (1)")
			var rows int
			if err := db.Reader().QueryRow("SELECT count(*) FROM t").Scan(&rows); err != nil {
				t.Fatalf("read: %v", err)
			}
			for _, file := range []string{path, path + "-wal", path + "-shm"} {
				info, err := os.Stat(file)
				if err != nil {
					t.Fatalf("stat %s: %v", filepath.Base(file), err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Errorf("%s mode = %04o, want 0600", filepath.Base(file), info.Mode().Perm())
				}
			}
		})
	}
}
