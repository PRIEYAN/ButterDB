package sigv4

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func TestClockSkewBoundary(t *testing.T) {
	tests := []struct {
		name   string
		offset time.Duration
		want   bool
	}{
		{name: "same instant", offset: 0, want: true},
		{name: "server ahead 14:59", offset: 14*time.Minute + 59*time.Second, want: true},
		{name: "server ahead exactly 15:00", offset: 15 * time.Minute, want: true},
		{name: "server ahead 15:01", offset: 15*time.Minute + time.Second, want: false},
		{name: "server behind 14:59", offset: -(14*time.Minute + 59*time.Second), want: true},
		{name: "server behind 15:01", offset: -(15*time.Minute + time.Second), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			req := sdkRequest{method: http.MethodGet, target: "/bucket/key"}.build(t)
			env.clock.set(baseTime.Add(test.offset))
			_, err := env.verifier.Authenticate(t.Context(), req)
			if test.want {
				if err != nil {
					t.Fatalf("Authenticate() error = %v", err)
				}
				return
			}
			typed := requireReason(t, err, ReasonSkew)
			if !errors.Is(err, errs.ErrRequestExpired) {
				t.Fatalf("error does not wrap ErrRequestExpired: %v", err)
			}
			if !typed.RequestTime.Equal(baseTime) || !typed.ServerTime.Equal(baseTime.Add(test.offset)) {
				t.Fatalf("Error times = %v / %v", typed.RequestTime, typed.ServerTime)
			}
		})
	}
}

func TestRegionUsEast1AlwaysAccepted(t *testing.T) {
	tests := []struct {
		name         string
		serverRegion string
		signRegion   string
	}{
		{name: "default region", serverRegion: "us-east-1", signRegion: "us-east-1"},
		{name: "configured region", serverRegion: "eu-west-1", signRegion: "eu-west-1"},
		{name: "us-east-1 against configured", serverRegion: "eu-west-1", signRegion: "us-east-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, test.serverRegion)
			req := sdkRequest{method: http.MethodGet, target: "/bucket/key", region: test.signRegion}.build(t)
			auth, err := env.verifier.Authenticate(t.Context(), req)
			if err != nil {
				t.Fatalf("Authenticate() error = %v", err)
			}
			if auth.Region != test.signRegion {
				t.Fatalf("Auth.Region = %q", auth.Region)
			}
		})
	}
}

func TestWrongRegion(t *testing.T) {
	env := newTestEnv(t, "eu-west-1")
	req := sdkRequest{method: http.MethodGet, target: "/bucket/key", region: "ap-south-1"}.build(t)
	_, err := env.verifier.Authenticate(t.Context(), req)
	typed := requireReason(t, err, ReasonWrongRegion)
	if typed.Region != "ap-south-1" || typed.AccessKeyID != testKeyID {
		t.Fatalf("Error = %+v", typed)
	}
	if !errors.Is(err, errs.ErrSignatureMismatch) {
		t.Fatalf("error does not wrap ErrSignatureMismatch: %v", err)
	}
}

func TestSigV2Rejected(t *testing.T) {
	tests := []struct {
		name   string
		header string
		target string
	}{
		{name: "authorization header", header: "AWS " + testKeyID + ":frJIUN8DYpKDtOLCwo//yllqDzg=", target: "/bucket/key"},
		{name: "query string", target: "/bucket/key?AWSAccessKeyId=" + testKeyID + "&Signature=abc%3D&Expires=1700000000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			req, _ := http.NewRequest(http.MethodGet, "http://"+testHost+test.target, nil)
			if test.header != "" {
				req.Header.Set("Authorization", test.header)
			}
			_, err := env.verifier.Authenticate(t.Context(), req)
			requireReason(t, err, ReasonSigV2)
		})
	}
}

