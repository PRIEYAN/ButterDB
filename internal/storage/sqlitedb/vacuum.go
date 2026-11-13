package sqlitedb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
)

func (d *DB) VacuumInto(ctx context.Context, dstPath string) error {
	if dstPath == "" {
		return fmt.Errorf("sqlitedb: empty vacuum destination")
	}
	if _, err := d.fs.Stat(dstPath); err == nil {
		return fmt.Errorf("sqlitedb: vacuum destination already exists")
	} else if !isNotExist(err) {
		return fmt.Errorf("sqlitedb: stat vacuum destination: %w", err)
	}
	tmp, err := newPrivateTemp(d.fs, dstPath)
	if err != nil {
		return err
	}
	if err := d.vacuumIntoFile(ctx, tmp); err != nil {
		_ = d.fs.Remove(tmp)
		return err
	}
	if err := d.fs.Rename(tmp, dstPath); err != nil {
		_ = d.fs.Remove(tmp)
		return fmt.Errorf("sqlitedb: publish vacuum copy: %w", err)
	}
	if err := d.fs.SyncDir(filepath.Dir(dstPath)); err != nil {
		return fmt.Errorf("sqlitedb: sync vacuum directory: %w", err)
	}
	return nil
}

func (d *DB) vacuumIntoFile(ctx context.Context, target string) (retErr error) {
	conn, err := d.reader.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlitedb: vacuum connection: %w", err)
	}
	queryOnlyOff := false
	defer func() {
		if err := finishVacuumConn(conn, queryOnlyOff); err != nil && retErr == nil {
			retErr = err
		}
	}()
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only=0"); err != nil {
		return fmt.Errorf("sqlitedb: enable vacuum: %w", err)
	}
	queryOnlyOff = true
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		return fmt.Errorf("sqlitedb: vacuum into: %w", err)
	}
	return nil
}

func finishVacuumConn(conn *sql.Conn, queryOnlyOff bool) error {
	if queryOnlyOff {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA query_only=1"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			_ = conn.Close()
			return fmt.Errorf("sqlitedb: restore query only: %w", err)
		}
	}
	return conn.Close()
}
