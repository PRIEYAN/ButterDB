package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

func freeLoopback(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func servingDeps(t *testing.T) (Dependencies, *bytes.Buffer) {
	t.Helper()
	deps := sessionDeps(t, privateDir(t))
	deps.Config.Admin.Listen = freeLoopback(t)
	deps.Config.Admin.ControlSocket = filepath.Join(deps.Config.DataDir, "admin.sock")
	deps.Config.Cache.Dir = filepath.Join(deps.Config.DataDir, "cache")
	logs := &bytes.Buffer{}
	deps.LogOutput = logs
	deps.Clock = clock.NewFake(time.Now())
	return deps, logs
}

func TestEnsureServerKey(t *testing.T) {
	t.Run("absent key without a deployment is generated with a warning", func(t *testing.T) {
		deps, logs := servingDeps(t)
		app := startApp(t, deps)
		if err := app.ensureServerKey(); err != nil {
			t.Fatalf("ensureServerKey: %v", err)
		}
		info, err := os.Stat(deps.Config.ServerKeyFile)
		if err != nil || info.Size() != 32 || info.Mode().Perm() != 0o600 {
			t.Fatalf("generated key: %v, %v", info, err)
		}
		if !strings.Contains(logs.String(), `"msg":"server key generated"`) || !strings.Contains(logs.String(), `"kek_fingerprint":"bkf1_`) {
			t.Fatalf("log %q lacks the generation warning", logs.String())
		}
		before, _ := os.ReadFile(deps.Config.ServerKeyFile)
		if err := app.ensureServerKey(); err != nil {
			t.Fatalf("second ensureServerKey: %v", err)
		}
		after, _ := os.ReadFile(deps.Config.ServerKeyFile)
		if !bytes.Equal(before, after) {
			t.Fatal("an existing key was replaced")
		}
	})
	t.Run("absent key of an existing deployment is refused", func(t *testing.T) {
		deps, _ := servingDeps(t)
		app := startApp(t, deps)
		if err := os.WriteFile(app.DataDir().Join(stateFileName), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := app.ensureServerKey()
		if !errors.Is(err, ErrKeyOrRegistry) {
			t.Fatalf("ensureServerKey = %v, want ErrKeyOrRegistry", err)
		}
		if _, statErr := os.Stat(deps.Config.ServerKeyFile); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("a key was generated for an existing deployment: %v", statErr)
		}
	})
}

func TestStartServingKeepsAdminReachableWhenUnlinked(t *testing.T) {
	deps, logs := servingDeps(t)
	deps.Build = BuildInfo{Version: "9.9.9", Commit: "deadbee"}
	app := startApp(t, deps)
	serving, err := app.StartServing(context.Background())
	if err != nil {
		t.Fatalf("StartServing: %v", err)
	}
	if serving.Runtime != nil || serving.Backend.Current() != nil {
		t.Fatal("an unlinked deployment started a storage runtime")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get("https://" + serving.Admin.Addr() + adminv1.PathServerInfo)
	if err != nil {
		t.Fatalf("server-info: %v", err)
	}
	var info adminv1.ServerInfo
	err = json.NewDecoder(resp.Body).Decode(&info)
	_ = resp.Body.Close()
	if err != nil || info.State != "unclaimed" || info.ServerVersion != "9.9.9" || info.TelegramLinked || info.TLS.SPKISHA256 == "" {
		t.Fatalf("server info = %+v, %v", info, err)
	}
	if !strings.Contains(logs.String(), `"msg":"setup token"`) || !strings.Contains(logs.String(), info.TLS.SPKISHA256) {
		t.Fatalf("log %q lacks the setup token or the fingerprint", logs.String())
	}
	kind, token, _, err := session.RequestAdminToken(context.Background(), deps.Config.Admin.ControlSocket)
	if err != nil || kind != session.KindSetupToken || !strings.HasPrefix(token, "bdst1_") {
		t.Fatalf("control socket = %q, %q, %v", kind, token, err)
	}
	if err := serving.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(deps.Config.Admin.ControlSocket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the control socket outlived the server: %v", err)
	}
	if _, err := client.Get("https://" + deps.Config.Admin.Listen + adminv1.PathServerInfo); err == nil {
		t.Fatal("the admin listener still answers after Close")
	}
}

func TestIssueAdminTokenWithoutARunningServer(t *testing.T) {
	deps, logs := servingDeps(t)
	app := startApp(t, deps)
	first, err := app.IssueAdminToken(context.Background())
	if err != nil || first.Kind != session.KindSetupToken || !strings.HasPrefix(first.Value, "bdst1_") || !first.ExpiresAt.After(app.clock.Now()) {
		t.Fatalf("first token = %+v, %v", first, err)
	}
	if strings.Contains(logs.String(), first.Value) {
		t.Fatal("the setup token was written to the log of the admin-token command")
	}
	second, err := app.IssueAdminToken(context.Background())
	if err != nil || second.Kind != session.KindSetupToken || second.Value == first.Value {
		t.Fatalf("second token = %+v, %v; a new token must replace the first", second, err)
	}
	control, err := sqlitedb.OpenWith(context.Background(), sqlitedb.Control, app.DataDir().Join(controlDatabaseFile), sqlitedb.Options{}, newDiscardLogger(),
		sqlitedb.Dependencies{Clock: app.clock, FS: app.fsys})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Apply(context.Background(), control, schema.Control); err != nil {
		t.Fatal(err)
	}
	repo, err := adminrepo.New(control, app.clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertDevice(context.Background(), adminrepo.NewDevice{
		DeviceID: "abcdefghijklmnop", Name: "laptop", CreatedVia: adminrepo.CreatedViaSetup, CreatedAt: app.clock.Now(),
	}); err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	claimed, err := app.IssueAdminToken(context.Background())
	if err != nil || claimed.Kind != session.KindPairingCode || len(claimed.Value) != 11 || claimed.Value[5] != '-' {
		t.Fatalf("claimed token = %+v, %v", claimed, err)
	}
}
