package adminv1_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type tlsFixture struct {
	server   *httptest.Server
	requests *atomic.Int64
	pin      string
}

func newTLSFixture(t *testing.T, handler http.HandlerFunc) tlsFixture {
	t.Helper()
	requests := &atomic.Int64{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	sum := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	return tlsFixture{server: server, requests: requests, pin: "SHA256:" + hex.EncodeToString(sum[:])}
}

func serverInfoHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"server_id":"srv1","api_version":"v1","state":"ready"}`))
}

func pinnedClient(t *testing.T, base, pin string) *adminv1.Client {
	t.Helper()
	client, err := adminv1.NewClient(adminv1.ClientConfig{
		BaseURL: base,
		Trust:   adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: pin},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func TestPinnedTrustAcceptsMatchingSPKI(t *testing.T) {
	fixture := newTLSFixture(t, serverInfoHandler)
	cases := []struct {
		name string
		pin  string
	}{
		{name: "canonical", pin: fixture.pin},
		{name: "lowercase prefix", pin: strings.ToLower(fixture.pin)},
		{name: "bare hex", pin: strings.TrimPrefix(fixture.pin, "SHA256:")},
		{name: "upper hex", pin: "SHA256:" + strings.ToUpper(strings.TrimPrefix(fixture.pin, "SHA256:"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := pinnedClient(t, fixture.server.URL, tc.pin)
			info, err := client.ServerInfo(context.Background())
			if err != nil {
				t.Fatalf("server info: %v", err)
			}
			if info.ServerID != "srv1" {
				t.Fatalf("server id = %q", info.ServerID)
			}
		})
	}
}

func TestPinnedTrustRejectsMismatchBeforeRequest(t *testing.T) {
	fixture := newTLSFixture(t, serverInfoHandler)
	wrong := "SHA256:" + strings.Repeat("ab", 32)
	client := pinnedClient(t, fixture.server.URL, wrong)
	client.SetToken("bdt1_secret")
	_, err := client.ServerInfo(context.Background())
	var mismatch *adminv1.PinMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %v, want *PinMismatchError", err)
	}
	if mismatch.Expected != wrong {
		t.Fatalf("expected = %q", mismatch.Expected)
	}
	if mismatch.Got != fixture.pin {
		t.Fatalf("got = %q, want %q", mismatch.Got, fixture.pin)
	}
	if _, ok := err.(*adminv1.PinMismatchError); !ok {
		t.Fatalf("error is wrapped: %T", err)
	}
	if n := fixture.requests.Load(); n != 0 {
		t.Fatalf("server saw %d requests", n)
	}
	if _, err := client.Status(context.Background()); !errors.As(err, &mismatch) {
		t.Fatalf("second call error = %v", err)
	}
	if n := fixture.requests.Load(); n != 0 {
		t.Fatalf("server saw %d requests after retry", n)
	}
}

func TestPinnedTrustConfigValidation(t *testing.T) {
	cases := []struct {
		name  string
		trust adminv1.Trust
	}{
		{name: "no mode", trust: adminv1.Trust{}},
		{name: "unknown mode", trust: adminv1.Trust{Mode: "tofu"}},
		{name: "pinned without pin", trust: adminv1.Trust{Mode: adminv1.TrustPinned}},
		{name: "pinned short pin", trust: adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: "SHA256:abcd"}},
		{name: "pinned non hex", trust: adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: "SHA256:" + strings.Repeat("zz", 32)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: "https://example.com", Trust: tc.trust}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSystemTrustIgnoresPinAndRejectsUnknownRoot(t *testing.T) {
	fixture := newTLSFixture(t, serverInfoHandler)
	client, err := adminv1.NewClient(adminv1.ClientConfig{
		BaseURL: fixture.server.URL,
		Trust:   adminv1.Trust{Mode: adminv1.TrustSystem, SPKISHA256: fixture.pin},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.ServerInfo(context.Background()); err == nil {
		t.Fatal("expected certificate verification failure")
	}
	if n := fixture.requests.Load(); n != 0 {
		t.Fatalf("server saw %d requests", n)
	}
}

func TestProbeCertificateSendsNoRequest(t *testing.T) {
	fixture := newTLSFixture(t, serverInfoHandler)
	info, err := adminv1.ProbeCertificate(context.Background(), fixture.server.URL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.SPKISHA256 != fixture.pin {
		t.Fatalf("spki = %q, want %q", info.SPKISHA256, fixture.pin)
	}
	groups := strings.Split(info.DisplayFingerprint, " ")
	if len(groups) != 16 {
		t.Fatalf("display groups = %d", len(groups))
	}
	if strings.Join(groups, "") != strings.TrimPrefix(fixture.pin, "SHA256:") {
		t.Fatalf("display = %q", info.DisplayFingerprint)
	}
	for _, group := range groups {
		if len(group) != 4 {
			t.Fatalf("group %q is not 4 characters", group)
		}
	}
	leaf := fixture.server.Certificate()
	if !info.NotAfter.Equal(leaf.NotAfter) {
		t.Fatalf("not after = %v, want %v", info.NotAfter, leaf.NotAfter)
	}
	if info.Subject != leaf.Subject.String() {
		t.Fatalf("subject = %q", info.Subject)
	}
	if len(info.DNSNames) != len(leaf.DNSNames) || len(info.IPAddresses) != len(leaf.IPAddresses) {
		t.Fatalf("sans = %v %v", info.DNSNames, info.IPAddresses)
	}
	if info.VerifiesWithSystemRoots {
		t.Fatal("self-signed test certificate verified with system roots")
	}
	time.Sleep(50 * time.Millisecond)
	if n := fixture.requests.Load(); n != 0 {
		t.Fatalf("server saw %d requests", n)
	}
}

func TestProbeCertificateErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fixture := newTLSFixture(t, serverInfoHandler)
	cases := []struct {
		name string
		ctx  context.Context
		url  string
	}{
		{name: "plain http", ctx: context.Background(), url: "http://127.0.0.1:1"},
		{name: "no host", ctx: context.Background(), url: "https://"},
		{name: "cancelled", ctx: ctx, url: fixture.server.URL},
		{name: "refused", ctx: context.Background(), url: "https://127.0.0.1:1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := adminv1.ProbeCertificate(tc.ctx, tc.url); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPlainHTTPOnlyLoopback(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "localhost", baseURL: "http://localhost:9443"},
		{name: "localhost upper", baseURL: "http://LOCALHOST:9443"},
		{name: "ipv4 loopback", baseURL: "http://127.0.0.1:9443"},
		{name: "ipv6 loopback", baseURL: "http://[::1]:9443"},
		{name: "https any host", baseURL: "https://example.com"},
		{name: "remote name", baseURL: "http://example.com", wantErr: true},
		{name: "private address", baseURL: "http://192.168.1.10:9443", wantErr: true},
		{name: "other loopback address", baseURL: "http://127.0.0.2:9443", wantErr: true},
		{name: "suffix trick", baseURL: "http://localhost.example.com", wantErr: true},
		{name: "ftp", baseURL: "ftp://localhost", wantErr: true},
		{name: "empty", baseURL: "", wantErr: true},
		{name: "no scheme", baseURL: "example.com:9443", wantErr: true},
		{name: "credentials", baseURL: "https://user:pw@example.com", wantErr: true},
		{name: "query", baseURL: "https://example.com?x=1", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adminv1.NewClient(adminv1.ClientConfig{
				BaseURL: tc.baseURL,
				Trust:   adminv1.Trust{Mode: adminv1.TrustSystem},
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestPlainHTTPLoopbackIgnoresTrust(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(serverInfoHandler))
	t.Cleanup(server.Close)
	client, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.ServerInfo(context.Background()); err != nil {
		t.Fatalf("server info: %v", err)
	}
}

func TestPinMismatchErrorText(t *testing.T) {
	err := &adminv1.PinMismatchError{Expected: "SHA256:aa", Got: "SHA256:bb"}
	if !strings.Contains(err.Error(), "SHA256:aa") || !strings.Contains(err.Error(), "SHA256:bb") {
		t.Fatalf("text = %q", err.Error())
	}
	var nilErr *adminv1.PinMismatchError
	if nilErr.Error() == "" {
		t.Fatal("nil receiver text empty")
	}
}
