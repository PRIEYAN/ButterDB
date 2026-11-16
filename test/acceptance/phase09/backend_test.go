package phase09

import (
	"context"
	"io"
	"net/http/httptest"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/conn"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

type answerTerminal struct{}

func (answerTerminal) Prompt(string) (string, error)       { return "yes", nil }
func (answerTerminal) PromptSecret(string) (string, error) { return "", io.EOF }
func (answerTerminal) Say(string)                          {}

type storageBackend struct {
	sync  *core.Sync
	write *core.Write
	read  *core.Read
	s3    *core.S3
	srv   *httptest.Server
}

func (w *world) publishBackend() *storageBackend {
	w.t.Helper()
	ctx := context.Background()
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		w.t.Fatal(err)
	}
	projection, err := core.NewProjection(core.ProjectionDependencies{Logger: discardLogger()})
	if err != nil {
		w.t.Fatalf("projection: %v", err)
	}
	activity := conn.NewActivity(0)
	statePath := w.dir + "/state.json"
	outcome, err := core.SetupRegistry(ctx, core.RegistrySetupDependencies{
		Database: w.db, Control: w.control, Projection: projection, Telegram: w.telegram, Sender: w.telegram,
		Signals: activity, Ring: w.ring, PrimaryUserID: primaryUser, StatePath: statePath, Writer: writer,
		Terminal: answerTerminal{}, Clock: w.clk, Sleeper: flow.SystemSleeper{}, Random: w.random, Logger: discardLogger(),
	}, core.RegistryOptions{})
	if err != nil || !outcome.Created {
		w.t.Fatalf("setup registry = %+v, %v", outcome, err)
	}
	state, err := core.NewRegistryStateStore(statePath, writer, w.control, w.random)
	if err != nil {
		w.t.Fatalf("state store: %v", err)
	}
	built := &storageBackend{}
	if built.sync, err = core.NewSync(core.SyncDependencies{
		Database: w.db, Control: w.control, Projection: projection, Telegram: w.telegram, Sender: w.telegram, Signals: activity,
		Ring: w.ring, State: state, ActorUserID: primaryUser, DeploymentID: outcome.DeploymentID, Clock: w.clk,
		Sleeper: flow.SystemSleeper{}, Random: w.random, Logger: discardLogger(),
	}); err != nil {
		w.t.Fatalf("new sync: %v", err)
	}
	flowControl, err := core.NewFlowControl(core.FlowDependencies{
		SendRate: w.cfg.Telegram.SendRate, Clock: w.clk, Sleeper: w.sleeper, Random: w.random, Logger: discardLogger(),
	})
	if err != nil {
		w.t.Fatalf("flow control: %v", err)
	}
	data, err := datadir.Open(fsx.OS{}, w.dir)
	if err != nil {
		w.t.Fatalf("data dir: %v", err)
	}
	if built.write, err = core.NewWrite(core.WriteDependencies{
		Config: w.cfg, Data: data, Control: w.control, Database: w.db, Projection: projection, Sync: built.sync,
		Telegram: w.telegram, Sender: w.telegram, Prepaced: w.telegram, Pacer: flowControl.Limiter, Ring: w.ring,
		ActorUserID: primaryUser, FS: fsx.OS{}, Clock: w.clk, Sleeper: w.sleeper, Random: w.random, Logger: discardLogger(),
	}); err != nil {
		w.t.Fatalf("new write: %v", err)
	}
	w.cfg.Cache.Dir = w.t.TempDir()
	if built.read, err = core.NewRead(core.ReadDependencies{
		Config: w.cfg, Database: w.db, Sync: built.sync, Telegram: w.telegram, Origin: w.telegram, Ring: w.ring,
		Files: fsx.OS{}, Clock: w.clk, Sleeper: w.sleeper, Random: w.random, Logger: discardLogger(),
	}); err != nil {
		w.t.Fatalf("new read: %v", err)
	}
	if err := built.read.Start(ctx); err != nil {
		w.t.Fatalf("start read: %v", err)
	}
	w.t.Cleanup(func() { _ = built.read.Close(context.Background()) })
	w.publish(built)
	return built
}

func (w *world) publish(built *storageBackend) {
	w.t.Helper()
	var err error
	built.s3, err = core.NewS3(core.S3Dependencies{
		Config: w.cfg, Control: w.control, Database: w.db, Ring: w.ring, Write: built.write, Read: built.read,
		Clock: w.clk, Random: w.random, Logger: discardLogger(), Keys: w.keys,
	})
	if err != nil {
		w.t.Fatalf("new s3: %v", err)
	}
	built.s3.MarkReady()
	built.srv = httptest.NewServer(built.s3.Handler())
	w.t.Cleanup(built.srv.Close)
	w.slot.Publish(&core.Backend{Sync: built.sync, Write: built.write, Read: built.read, Database: w.db, Readiness: built.s3.Readiness})
}