func TestSigV4ARejected(t *testing.T) {
	tests := []struct {
		name   string
		header string
		target string
	}{
		{name: "authorization header", header: "AWS4-ECDSA-P256-SHA256 Credential=" + testKeyID + "/20261113/s3/aws4_request, SignedHeaders=host, Signature=abc", target: "/b/k"},
		{name: "streaming content sha", header: "", target: "/b/k"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			var err error
			if test.name == "streaming content sha" {
				req := sdkRequest{method: http.MethodPut, target: test.target, payloadHash: "STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD"}.build(t)
				_, err = env.verifier.Authenticate(t.Context(), req)
			} else {
				req, _ := http.NewRequest(http.MethodGet, "http://"+testHost+test.target, nil)
				if test.header != "" {
					req.Header.Set("Authorization", test.header)
				}
				_, err = env.verifier.Authenticate(t.Context(), req)
			}
			requireReason(t, err, ReasonSigV4A)
			if !errors.Is(err, errs.ErrNotImplemented) {
				t.Fatalf("error does not wrap ErrNotImplemented: %v", err)
			}
		})
	}
}

func TestSecurityTokenRejected(t *testing.T) {
	tests := []struct {
		name   string
		header bool
		target string
	}{
		{name: "header on signed request", header: true, target: "/b/k"},
		{name: "query on signed request", target: "/b/k?X-Amz-Security-Token=abc"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			sdk := sdkRequest{method: http.MethodGet, target: test.target}
			if test.header {
				sdk.headers = map[string]string{"X-Amz-Security-Token": "token"}
			}
			_, err := env.verifier.Authenticate(t.Context(), sdk.build(t))
			requireReason(t, err, ReasonSecurityToken)
		})
	}
	t.Run("header on anonymous request", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req, _ := http.NewRequest(http.MethodGet, "http://"+testHost+"/b/k", nil)
		req.Header.Set("X-Amz-Security-Token", "token")
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonSecurityToken)
	})
}

func TestBothMechanismsRejected(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkRequest{method: http.MethodGet, target: "/b/k?X-Amz-Algorithm=AWS4-HMAC-SHA256"}.build(t)
	_, err := env.verifier.Authenticate(t.Context(), req)
	requireReason(t, err, ReasonBothMechanisms)
}

