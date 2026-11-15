package core

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/conn"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const registryWorldUser = int64(777000123)

type registryWorld struct {
	t            *testing.T
	dir          string
	clk          *clock.Fake
	network      *faketg.Network
	client       *faketg.Client
	ring         *keyring.Ring
	projectionDB *sqlitedb.DB
	controlDB    *sqlitedb.DB
	projection   *Projection
	terminal     *scriptedTerminal
	writer       *secretfile.Writer
}

func newRegistryWorld(t *testing.T) *registryWorld {
	t.Helper()
	ctx := context.Background()
	dir := privateDir(t)
	clk := clock.NewFake(time.Unix(1763000000, 0))
	network, err := faketg.NewNetwork(clk)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	client, err := network.AddAccount(faketg.AccountConfig{UserID: registryWorldUser, FirstName: "Ada", Username: "ada"})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	w := &registryWorld{t: t, dir: dir, clk: clk, network: network, client: client, terminal: &scriptedTerminal{answers: map[string][]string{}, secrets: map[string][]string{}}}
	w.ring = w.newRing("server.key")
	w.projectionDB = w.openDatabase(ctx, sqlitedb.Projection, schema.Projection, "projection.db")
	w.controlDB = w.openDatabase(ctx, sqlitedb.Control, schema.Control, "control.db")
	w.projection, err = NewProjection(ProjectionDependencies{Logger: newDiscardLogger()})
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	w.writer, err = secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	return w
}

func (w *registryWorld) newRing(name string) *keyring.Ring {
	w.t.Helper()
	path := filepath.Join(w.dir, name)
	if _, err := keyring.Generate(path, cryptorand.Reader); err != nil {
		w.t.Fatalf("generate key: %v", err)
	}
	ring, err := keyring.Load(path)
	if err != nil {
		w.t.Fatalf("load key: %v", err)
	}
	w.t.Cleanup(ring.Close)
	return ring
}

func (w *registryWorld) openDatabase(ctx context.Context, name sqlitedb.Name, set schema.Set, file string) *sqlitedb.DB {
	w.t.Helper()
	db, err := sqlitedb.Open(ctx, name, filepath.Join(w.dir, file), sqlitedb.Options{}, newDiscardLogger())
	if err != nil {
		w.t.Fatalf("open %s: %v", file, err)
	}
	w.t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(ctx, db, set); err != nil {
		w.t.Fatalf("apply %s: %v", file, err)
	}
	return db
}

func (w *registryWorld) statePath() string {
	return filepath.Join(w.dir, stateFileName)
}

func (w *registryWorld) deps() RegistrySetupDependencies {
	return RegistrySetupDependencies{
		Database:      w.projectionDB,
		Control:       w.controlDB,
		Projection:    w.projection,
		Telegram:      w.client,
		Sender:        w.client,
		Signals:       conn.NewActivity(0),
		Ring:          w.ring,
		PrimaryUserID: registryWorldUser,
		StatePath:     w.statePath(),
		Writer:        w.writer,
		Terminal:      w.terminal,
		Clock:         w.clk,
		Sleeper:       flow.SystemSleeper{},
		Random:        cryptorand.Reader,
		Logger:        newDiscardLogger(),
	}
}

func (w *registryWorld) answer(label string, values ...string) {
	w.terminal.answers[label] = append(w.terminal.answers[label], values...)
}

func (w *registryWorld) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	err := w.projectionDB.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	})
	if err != nil {
		w.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (w *registryWorld) readState() []byte {
	w.t.Helper()
	raw, err := os.ReadFile(w.statePath())
	if err != nil {
		w.t.Fatalf("read state: %v", err)
	}
	return raw
}
