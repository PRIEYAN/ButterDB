package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func TestOptionsRejectMissingDependencies(t *testing.T) {
	h := newHarness(t)
	full := h.opts
	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"no clock", func(o *Options) { o.Now = nil }},
		{"no engine", func(o *Options) { o.Engine = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := full
			tc.mutate(&opts)
			if _, err := Build(h.app, h.app.NewWindow("x"), opts); !errors.Is(err, ErrMissingDependency) {
				t.Fatalf("Build error %v, want ErrMissingDependency", err)
			}
			if err := Run(opts); !errors.Is(err, ErrMissingDependency) {
				t.Fatalf("Run error %v, want ErrMissingDependency", err)
			}
		})
	}
	if _, err := Build(nil, h.app.NewWindow("x"), full); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("Build without an app: %v", err)
	}
	if _, err := Build(h.app, nil, full); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("Build without a window: %v", err)
	}
}

type recordingTelegram struct {
	mu    sync.Mutex
	calls []string
	reqs  []any
}

func (r *recordingTelegram) record(name string, req any) adminv1.TelegramStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
	r.reqs = append(r.reqs, req)
	return adminv1.TelegramStatus{State: adminv1.TelegramReady}
}

func (r *recordingTelegram) TelegramStatus(context.Context) (adminv1.TelegramStatus, error) {
	return r.record("TelegramStatus", nil), nil
}

func (r *recordingTelegram) SaveTelegramCredentials(_ context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error) {
	return r.record("SaveTelegramCredentials", req), nil
}

func (r *recordingTelegram) DeleteTelegramCredentials(context.Context) (adminv1.TelegramStatus, error) {
	return r.record("DeleteTelegramCredentials", nil), nil
}

func (r *recordingTelegram) StartTelegramLogin(_ context.Context, req adminv1.TelegramLoginStartRequest) (adminv1.TelegramStatus, error) {
	return r.record("StartTelegramLogin", req), nil
}

func (r *recordingTelegram) SubmitTelegramCode(_ context.Context, req adminv1.TelegramCodeRequest) (adminv1.TelegramStatus, error) {
	return r.record("SubmitTelegramCode", req), nil
}

func (r *recordingTelegram) SubmitTelegramPassword(_ context.Context, req adminv1.TelegramPasswordRequest) (adminv1.TelegramStatus, error) {
	return r.record("SubmitTelegramPassword", req), nil
}

func (r *recordingTelegram) CancelTelegramLogin(context.Context) (adminv1.TelegramStatus, error) {
	return r.record("CancelTelegramLogin", nil), nil
}

func TestPrimaryFlowMapsEachCallToTheTelegramMethods(t *testing.T) {
	api := &recordingTelegram{}
	flow, err := newPrimaryFlow(api)
	if err != nil {
		t.Fatalf("newPrimaryFlow: %v", err)
	}
	ctx := context.Background()
	steps := []struct {
		name string
		call func() error
		want any
	}{
		{"TelegramStatus", func() error { _, err := flow.Status(ctx); return err }, nil},
		{"SaveTelegramCredentials", func() error {
			_, err := flow.SaveApp(ctx, adminv1.TelegramCredentialsRequest{APIID: 7, APIHash: testAPIHash})
			return err
		}, adminv1.TelegramCredentialsRequest{APIID: 7, APIHash: testAPIHash}},
		{"DeleteTelegramCredentials", func() error { _, err := flow.DeleteApp(ctx); return err }, nil},
		{"StartTelegramLogin", func() error { _, err := flow.Start(ctx, "+447700900134"); return err }, adminv1.TelegramLoginStartRequest{Phone: "+447700900134"}},
		{"SubmitTelegramCode", func() error { _, err := flow.SubmitCode(ctx, "login-1", "12345"); return err }, adminv1.TelegramCodeRequest{LoginID: "login-1", Code: "12345"}},
		{"SubmitTelegramPassword", func() error { _, err := flow.SubmitPassword(ctx, "login-1", "hunter2"); return err }, adminv1.TelegramPasswordRequest{LoginID: "login-1", Password: "hunter2"}},
		{"CancelTelegramLogin", func() error { _, err := flow.Cancel(ctx); return err }, nil},
	}
	for i, step := range steps {
		if err := step.call(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if api.calls[i] != step.name || api.reqs[i] != step.want {
			t.Fatalf("step %d called %s with %v, want %s with %v", i, api.calls[i], api.reqs[i], step.name, step.want)
		}
	}
	if _, err := newPrimaryFlow(nil); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("newPrimaryFlow(nil) = %v, want ErrMissingDependency", err)
	}
}

type stubSessionAPI struct {
	sessionAPI
	err error
}

func (s stubSessionAPI) ListBuckets(context.Context) (adminv1.BucketList, error) {
	return adminv1.BucketList{}, s.err
}

func (s stubSessionAPI) DeleteBucket(context.Context, string) error { return s.err }

func TestGuardedClientReportsSessionErrorsOnly(t *testing.T) {
	apiError := func(code adminv1.Code) error {
		return &adminv1.APIError{Status: 400, Body: adminv1.ErrorBody{Code: code}}
	}
	cases := []struct {
		name      string
		err       error
		wantCalls int
	}{
		{"no error", nil, 0},
		{"unauthenticated", apiError(adminv1.CodeUnauthenticated), 1},
		{"wrapped unauthenticated", fmt.Errorf("call: %w", apiError(adminv1.CodeUnauthenticated)), 1},
		{"telegram not linked", apiError(adminv1.CodeTelegramNotLinked), 1},
		{"device revoked", apiError(adminv1.CodeDeviceRevoked), 0},
		{"bucket not found", apiError(adminv1.CodeBucketNotFound), 0},
		{"rate limited", apiError(adminv1.CodeRateLimited), 0},
		{"transport error", errors.New("connection reset"), 0},
		{"context cancelled", context.Canceled, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []error
			guarded, err := newGuardedClient(stubSessionAPI{err: tc.err}, func(err error) { seen = append(seen, err) })
			if err != nil {
				t.Fatalf("newGuardedClient: %v", err)
			}
			if _, got := guarded.ListBuckets(context.Background()); !errors.Is(got, tc.err) {
				t.Fatalf("ListBuckets error %v, want %v passed through", got, tc.err)
			}
			if got := guarded.DeleteBucket(context.Background(), "photos"); !errors.Is(got, tc.err) {
				t.Fatalf("DeleteBucket error %v, want %v passed through", got, tc.err)
			}
			if len(seen) != tc.wantCalls*2 {
				t.Fatalf("handler ran %d times, want %d", len(seen), tc.wantCalls*2)
			}
		})
	}
}

func TestGuardedClientRejectsNilDependencies(t *testing.T) {
	if _, err := newGuardedClient(nil, func(error) {}); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("nil upstream: %v", err)
	}
	if _, err := newGuardedClient(stubSessionAPI{}, nil); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("nil handler: %v", err)
	}
}
