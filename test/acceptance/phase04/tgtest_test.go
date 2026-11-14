//go:build tgtest

package phase04

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
)

const (
	testDCNumber    = 1
	testPhoneSuffix = "0001"
	smokeBodyBytes  = 4096
)

func smokeCredentials(t *testing.T) (int, string) {
	t.Helper()
	if os.Getenv("BUTTERDB_TGTEST") != "1" {
		t.Skip("telegram test-dc smoke test is gated: set BUTTERDB_TGTEST=1")
	}
	rawID, hash := os.Getenv("BUTTERDB_TGTEST_API_ID"), os.Getenv("BUTTERDB_TGTEST_API_HASH")
	if rawID == "" || hash == "" {
		t.Skip("telegram test-dc smoke test needs BUTTERDB_TGTEST_API_ID and BUTTERDB_TGTEST_API_HASH")
	}
	apiID, err := strconv.Atoi(rawID)
	if err != nil {
		t.Skipf("BUTTERDB_TGTEST_API_ID is not a number: %v", err)
	}
	return apiID, hash
}

func smokeSession(t *testing.T) *core.TelegramSession {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.ServerKeyFile = filepath.Join(dataDir, "secrets", "server.key")
	cfg.Telegram.AppFile = filepath.Join(dataDir, "secrets", "telegram_app.json")
	cfg.Telegram.SessionFile = filepath.Join(dataDir, "secrets", "session_primary.json")
	cfg.Telegram.ReplicaSessionFile = filepath.Join(dataDir, "secrets", "session_replica.json")
	cfg.Telegram.TestDC = true
	if _, err := keyring.Generate(cfg.ServerKeyFile, cryptorand.Reader); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	app, err := core.New(core.Dependencies{Config: cfg, Role: "tgtest", LogOutput: os.Stderr, Clock: clock.System{}, FS: fsx.OS{}})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("app.Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	session, err := app.OpenTelegram(ctx, identity.RolePrimary)
	if err != nil {
		t.Fatalf("OpenTelegram: %v", err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session
}

func TestTelegramTestDCSmoke(t *testing.T) {
	apiID, apiHash := smokeCredentials(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session := smokeSession(t)

	if err := session.Identity().SaveCredentials(ctx, apiID, apiHash); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	phone := "+99966" + strconv.Itoa(testDCNumber) + testPhoneSuffix
	snap, err := session.Identity().Start(ctx, phone)
	if err != nil || snap.State != identity.StateAwaitingCode {
		t.Fatalf("Start = %+v, %v; want awaiting_code", snap, err)
	}
	code := bytes.Repeat([]byte(strconv.Itoa(testDCNumber)), 5)
	snap, err = session.Identity().SubmitCode(ctx, snap.Login.LoginID, string(code))
	if err != nil || snap.State != identity.StateLinked {
		t.Fatalf("SubmitCode = %+v, %v; want linked", snap, err)
	}

	transport, err := session.Transport(ctx)
	if err != nil {
		t.Fatalf("Transport: %v", err)
	}
	peer, err := transport.Channels.CreateChannel(ctx, "butterDB smoke", "phase 04 smoke test")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	controlID, err := send.StableRandomID("smoke-job", "control")
	if err != nil {
		t.Fatalf("random id: %v", err)
	}
	controlMsg, err := transport.Sender.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: peer, Text: "TDX1|t=smoke", RandomID: controlID})
	if err != nil || controlMsg == 0 {
		t.Fatalf("SendControlIdempotent = %d, %v", controlMsg, err)
	}
	bodyID, err := send.StableRandomID("smoke-job", "body")
	if err != nil {
		t.Fatalf("random id: %v", err)
	}
	body := patternedBytes(smokeBodyBytes)
	sent, err := transport.Sender.SendFileIdempotent(ctx, tgclient.SendFileRequest{
		Peer: peer, Caption: "TDX1|t=body|j=smoke-job", FileName: "smoke.bin", Size: smokeBodyBytes, Body: bytes.NewReader(body), RandomID: bodyID,
	})
	if err != nil || sent.MsgID == 0 {
		t.Fatalf("SendFileIdempotent = %+v, %v", sent, err)
	}
	ref, err := transport.Reader.ResolveDocument(ctx, peer, sent.MsgID)
	if err != nil {
		t.Fatalf("ResolveDocument: %v", err)
	}
	got := make([]byte, smokeBodyBytes)
	if n, err := transport.Reader.ReadDocumentRange(ctx, ref, 0, got); err != nil || n != smokeBodyBytes || !bytes.Equal(got, body) {
		t.Fatalf("ReadDocumentRange = %d, %v; bytes equal = %v", n, err, bytes.Equal(got, body))
	}
	if err := transport.Channels.DeleteMessages(ctx, peer, []int64{controlMsg, sent.MsgID}); err != nil {
		t.Fatalf("DeleteMessages: %v", err)
	}
}
