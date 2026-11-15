package sigv4

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	signerv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func sdkPresigned(t *testing.T, method, target string, expires time.Duration, at time.Time) *http.Request {
	t.Helper()
	separator := "?"
	if strings.Contains(target, "?") {
		separator = "&"
	}
	raw := "http://" + testHost + target + separator + "X-Amz-Expires=" + strconv.FormatInt(int64(expires/time.Second), 10)
	req, err := http.NewRequest(method, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	signer := signerv4.NewSigner(func(o *signerv4.SignerOptions) { o.DisableURIPathEscaping = true })
	creds := aws.Credentials{AccessKeyID: testKeyID, SecretAccessKey: testSecret}
	signed, _, err := signer.PresignHTTP(context.Background(), creds, req, testPayloadMD, "s3", "us-east-1", at)
	if err != nil {
		t.Fatal(err)
	}
	out, err := http.NewRequest(method, signed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAWSDocExamplePresignedURL(t *testing.T) {
	example := loadDocExample(t, "awsdoc_presigned_url.json")
	env := example.env(t)
	req := example.request(t)
	checkDocExampleCanonical(t, example, req, "host", unsignedPayload, querySignature)

	auth, err := env.verifier.Authenticate(t.Context(), req)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if auth.Kind != AuthPresigned || auth.Mode != PayloadUnsigned || auth.Region != example.Region {
		t.Fatalf("Authenticate() = %+v", auth)
	}

	tampered := example.request(t)
	tampered.URL.RawQuery = strings.Replace(tampered.URL.RawQuery, "X-Amz-Expires=86400", "X-Amz-Expires=86399", 1)
	_, err = env.verifier.Authenticate(t.Context(), tampered)
	requireReason(t, err, ReasonSignature)

	input := PresignInput{
		Method:      http.MethodGet,
		URL:         mustURL(t, "https://examplebucket.s3.amazonaws.com/test.txt"),
		AccessKeyID: access.AccessKeyID(example.AccessKeyID),
		Secret:      []byte(example.SecretAccessKey),
		Region:      example.Region,
		Time:        env.clock.Now(),
		Expires:     86400 * time.Second,
	}
	got, err := Presign(input)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://examplebucket.s3.amazonaws.com" + example.Target; got != want {
		t.Fatalf("Presign() = %q\nwant %q", got, want)
	}
}

func TestPresignExpiresMax(t *testing.T) {
	tests := []struct {
		name    string
		expires time.Duration
		reason  Reason
	}{
		{name: "one second", expires: time.Second},
		{name: "exactly seven days", expires: 7 * 24 * time.Hour},
		{name: "one second over seven days", expires: 7*24*time.Hour + time.Second, reason: ReasonQueryMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			req := sdkPresigned(t, http.MethodGet, "/b/k", test.expires, baseTime)
			_, err := env.verifier.Authenticate(t.Context(), req)
			if test.reason == 0 {
				if err != nil {
					t.Fatalf("Authenticate() error = %v", err)
				}
				return
			}
			requireReason(t, err, test.reason)
		})
	}
	for _, value := range []string{"0", "-5", "abc", ""} {
		t.Run("expires "+value, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			req := sdkPresigned(t, http.MethodGet, "/b/k", time.Hour, baseTime)
			query := req.URL.Query()
			query.Set("X-Amz-Expires", value)
			req.URL.RawQuery = query.Encode()
			_, err := env.verifier.Authenticate(t.Context(), req)
			requireReason(t, err, ReasonQueryMalformed)
		})
	}
	t.Run("presign refuses over seven days", func(t *testing.T) {
		_, err := Presign(PresignInput{Method: "GET", URL: mustURL(t, "http://h/b/k"), AccessKeyID: testKeyID, Secret: []byte(testSecret), Region: "us-east-1", Time: baseTime, Expires: 7*24*time.Hour + time.Second})
		if err == nil {
			t.Fatal("Presign() accepted an expiry over seven days")
		}
	})
}

func TestPresignExpired(t *testing.T) {
	tests := []struct {
		name    string
		advance time.Duration
		expired bool
	}{
		{name: "at signing time", advance: 0},
		{name: "exactly at expiry", advance: 60 * time.Second},
		{name: "one second past expiry", advance: 61 * time.Second, expired: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			req := sdkPresigned(t, http.MethodGet, "/b/k", 60*time.Second, baseTime)
			env.clock.set(baseTime.Add(test.advance))
			_, err := env.verifier.Authenticate(t.Context(), req)
			if !test.expired {
				if err != nil {
					t.Fatalf("Authenticate() error = %v", err)
				}
				return
			}
			typed := requireReason(t, err, ReasonExpired)
			if !typed.Expires.Equal(baseTime.Add(60*time.Second)) || !typed.ServerTime.Equal(baseTime.Add(test.advance)) {
				t.Fatalf("Error times = %v / %v", typed.Expires, typed.ServerTime)
			}
			if !errors.Is(err, errs.ErrRequestExpired) {
				t.Fatalf("error does not wrap ErrRequestExpired: %v", err)
			}
		})
	}
}

