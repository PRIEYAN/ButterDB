package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

var ErrWrongDatabase = errors.New("sqlitedb: database belongs to the other set")

const privateFileMode = 0o600

type Dependencies struct {
	Clock clock.Clock
	FS    fsx.FS
}

type DB struct {
	name   Name
	path   string
	opts   Options
	prof   profile
	log    *slog.Logger
	clock  clock.Clock
	fs     fsx.FS
	writer *sql.DB
	reader *sql.DB

	closeMu  sync.Mutex
	closed   bool
	closeErr error
}

func Open(ctx context.Context, name Name, path string, opts Options, log *slog.Logger) (*DB, error) {
	return OpenWith(ctx, name, path, opts, log, Dependencies{Clock: clock.System{}, FS: fsx.OS{}})
}

func OpenWith(ctx context.Context, name Name, path string, opts Options, log *slog.Logger, deps Dependencies) (*DB, error) {
	if log == nil {
		return nil, fmt.Errorf("sqlitedb: %w: logger", errs.ErrMissingDependency)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("sqlitedb: %w: clock", errs.ErrMissingDependency)
	}
	if deps.FS == nil {
		return nil, fmt.Errorf("sqlitedb: %w: filesystem", errs.ErrMissingDependency)
	}
	prof, err := profileFor(name)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("sqlitedb: empty database path")
	}
	d := &DB{
		name:  name,
		path:  path,
		opts:  withDefaults(opts),
		prof:  prof,
		log:   log,
		clock: deps.Clock,
		fs:    deps.FS,
	}
	if err := ensurePrivateFile(d.fs, path); err != nil {
		return nil, err
	}
	if err := d.openHandles(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

func withDefaults(opts Options) Options {
	if opts.ReadConns <= 0 {
		opts.ReadConns = max(defaultReadConnsFloor, runtime.GOMAXPROCS(0))
	}
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = defaultBusyTimeout
	}
	if opts.SlowTxThreshold <= 0 {
		opts.SlowTxThreshold = defaultSlowTxThreshold
	}
	return opts
}

func (d *DB) openHandles(ctx context.Context) (retErr error) {
	writer, err := sql.Open("sqlite", writerDSN(d.path, d.prof, d.opts.BusyTimeout))
	if err != nil {
		return fmt.Errorf("sqlitedb: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)
	d.writer = writer
	defer func() {
		if retErr != nil {
			_ = writer.Close()
		}
	}()
	if err := d.prepareWriter(ctx); err != nil {
		return err
	}
	reader, err := sql.Open("sqlite", readerDSN(d.path, d.prof, d.opts.BusyTimeout))
	if err != nil {
		return fmt.Errorf("sqlitedb: open read pool: %w", err)
	}
	reader.SetMaxOpenConns(d.opts.ReadConns)
	reader.SetMaxIdleConns(d.opts.ReadConns)
	reader.SetConnMaxIdleTime(readConnMaxIdleTime)
	if err := reader.PingContext(ctx); err != nil {
		_ = reader.Close()
		return fmt.Errorf("sqlitedb: open read pool: %w", err)
	}
	d.reader = reader
	return nil
}

func (d *DB) prepareWriter(ctx context.Context) error {
	if err := d.writer.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlitedb: open writer: %w", err)
	}
	if err := d.verifyApplicationID(ctx); err != nil {
		return err
	}
	var mode string
	if err := d.writer.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return fmt.Errorf("sqlitedb: enable wal: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("sqlitedb: journal mode is %q, want wal", mode)
	}
	return nil
}

func (d *DB) verifyApplicationID(ctx context.Context) error {
	var id int
	if err := d.writer.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return fmt.Errorf("sqlitedb: read application id: %w", err)
	}
	if id != 0 && id != d.prof.applicationID {
		return fmt.Errorf("sqlitedb: %w: %s", ErrWrongDatabase, d.name)
	}
	return nil
}

func (d *DB) Name() Name {
	return d.name
}

func (d *DB) Now() time.Time {
	return d.clock.Now()
}

func (d *DB) Reader() *sql.DB {
	return d.reader
}

func (d *DB) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	caller := callerOf(2)
	tx, err := d.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlitedb: begin write: %w", err)
	}
	started := d.clock.Now()
	err = runInTx(tx, fn, true)
	d.warnIfSlow(started, caller)
	return err
}

func (d *DB) Read(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.reader.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlitedb: begin read: %w", err)
	}
	return runInTx(tx, fn, false)
}

func runInTx(tx *sql.Tx, fn func(tx *sql.Tx) error, commit bool) (retErr error) {
	finished := false
	defer func() {
		if finished {
			return
		}
		_ = tx.Rollback()
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
	}()
	if err := fn(tx); err != nil {
		finished = true
		_ = tx.Rollback()
		return err
	}
	finished = true
	if !commit {
		if err := tx.Rollback(); err != nil {
			return fmt.Errorf("sqlitedb: end read: %w", err)
		}
		return nil
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlitedb: commit: %w", err)
	}
	return nil
}

func (d *DB) warnIfSlow(started time.Time, caller string) {
	elapsed := d.clock.Now().Sub(started)
	if elapsed <= d.opts.SlowTxThreshold {
		return
	}
	d.log.Warn("sqlitedb: slow write transaction", "database", string(d.name), "duration", elapsed, "caller", caller)
}

func callerOf(skip int) string {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown"
	}
	name := "unknown"
	if fn := runtime.FuncForPC(pc); fn != nil {
		name = fn.Name()
	}
	return fmt.Sprintf("%s %s:%d", name, file, line)
}

func (d *DB) Close() error {
	d.closeMu.Lock()
	defer d.closeMu.Unlock()
	if d.closed {
		return d.closeErr
	}
	d.closed = true
	poolErr := d.reader.Close()
	writerErr := d.writer.Close()
	d.closeErr = errors.Join(wrapClose("read pool", poolErr), wrapClose("writer", writerErr), d.checkpointTruncate())
	return d.closeErr
}

func wrapClose(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sqlitedb: close %s: %w", what, err)
}

func (d *DB) checkpointTruncate() (retErr error) {
	fresh, err := sql.Open("sqlite", writerDSN(d.path, d.prof, d.opts.BusyTimeout))
	if err != nil {
		return fmt.Errorf("sqlitedb: open checkpoint connection: %w", err)
	}
	defer func() {
		if err := fresh.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("sqlitedb: close checkpoint connection: %w", err)
		}
	}()
	fresh.SetMaxOpenConns(1)
	var busy, logFrames, checkpointed int
	ctx, cancel := context.WithTimeout(context.Background(), d.opts.BusyTimeout)
	defer cancel()
	if err := fresh.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("sqlitedb: wal checkpoint: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("sqlitedb: wal checkpoint was blocked")
	}
	return nil
}

func ensurePrivateFile(fsys fsx.FS, path string) error {
	info, err := fsys.Stat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("sqlitedb: database path is a directory")
		}
		if info.Mode().Perm() != privateFileMode {
			if err := fsys.Chmod(path, privateFileMode); err != nil {
				return fmt.Errorf("sqlitedb: restrict database file: %w", err)
			}
		}
		return nil
	}
	if !isNotExist(err) {
		return fmt.Errorf("sqlitedb: stat database file: %w", err)
	}
	return createPrivateFile(fsys, path)
}
