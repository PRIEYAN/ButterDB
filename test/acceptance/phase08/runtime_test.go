package phase08

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

func (w *world) startRuntime() *core.Runtime {
	w.t.Helper()
	cfg := config.Default()
	cfg.S3.Listen = "127.0.0.1:0"
	cfg.Cache.Dir = w.t.TempDir()
	cfg.Telegram.SendRate = config.SendRateConfig{ControlPerMinute: 600, ControlBurst: 100, DocumentsPerMinute: 120, DocumentsBurst: 20}
	flowControl, err := core.NewFlowControl(core.FlowDependencies{
		SendRate: cfg.Telegram.SendRate, Clock: w.clk, Sleeper: w.sleeper, Random: w.random, Logger: discardLogger(),
	})
	if err != nil {
		w.t.Fatalf("flow control: %v", err)
	}
	data, err := datadir.Open(fsx.OS{}, w.dir)
	if err != nil {
		w.t.Fatalf("data dir: %v", err)
	}
	state, err := core.NewRegistryStateStore(w.statePath(), w.writer, w.control, w.random)
	if err != nil {
		w.t.Fatalf("state store: %v", err)
	}
	runtime, err := core.NewRuntime(core.RuntimeDependencies{
		Config: cfg, Data: data, Control: w.control, Database: w.db, Ring: w.ring, State: state,
		Telegram: w.client, Sender: w.client, Prepaced: w.client, Origin: w.client, Signals: w.activity,
		Pacer: flowControl.Limiter, ActorUserID: primaryUser, DeploymentID: w.deployment,
		FS: fsx.OS{}, Files: fsx.OS{}, Clock: w.clk, Sleeper: flow.SystemSleeper{}, Random: cryptorand.Reader, Logger: discardLogger(),
	})
	if err != nil {
		w.t.Fatalf("new runtime: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		w.t.Fatalf("start runtime: %v", err)
	}
	w.t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	return runtime
}

func TestRuntimeServesS3OnItsListenerAfterInitialSync(t *testing.T) {
	w := newWorld(t)
	w.createBucket("photos", "none")
	runtime := w.startRuntime()
	endpoint := "http://" + runtime.S3.Addr()
	scope, err := access.NewScope([]string{access.AllBucketsWildcard}, []string{"read", "list", "write", "delete"})
	if err != nil {
		t.Fatal(err)
	}
	info, secret, err := runtime.S3.Keys.Create(context.Background(), "runtime", scope)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	client := s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(info.ID.String(), secret, ""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	waitUntilReady(t, endpoint)

	ctx := context.Background()
	body := []byte("served by the runtime listener")
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("photos"), Key: aws.String("hello.txt"), Body: bytes.NewReader(body)}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("hello.txt")})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	data, _ := io.ReadAll(got.Body)
	_ = got.Body.Close()
	if !bytes.Equal(data, body) {
		t.Fatalf("GetObject body = %q", data)
	}
}

func waitUntilReady(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint + "/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("runtime listener never left the starting state")
}
