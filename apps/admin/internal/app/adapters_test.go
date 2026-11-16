package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/credstore"
)

func TestProbeKeychainSelectsKeychainWhenItWorks(t *testing.T) {
	keyring.MockInit()
	keychain := credstore.NewKeychain()

	got := ProbeKeychain(keychain)
	if got != model.TokenStore(keychain) || !keychainUsable(got) {
		t.Fatalf("a working keychain was not selected: %T", got)
	}
	if _, err := keyring.Get(credstore.KeychainService, probeAccount); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("probe entry left in the keychain: %v", err)
	}
}

func TestProbeKeychainFallsBackWhenUnavailable(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrUnsupportedPlatform)
	t.Cleanup(keyring.MockInit)

	got := ProbeKeychain(credstore.NewKeychain())
	if keychainUsable(got) {
		t.Fatalf("an unavailable keychain was selected: %T", got)
	}
	if got.Kind() != model.TokenKeychain {
		t.Fatalf("fallback kind %q, want keychain so the vm asks for a fallback", got.Kind())
	}
	if err := got.Set("server", "token"); !errors.Is(err, model.ErrKeychainUnavailable) {
		t.Fatalf("Set error %v, want ErrKeychainUnavailable", err)
	}
	if _, err := got.Get("server"); !errors.Is(err, model.ErrKeychainUnavailable) {
		t.Fatalf("Get error %v, want ErrKeychainUnavailable", err)
	}
	if err := got.Delete("server"); !errors.Is(err, model.ErrKeychainUnavailable) {
		t.Fatalf("Delete error %v, want ErrKeychainUnavailable", err)
	}
}

func TestProbeKeychainRejectsNil(t *testing.T) {
	if keychainUsable(ProbeKeychain(nil)) {
		t.Fatalf("a nil keychain counted as usable")
	}
}

func TestOptionsRejectMissingDependencies(t *testing.T) {
	h := newHarness(t)
	full := h.opts
	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"no clock", func(o *Options) { o.Now = nil }},
		{"no dialer", func(o *Options) { o.Dialer = nil }},
		{"no keychain tokens", func(o *Options) { o.Tokens = nil }},
		{"no file tokens", func(o *Options) { o.Files = nil }},
		{"no memory tokens", func(o *Options) { o.Memory = nil }},
		{"no profile store", func(o *Options) { o.Store = nil }},
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

func TestBuildReturnsShellOnConnectRoute(t *testing.T) {
	h := newHarness(t)
	win := h.app.NewWindow("x")
	sh, err := Build(h.app, win, h.opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if sh == nil || sh.Window() != win {
		t.Fatalf("Build returned a shell that does not own the window")
	}
	if got := sh.Navigator().Current(); got != model.RouteConnect {
		t.Fatalf("first route %q, want connect", got)
	}
	h.drain()
}

func TestAPIDialerProbesAndDials(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	dialer := NewDialer()

	info, err := dialer.Probe(context.Background(), admin.srv.URL)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.SPKISHA256 != admin.pin {
		t.Fatalf("probed pin %q, want %q", info.SPKISHA256, admin.pin)
	}
	if info.VerifiesWithSystemRoots {
		t.Fatalf("a self-signed test certificate verified with system roots")
	}

	client, err := dialer.Dial(adminv1.ClientConfig{BaseURL: admin.srv.URL, Trust: adminv1Pinned(admin.pin), Token: testDeviceTok})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if _, ok := client.(sessionAPI); !ok {
		t.Fatalf("dialed client %T lacks the session methods", client)
	}
	got, err := client.ServerInfo(context.Background())
	if err != nil || got.ServerID != testServerID {
		t.Fatalf("ServerInfo = %+v, %v", got, err)
	}

	wrong := "SHA256:" + fmt.Sprintf("%064x", 1)
	pinned, err := dialer.Dial(adminv1.ClientConfig{BaseURL: admin.srv.URL, Trust: adminv1Pinned(wrong)})
	if err != nil {
		t.Fatalf("Dial with a wrong pin: %v", err)
	}
	var mismatch *adminv1.PinMismatchError
	if _, err := pinned.ServerInfo(context.Background()); !errors.As(err, &mismatch) {
		t.Fatalf("ServerInfo with a wrong pin = %v, want PinMismatchError", err)
	}

	if _, err := dialer.Dial(adminv1.ClientConfig{BaseURL: "::not a url::"}); err == nil {
		t.Fatalf("Dial accepted an invalid URL")
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
		{"device revoked", apiError(adminv1.CodeDeviceRevoked), 1},
		{"wrapped device revoked", fmt.Errorf("call: %w", apiError(adminv1.CodeDeviceRevoked)), 1},
		{"pin mismatch", &adminv1.PinMismatchError{Expected: "a", Got: "b"}, 1},
		{"wrapped pin mismatch", fmt.Errorf("call: %w", &adminv1.PinMismatchError{Expected: "a", Got: "b"}), 1},
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
