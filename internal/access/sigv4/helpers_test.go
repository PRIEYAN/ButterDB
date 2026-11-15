package sigv4

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	signerv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const (
	testKeyID     = "BDBK7Q2M9X4T1W8Z3R6Y"
	testSecret    = "k9Q2m7ZpLx4VbN8sT1wRfYc6HdGaE3uJoXiKqWzP"
	testHost      = "s3.butter.test"
	testPayloadMD = "UNSIGNED-PAYLOAD"
)

var baseTime = time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

type fakeSecretEntry struct {
	secret []byte
	info   access.KeyInfo
	err    error
}

type fakeSecrets struct {
	mu      sync.Mutex
	entries map[string]fakeSecretEntry
	lookups int
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{entries: make(map[string]fakeSecretEntry)}
}

func (f *fakeSecrets) add(id, secret string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[id] = fakeSecretEntry{secret: []byte(secret), info: access.KeyInfo{ID: access.AccessKeyID(id), Name: "test"}}
}

func (f *fakeSecrets) put(id string, entry fakeSecretEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[id] = entry
}

func (f *fakeSecrets) Secret(_ context.Context, id access.AccessKeyID) ([]byte, access.KeyInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	entry, ok := f.entries[string(id)]
	if !ok {
		return nil, access.KeyInfo{}, fmt.Errorf("fake: %w", errs.ErrUnknownAccessKey)
	}
	if entry.err != nil {
		return nil, access.KeyInfo{}, entry.err
	}
	return append([]byte(nil), entry.secret...), entry.info, nil
}

type testEnv struct {
	verifier *Verifier
	secrets  *fakeSecrets
	clock    *testClock
}

func newTestEnv(t *testing.T, region string) *testEnv {
	t.Helper()
	secrets := newFakeSecrets()
	secrets.add(testKeyID, testSecret)
	clock := &testClock{now: baseTime}
	verifier := NewVerifier(Config{Region: region, MaxSkew: 15 * time.Minute, MaxPresign: 7 * 24 * time.Hour, Now: clock.Now}, secrets)
	return &testEnv{verifier: verifier, secrets: secrets, clock: clock}
}

type sdkRequest struct {
	method      string
	target      string
	host        string
	body        string
	payloadHash string
	region      string
	at          time.Time
	headers     map[string]string
	keyID       string
	secret      string
}

func (s sdkRequest) build(t *testing.T) *http.Request {
	t.Helper()
	host := s.host
	if host == "" {
		host = testHost
	}
	req, err := http.NewRequest(s.method, "http://"+host+s.target, strings.NewReader(s.body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range s.headers {
		req.Header.Set(name, value)
	}
	payloadHash := s.payloadHash
	if payloadHash == "" {
		payloadHash = testPayloadMD
	}
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	region := s.region
	if region == "" {
		region = "us-east-1"
	}
	at := s.at
	if at.IsZero() {
		at = baseTime
	}
	keyID, secret := s.keyID, s.secret
	if keyID == "" {
		keyID, secret = testKeyID, testSecret
	}
	signer := signerv4.NewSigner(func(o *signerv4.SignerOptions) { o.DisableURIPathEscaping = true })
	creds := aws.Credentials{AccessKeyID: keyID, SecretAccessKey: secret}
	if err := signer.SignHTTP(context.Background(), creds, req, payloadHash, "s3", region, at); err != nil {
		t.Fatal(err)
	}
	return req
}

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not *Error", err)
	}
	return typed
}

func requireReason(t *testing.T, err error, want Reason) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want reason %d", want)
	}
	typed := asError(t, err)
	if typed.Reason != want {
		t.Fatalf("reason = %d (%v), want %d", typed.Reason, err, want)
	}
	return typed
}

type docExample struct {
	Method           string            `json:"method"`
	Target           string            `json:"target"`
	Host             string            `json:"host"`
	Body             string            `json:"body"`
	Headers          map[string]string `json:"headers"`
	Authorization    string            `json:"authorization"`
	CanonicalRequest string            `json:"canonical_request"`
	StringToSign     string            `json:"string_to_sign"`
	Signature        string            `json:"signature"`
	AccessKeyID      string            `json:"access_key_id"`
	SecretAccessKey  string            `json:"secret_access_key"`
	Region           string            `json:"region"`
	Now              string            `json:"now"`
	Chunks           []docChunk        `json:"chunks"`
	ChunkByte        string            `json:"chunk_byte"`
}

type docChunk struct {
	Size      int    `json:"size"`
	Signature string `json:"signature"`
}

func loadDocExample(t *testing.T, name string) docExample {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var example docExample
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	return example
}

func (d docExample) request(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(d.Method, "http://"+d.Host+d.Target, strings.NewReader(d.Body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range d.Headers {
		req.Header.Set(name, value)
	}
	if d.Authorization != "" {
		req.Header.Set("Authorization", d.Authorization)
	}
	return req
}

func (d docExample) env(t *testing.T) *testEnv {
	t.Helper()
	now, err := time.Parse(time.RFC3339, d.Now)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newFakeSecrets()
	secrets.add(d.AccessKeyID, d.SecretAccessKey)
	clock := &testClock{now: now}
	verifier := NewVerifier(Config{Region: d.Region, Now: clock.Now}, secrets)
	return &testEnv{verifier: verifier, secrets: secrets, clock: clock}
}