func TestPresignNotYetValid(t *testing.T) {
	tests := []struct {
		name   string
		signed time.Duration
		valid  bool
	}{
		{name: "ahead 14:59", signed: 14*time.Minute + 59*time.Second, valid: true},
		{name: "ahead exactly 15:00", signed: 15 * time.Minute, valid: true},
		{name: "ahead 15:01", signed: 15*time.Minute + time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			req := sdkPresigned(t, http.MethodGet, "/b/k", time.Hour, baseTime.Add(test.signed))
			_, err := env.verifier.Authenticate(t.Context(), req)
			if test.valid {
				if err != nil {
					t.Fatalf("Authenticate() error = %v", err)
				}
				return
			}
			requireReason(t, err, ReasonNotYetValid)
		})
	}
}

func TestPresignedSkewDoesNotApplyToOldRequests(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkPresigned(t, http.MethodGet, "/b/k", 24*time.Hour, baseTime)
	env.clock.set(baseTime.Add(20 * time.Hour))
	if _, err := env.verifier.Authenticate(t.Context(), req); err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
}

func TestPresignRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		method string
		url    string
	}{
		{name: "get", method: http.MethodGet, url: "http://s3.butter.test/bucket/key.txt"},
		{name: "put", method: http.MethodPut, url: "http://s3.butter.test/bucket/key.txt"},
		{name: "upload part", method: http.MethodPut, url: "http://s3.butter.test/bucket/key.txt?partNumber=2&uploadId=abc"},
		{name: "special characters", method: http.MethodGet, url: "http://s3.butter.test/bucket/my%20photo%28%2B%29%21.jpg"},
		{name: "unknown query parameter", method: http.MethodGet, url: "http://s3.butter.test/bucket/key?response-content-type=text%2Fplain&x-id=GetObject"},
		{name: "unicode key", method: http.MethodGet, url: "http://s3.butter.test/bucket/%E2%82%AC.txt"},
		{name: "host with port", method: http.MethodGet, url: "http://127.0.0.1:9000/bucket/key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			signed, err := Presign(PresignInput{
				Method: test.method, URL: mustURL(t, test.url), AccessKeyID: testKeyID, Secret: []byte(testSecret),
				Region: "us-east-1", Time: baseTime, Expires: time.Hour,
			})
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest(test.method, signed, nil)
			if err != nil {
				t.Fatal(err)
			}
			auth, err := env.verifier.Authenticate(t.Context(), req)
			if err != nil || auth.Kind != AuthPresigned {
				t.Fatalf("Authenticate() = %+v, %v", auth, err)
			}
		})
	}
}

func TestPresignMatchesSDKPresigner(t *testing.T) {
	targets := []string{"/bucket/key.txt", "/bucket/a%20b%2Bc%21.txt?versionId=1&prefix=a%20b"}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			sdkURL := sdkPresigned(t, http.MethodGet, target, time.Hour, baseTime).URL
			parsed := mustURL(t, "http://"+testHost+target)
			got, err := Presign(PresignInput{
				Method: http.MethodGet, URL: parsed, AccessKeyID: testKeyID, Secret: []byte(testSecret),
				Region: "us-east-1", Time: baseTime, Expires: time.Hour,
			})
			if err != nil {
				t.Fatal(err)
			}
			gotURL, err := url.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			if gotURL.Query().Get("X-Amz-Signature") != sdkURL.Query().Get("X-Amz-Signature") {
				t.Fatalf("signature differs from the SDK presigner\n got: %s\nsdk: %s", got, sdkURL)
			}
		})
	}
}

func TestPresignValidation(t *testing.T) {
	valid := PresignInput{Method: "GET", URL: mustURL(t, "http://h/b/k"), AccessKeyID: testKeyID, Secret: []byte(testSecret), Region: "us-east-1", Time: baseTime, Expires: time.Hour}
	tests := []struct {
		name   string
		mutate func(*PresignInput)
	}{
		{name: "nil url", mutate: func(in *PresignInput) { in.URL = nil }},
		{name: "no host", mutate: func(in *PresignInput) { in.URL = mustURL(t, "/b/k") }},
		{name: "no method", mutate: func(in *PresignInput) { in.Method = "" }},
		{name: "no key id", mutate: func(in *PresignInput) { in.AccessKeyID = "" }},
		{name: "no secret", mutate: func(in *PresignInput) { in.Secret = nil }},
		{name: "no region", mutate: func(in *PresignInput) { in.Region = "" }},
		{name: "zero expires", mutate: func(in *PresignInput) { in.Expires = 0 }},
		{name: "sub second expires", mutate: func(in *PresignInput) { in.Expires = time.Millisecond }},
		{name: "already signed", mutate: func(in *PresignInput) { in.URL = mustURL(t, "http://h/b/k?X-Amz-Signature=abc") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := valid
			test.mutate(&in)
			if _, err := Presign(in); err == nil {
				t.Fatal("Presign() succeeded")
			}
		})
	}
	if _, err := Presign(valid); err != nil {
		t.Fatalf("Presign(valid) error = %v", err)
	}
}