func TestUnsupportedSchemeRejected(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req, _ := http.NewRequest(http.MethodGet, "http://"+testHost+"/b/k", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	_, err := env.verifier.Authenticate(t.Context(), req)
	requireReason(t, err, ReasonUnsupportedScheme)
}

func TestAnonymousRequest(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req, _ := http.NewRequest(http.MethodGet, "http://"+testHost+"/b/k?prefix=a", nil)
	auth, err := env.verifier.Authenticate(t.Context(), req)
	if err != nil || auth.Kind != AuthAnonymous || auth.Mode != PayloadNone {
		t.Fatalf("Authenticate() = %+v, %v", auth, err)
	}
	if env.secrets.lookups != 0 {
		t.Fatalf("anonymous request looked up %d secrets", env.secrets.lookups)
	}
}

func TestRevokedKeyRejected(t *testing.T) {
	tests := []struct {
		name  string
		entry fakeSecretEntry
	}{
		{name: "revoked key info", entry: fakeSecretEntry{secret: []byte(testSecret), info: access.KeyInfo{ID: testKeyID, RevokedAt: 1700000000}}},
		{name: "source reports revoked", entry: fakeSecretEntry{err: fmt.Errorf("keys: %w", errs.ErrAccessKeyRevoked)}},
		{name: "source reports not found", entry: fakeSecretEntry{err: fmt.Errorf("keys: %w", errs.ErrAccessKeyNotFound)}},
		{name: "source reports malformed id", entry: fakeSecretEntry{err: fmt.Errorf("keys: %w", access.ErrInvalidAccessKeyID)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			env.secrets.put(testKeyID, test.entry)
			req := sdkRequest{method: http.MethodGet, target: "/b/k"}.build(t)
			_, err := env.verifier.Authenticate(t.Context(), req)
			typed := requireReason(t, err, ReasonUnknownKey)
			if typed.AccessKeyID != testKeyID || !errors.Is(err, errs.ErrUnknownAccessKey) {
				t.Fatalf("Error = %+v", typed)
			}
		})
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkRequest{method: http.MethodGet, target: "/b/k", keyID: "BDBKAAAAAAAAAAAAAAAA", secret: "whatever"}.build(t)
	_, err := env.verifier.Authenticate(t.Context(), req)
	requireReason(t, err, ReasonUnknownKey)
}

func TestSecretLookupFailureIsNotAuthError(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	boom := errors.New("database offline")
	env.secrets.put(testKeyID, fakeSecretEntry{err: boom})
	req := sdkRequest{method: http.MethodGet, target: "/b/k"}.build(t)
	_, err := env.verifier.Authenticate(t.Context(), req)
	var typed *Error
	if !errors.Is(err, boom) || errors.As(err, &typed) {
		t.Fatalf("Authenticate() error = %v", err)
	}
}

func TestHeaderAuthMalformed(t *testing.T) {
	good := "Credential=" + testKeyID + "/20261113/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=" + strings.Repeat("a", 64)
	tests := []struct {
		name          string
		authorization string
		want          Reason
	}{
		{name: "missing signature", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261113/us-east-1/s3/aws4_request, SignedHeaders=host", want: ReasonHeaderMalformed},
		{name: "missing credential", authorization: "AWS4-HMAC-SHA256 SignedHeaders=host, Signature=" + strings.Repeat("a", 64), want: ReasonHeaderMalformed},
		{name: "four credential parts", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261113/us-east-1/aws4_request, SignedHeaders=host, Signature=" + strings.Repeat("a", 64), want: ReasonHeaderMalformed},
		{name: "wrong service", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261113/us-east-1/ec2/aws4_request, SignedHeaders=host, Signature=" + strings.Repeat("a", 64), want: ReasonHeaderMalformed},
		{name: "wrong terminator", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261113/us-east-1/s3/aws4_other, SignedHeaders=host, Signature=" + strings.Repeat("a", 64), want: ReasonHeaderMalformed},
		{name: "duplicate component", authorization: "AWS4-HMAC-SHA256 " + good + ", Signature=" + strings.Repeat("b", 64), want: ReasonHeaderMalformed},
		{name: "unknown component", authorization: "AWS4-HMAC-SHA256 " + good + ", Extra=1", want: ReasonHeaderMalformed},
		{name: "short signature", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261113/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=abc", want: ReasonSignature},
		{name: "uppercase signature", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261113/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=" + strings.Repeat("A", 64), want: ReasonSignature},
		{name: "signed headers lack host", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261113/us-east-1/s3/aws4_request, SignedHeaders=x-amz-date, Signature=" + strings.Repeat("a", 64), want: ReasonHeaderMalformed},
		{name: "signed headers unsorted", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261113/us-east-1/s3/aws4_request, SignedHeaders=x-amz-date;host, Signature=" + strings.Repeat("a", 64), want: ReasonHeaderMalformed},
		{name: "signed headers upper case", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261113/us-east-1/s3/aws4_request, SignedHeaders=Host, Signature=" + strings.Repeat("a", 64), want: ReasonHeaderMalformed},
		{name: "scope date differs from request date", authorization: "AWS4-HMAC-SHA256 Credential=" + testKeyID + "/20261112/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=" + strings.Repeat("a", 64), want: ReasonHeaderMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			req, _ := http.NewRequest(http.MethodGet, "http://"+testHost+"/b/k", nil)
			req.Header.Set("Authorization", test.authorization)
			req.Header.Set("X-Amz-Date", "20261113T120000Z")
			req.Header.Set("X-Amz-Content-Sha256", testPayloadMD)
			_, err := env.verifier.Authenticate(t.Context(), req)
			requireReason(t, err, test.want)
		})
	}
}

func TestHeaderAuthRequiredHeaders(t *testing.T) {
	t.Run("missing date", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodGet, target: "/b/k"}.build(t)
		req.Header.Del("X-Amz-Date")
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonMissingDate)
		if !errors.Is(err, errs.ErrAccessDenied) {
			t.Fatalf("error does not wrap ErrAccessDenied: %v", err)
		}
	})
	t.Run("unparseable date", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodGet, target: "/b/k"}.build(t)
		req.Header.Set("X-Amz-Date", "yesterday")
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonMissingDate)
	})
	t.Run("missing content sha256", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodGet, target: "/b/k"}.build(t)
		req.Header.Del("X-Amz-Content-Sha256")
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonMissingContentSHA256)
	})
	t.Run("bad content sha256", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", payloadHash: "not-a-digest"}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonBadContentSHA256)
	})
	t.Run("uppercase hex content sha256", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", payloadHash: strings.ToUpper(emptyPayloadSHA256)}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonBadContentSHA256)
	})
	t.Run("hex digest on get must be the empty digest", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		other := strings.Repeat("ab", 32)
		req := sdkRequest{method: http.MethodGet, target: "/b/k", payloadHash: other}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		typed := requireReason(t, err, ReasonContentSHA256Mismatch)
		if typed.ClientSHA256 != other || typed.ServerSHA256 != emptyPayloadSHA256 {
			t.Fatalf("Error digests = %q / %q", typed.ClientSHA256, typed.ServerSHA256)
		}
	})
}

