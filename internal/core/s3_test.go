package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

func completeS3Dependencies() S3Dependencies {
	return S3Dependencies{
		Config: config.Default(), Control: &sqlitedb.DB{}, Database: &sqlitedb.DB{}, Ring: &keyring.Ring{},
		Write: &Write{}, Read: &Read{}, Clock: clock.NewFake(time.Unix(1763000000, 0)), Random: zeroReader{}, Logger: newDiscardLogger(),
	}
}

func TestNewS3RejectsMissingDependencies(t *testing.T) {
	tests := []struct {
		name   string
		remove func(*S3Dependencies)
	}{
		{name: "control database", remove: func(d *S3Dependencies) { d.Control = nil }},
		{name: "projection database", remove: func(d *S3Dependencies) { d.Database = nil }},
		{name: "server key", remove: func(d *S3Dependencies) { d.Ring = nil }},
		{name: "write", remove: func(d *S3Dependencies) { d.Write = nil }},
		{name: "read", remove: func(d *S3Dependencies) { d.Read = nil }},
		{name: "clock", remove: func(d *S3Dependencies) { d.Clock = nil }},
		{name: "random", remove: func(d *S3Dependencies) { d.Random = nil }},
		{name: "logger", remove: func(d *S3Dependencies) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := completeS3Dependencies()
			tt.remove(&deps)
			if _, err := NewS3(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewS3 = %v, want ErrMissingDependency", err)
			}
		})
	}
}

func TestListensOnLoopback(t *testing.T) {
	tests := []struct {
		listen string
		want   bool
	}{
		{listen: "127.0.0.1:9000", want: true},
		{listen: "[::1]:9000", want: true},
		{listen: "localhost:9000", want: true},
		{listen: "0.0.0.0:9000", want: false},
		{listen: ":9000", want: false},
		{listen: "10.1.2.3:9000", want: false},
		{listen: "not an address", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.listen, func(t *testing.T) {
			if got := listensOnLoopback(tt.listen); got != tt.want {
				t.Fatalf("listensOnLoopback(%q) = %v, want %v", tt.listen, got, tt.want)
			}
		})
	}
}

func TestPublicHosts(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want []string
	}{
		{name: "empty", url: "", want: nil},
		{name: "https with port", url: "https://s3.example.com:8443", want: []string{"s3.example.com:8443"}},
		{name: "plain host", url: "https://s3.example.com", want: []string{"s3.example.com"}},
		{name: "unparseable", url: "http://[::1", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := publicHosts(tt.url)
			if len(got) != len(tt.want) || (len(got) == 1 && got[0] != tt.want[0]) {
				t.Fatalf("publicHosts(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestS3PrincipalAttacher(t *testing.T) {
	tests := []struct {
		name        string
		accessKeyID string
		wantScope   string
		wantErr     error
	}{
		{name: "s3 access key", accessKeyID: "BDBK7Q2M9X4T1W8Z3R6Y", wantScope: "key:BDBK7Q2M9X4T1W8Z3R6Y"},
		{name: "empty caller", accessKeyID: "", wantErr: errs.ErrAccessDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := s3PrincipalAttacher{}.WithPrincipal(context.Background(), tt.accessKeyID, "req-1")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("WithPrincipal error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			principal, err := principalContext{}.Principal(ctx)
			if err != nil || principal.Scope != tt.wantScope || principal.Origin != jobrepo.OriginS3 || principal.RequestID != "req-1" {
				t.Fatalf("principal = %+v, %v", principal, err)
			}
		})
	}
}

func TestS3ServerStartsServesAndCloses(t *testing.T) {
	cfg := config.Default().S3
	cfg.Listen = "127.0.0.1:0"
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })
	server := newS3Server(cfg, handler, newDiscardLogger())
	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	resp, err := http.Get("http://" + server.addr() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "pong" {
		t.Fatalf("body = %q", body)
	}
	if err := server.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if server.addr() != "" {
		t.Fatalf("addr after Close = %q", server.addr())
	}
	if _, err := http.Get("http://" + resp.Request.URL.Host + "/"); err == nil {
		t.Fatal("listener still accepts after Close")
	}
}

func TestS3ReadinessStartsStartingAndFollowsMarks(t *testing.T) {
	s := &S3{Readiness: newS3Readiness()}
	if got := s.Readiness.State(); got != router.StateStarting {
		t.Fatalf("initial state = %v, want starting", got)
	}
	s.MarkReady()
	if got := s.Readiness.State(); got != router.StateReady {
		t.Fatalf("after MarkReady = %v, want ready", got)
	}
	s.MarkDegraded()
	if got := s.Readiness.State(); got != router.StateDegraded {
		t.Fatalf("after MarkDegraded = %v, want degraded", got)
	}
}