func TestPresignedMalformedParameters(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(url.Values)
		reason Reason
	}{
		{name: "missing credential", mutate: func(q url.Values) { q.Del("X-Amz-Credential") }, reason: ReasonQueryMalformed},
		{name: "missing date", mutate: func(q url.Values) { q.Del("X-Amz-Date") }, reason: ReasonQueryMalformed},
		{name: "missing signed headers", mutate: func(q url.Values) { q.Del("X-Amz-SignedHeaders") }, reason: ReasonQueryMalformed},
		{name: "missing signature", mutate: func(q url.Values) { q.Del("X-Amz-Signature") }, reason: ReasonQueryMalformed},
		{name: "missing expires", mutate: func(q url.Values) { q.Del("X-Amz-Expires") }, reason: ReasonQueryMalformed},
		{name: "unknown algorithm", mutate: func(q url.Values) { q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA512") }, reason: ReasonQueryMalformed},
		{name: "sigv4a algorithm", mutate: func(q url.Values) { q.Set("X-Amz-Algorithm", "AWS4-ECDSA-P256-SHA256") }, reason: ReasonSigV4A},
		{name: "bad credential", mutate: func(q url.Values) { q.Set("X-Amz-Credential", testKeyID+"/20261113/us-east-1") }, reason: ReasonQueryMalformed},
		{name: "bad date", mutate: func(q url.Values) { q.Set("X-Amz-Date", "tomorrow") }, reason: ReasonQueryMalformed},
		{name: "short signature", mutate: func(q url.Values) { q.Set("X-Amz-Signature", "abc") }, reason: ReasonSignature},
		{name: "signed headers lack host", mutate: func(q url.Values) { q.Set("X-Amz-SignedHeaders", "x-amz-date") }, reason: ReasonQueryMalformed},
		{name: "scope date differs", mutate: func(q url.Values) { q.Set("X-Amz-Date", "20261112T120000Z") }, reason: ReasonQueryMalformed},
		{name: "wrong region", mutate: func(q url.Values) { q.Set("X-Amz-Credential", testKeyID+"/20261113/mars-1/s3/aws4_request") }, reason: ReasonWrongRegion},
		{name: "security token", mutate: func(q url.Values) { q.Set("X-Amz-Security-Token", "t") }, reason: ReasonSecurityToken},
		{name: "streaming signed content sha", mutate: func(q url.Values) { q.Set("X-Amz-Content-Sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD") }, reason: ReasonBadContentSHA256},
		{name: "garbage content sha", mutate: func(q url.Values) { q.Set("X-Amz-Content-Sha256", "zzz") }, reason: ReasonBadContentSHA256},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			if test.name == "wrong region" {
				env = newTestEnv(t, "eu-west-1")
			}
			req := sdkPresigned(t, http.MethodGet, "/b/k", time.Hour, baseTime)
			query := req.URL.Query()
			test.mutate(query)
			req.URL.RawQuery = query.Encode()
			_, err := env.verifier.Authenticate(t.Context(), req)
			requireReason(t, err, test.reason)
		})
	}
}

func TestPresignedRevokedKeyRejected(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkPresigned(t, http.MethodGet, "/b/k", time.Hour, baseTime)
	if _, err := env.verifier.Authenticate(t.Context(), req); err != nil {
		t.Fatalf("Authenticate() before revocation: %v", err)
	}
	env.secrets.put(testKeyID, fakeSecretEntry{secret: []byte(testSecret), info: access.KeyInfo{ID: testKeyID, RevokedAt: 1}})
	_, err := env.verifier.Authenticate(t.Context(), req)
	requireReason(t, err, ReasonUnknownKey)
}

func TestPresignedHexDigestHeaderIsVerified(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkPresigned(t, http.MethodPut, "/b/k", time.Hour, baseTime)
	digest := strings.Repeat("ab", 32)
	req.Header.Set("X-Amz-Content-Sha256", digest)
	auth, err := env.verifier.Authenticate(t.Context(), req)
	if err != nil || auth.Mode != PayloadSignedHex || auth.ContentSHA256 != digest {
		t.Fatalf("Authenticate() = %+v, %v", auth, err)
	}
}

func TestPresignedAuthKindWithSDKQueryContentSHA(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkPresigned(t, http.MethodGet, "/b/k", time.Hour, baseTime)
	if auth, err := env.verifier.Authenticate(t.Context(), req); err != nil || auth.ContentSHA256 != unsignedPayload {
		t.Fatalf("Authenticate() = %+v, %v", auth, err)
	}
}