func TestHeaderAuthDateHeaderFallback(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkRequest{method: http.MethodGet, target: "/b/k", headers: map[string]string{"Date": baseTime.Format(http.TimeFormat)}}.build(t)
	if _, err := env.verifier.Authenticate(t.Context(), req); err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
}

func TestAuthFieldsAfterHeaderAuth(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkRequest{method: http.MethodPut, target: "/b/k", body: "hello", payloadHash: testPayloadMD}.build(t)
	auth, err := env.verifier.Authenticate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if auth.Kind != AuthHeader || auth.Mode != PayloadUnsigned || auth.ContentSHA256 != testPayloadMD || auth.DecodedLength != 5 ||
		string(auth.Key.ID) != testKeyID || !auth.RequestTime.Equal(baseTime) || auth.Trailer != "" {
		t.Fatalf("Auth = %+v", auth)
	}
}

func TestErrorMessageHasNoSecrets(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkRequest{method: http.MethodGet, target: "/b/k"}.build(t)
	signature := req.Header.Get("Authorization")
	signature = signature[strings.LastIndex(signature, "Signature=")+len("Signature="):]
	req.Header.Set("Authorization", strings.Replace(req.Header.Get("Authorization"), signature[:8], "00000000", 1))
	_, err := env.verifier.Authenticate(t.Context(), req)
	typed := requireReason(t, err, ReasonSignature)
	for _, secret := range []string{signature, testSecret, "00000000"} {
		if strings.Contains(typed.Error(), secret) || strings.Contains(typed.Err.Error(), secret) {
			t.Fatalf("error text leaks %q", secret)
		}
	}
}

func TestNewVerifierRejectsNilDependencies(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		secrets SecretSource
	}{
		{name: "nil secrets", cfg: Config{Now: time.Now}, secrets: nil},
		{name: "nil clock", cfg: Config{}, secrets: newFakeSecrets()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				recovered, _ := recover().(error)
				if !errors.Is(recovered, errs.ErrMissingDependency) {
					t.Fatalf("recovered %v, want ErrMissingDependency", recovered)
				}
			}()
			NewVerifier(test.cfg, test.secrets)
		})
	}
}

func TestSigningKeyCacheBounded(t *testing.T) {
	clock := &testClock{now: baseTime}
	cache := newSigningKeyCache(clock.Now)
	for i := range signingKeyCacheMax + 50 {
		cache.signingKey(signingKeyID{accessKeyID: fmt.Sprintf("key-%d", i), date: "20261113", region: "us-east-1"}, []byte(testSecret))
	}
	if got := cache.size(); got != signingKeyCacheMax {
		t.Fatalf("cache size = %d, want %d", got, signingKeyCacheMax)
	}
}

func TestSigningKeyCacheReuseExpiryAndRotation(t *testing.T) {
	clock := &testClock{now: baseTime}
	cache := newSigningKeyCache(clock.Now)
	id := signingKeyID{accessKeyID: testKeyID, date: "20261113", region: "us-east-1"}
	first := cache.signingKey(id, []byte(testSecret))
	if again := cache.signingKey(id, []byte(testSecret)); &again[0] != &first[0] {
		t.Fatal("second lookup did not reuse the cached key")
	}
	rotated := cache.signingKey(id, []byte("rotated-secret"))
	if string(rotated) == string(first) {
		t.Fatal("rotated secret reused the stale signing key")
	}
	if string(rotated) != string(deriveSigningKey([]byte("rotated-secret"), id.date, id.region)) {
		t.Fatal("rotated signing key is wrong")
	}
	clock.set(baseTime.Add(signingKeyCacheTTL))
	expired := cache.signingKey(id, []byte("rotated-secret"))
	if &expired[0] == &rotated[0] {
		t.Fatal("entry past 24h was reused")
	}
	if cache.size() != 1 {
		t.Fatalf("cache size = %d", cache.size())
	}
}
