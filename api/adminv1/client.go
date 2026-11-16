package adminv1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultJSONTimeout = 75 * time.Second
	maxErrorBodyBytes  = 1 << 20
	maxJSONBodyBytes   = 64 << 20
	modulePath         = "github.com/PRIEYAN/ButterDB"
	timestampLayout    = "2006-01-02T15:04:05.000Z"
)

type ClientConfig struct {
	BaseURL       string
	Trust         Trust
	Token         string
	UserAgent     string
	JSONTimeout   time.Duration
	HTTPTransport http.RoundTripper
}

type Client struct {
	base      string
	http      *http.Client
	userAgent string
	timeout   time.Duration
	mu        sync.RWMutex
	token     string
}

type callSpec struct {
	method    string
	path      string
	query     url.Values
	body      any
	anonymous bool
}

func NewClient(cfg ClientConfig) (*Client, error) {
	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.JSONTimeout < 0 {
		return nil, errors.New("adminv1: json timeout must not be negative")
	}
	timeout := cfg.JSONTimeout
	if timeout == 0 {
		timeout = defaultJSONTimeout
	}
	transport, err := resolveTransport(base, cfg.Trust, cfg.HTTPTransport)
	if err != nil {
		return nil, err
	}
	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent()
	}
	return &Client{
		base: strings.TrimSuffix(base.String(), "/"),
		http: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		userAgent: userAgent,
		timeout:   timeout,
		token:     cfg.Token,
	}, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("adminv1: base url is not valid")
	}
	if base.Host == "" || base.Hostname() == "" {
		return nil, errors.New("adminv1: base url needs a host")
	}
	if base.User != nil {
		return nil, errors.New("adminv1: base url must not carry credentials")
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("adminv1: base url must not carry a query or fragment")
	}
	switch base.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(base.Hostname()) {
			return nil, errors.New("adminv1: plain http is allowed only for localhost, 127.0.0.1 and ::1")
		}
	default:
		return nil, errors.New("adminv1: base url scheme must be https")
	}
	return base, nil
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}

func resolveTransport(base *url.URL, trust Trust, custom http.RoundTripper) (http.RoundTripper, error) {
	if base.Scheme == "http" {
		if custom != nil {
			return custom, nil
		}
		return newTransport(base, trust)
	}
	tlsConfig, err := newTLSConfig(trust)
	if err != nil {
		return nil, err
	}
	if custom == nil {
		return newTransport(base, trust)
	}
	if trust.Mode != TrustPinned {
		return custom, nil
	}
	source, ok := custom.(*http.Transport)
	if !ok {
		return nil, errors.New("adminv1: pinned trust needs an *http.Transport or no custom transport")
	}
	if source.DialTLS != nil || source.DialTLSContext != nil {
		return nil, errors.New("adminv1: pinned trust cannot be combined with a custom tls dialer")
	}
	pinned := source.Clone()
	pinned.TLSClientConfig = tlsConfig
	return pinned, nil
}

func newTransport(base *url.URL, trust Trust) (http.RoundTripper, error) {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if base.Scheme == "http" {
		return transport, nil
	}
	tlsConfig, err := newTLSConfig(trust)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}

func defaultUserAgent() string {
	version := "devel"
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Path == modulePath && info.Main.Version != "" {
			version = info.Main.Version
		}
		for _, dep := range info.Deps {
			if dep.Path == modulePath && dep.Version != "" {
				version = dep.Version
			}
		}
	}
	return "butterdb-adminv1/" + version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

func (c *Client) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

func (c *Client) currentToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

func (c *Client) newRequest(ctx context.Context, spec callSpec, body io.Reader) (*http.Request, error) {
	target := c.base + spec.path
	if len(spec.query) > 0 {
		target += "?" + spec.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, target, body)
	if err != nil {
		return nil, errors.New("adminv1: build request failed")
	}
	req.Header.Set("User-Agent", c.userAgent)
	if !spec.anonymous {
		if token := c.currentToken(); token != "" {
			req.Header.Set(HeaderAuthorization, BearerPrefix+token)
		}
	}
	return req, nil
}

func (c *Client) send(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err == nil {
		return resp, nil
	}
	var mismatch *PinMismatchError
	if errors.As(err, &mismatch) {
		return nil, mismatch
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return nil, fmt.Errorf("adminv1: %s %s: %w", req.Method, req.URL.EscapedPath(), err)
}

func (c *Client) doJSON(ctx context.Context, spec callSpec, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var body io.Reader
	if spec.body != nil {
		payload, err := json.Marshal(spec.body)
		if err != nil {
			return fmt.Errorf("adminv1: encode request: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	req, err := c.newRequest(ctx, spec, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.send(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.apiError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxJSONBodyBytes))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBodyBytes)).Decode(out); err != nil {
		return fmt.Errorf("adminv1: decode %s response: %w", spec.method, err)
	}
	return nil
}

func (c *Client) apiError(resp *http.Response) *APIError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	var envelope ErrorEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Error.Code == "" {
		envelope = ErrorEnvelope{Error: fallbackBody(resp.StatusCode)}
	}
	return c.finishAPIError(resp, envelope.Error)
}

func (c *Client) headerError(resp *http.Response) *APIError {
	body := fallbackBody(resp.StatusCode)
	if code := resp.Header.Get(HeaderErrorCode); code != "" {
		body.Code = Code(code)
	}
	return c.finishAPIError(resp, body)
}

func (c *Client) finishAPIError(resp *http.Response, body ErrorBody) *APIError {
	if body.RequestID == "" {
		body.RequestID = resp.Header.Get(HeaderRequestID)
	}
	if body.RetryAfterSeconds <= 0 {
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			body.RetryAfterSeconds = seconds
		}
	}
	if token := c.currentToken(); token != "" {
		body.Message = strings.ReplaceAll(body.Message, token, "[redacted]")
		body.Hint = strings.ReplaceAll(body.Hint, token, "[redacted]")
	}
	return &APIError{Status: resp.StatusCode, Body: body}
}

func fallbackBody(status int) ErrorBody {
	body := ErrorBody{Message: http.StatusText(status)}
	switch {
	case status == http.StatusUnauthorized:
		body.Code = CodeUnauthenticated
	case status == http.StatusNotFound:
		body.Code = CodeNotFound
	case status == http.StatusMethodNotAllowed:
		body.Code = CodeMethodNotAllowed
	case status == http.StatusTooManyRequests:
		body.Code = CodeRateLimited
		body.Retryable = true
	case status == http.StatusNotImplemented:
		body.Code = CodeNotImplemented
	case status >= 500:
		body.Code = CodeInternal
		body.Retryable = status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
	default:
		body.Code = CodeBadRequest
	}
	return body
}

func call[T any](ctx context.Context, c *Client, spec callSpec) (T, error) {
	var out T
	err := c.doJSON(ctx, spec, &out)
	return out, err
}

func pathWith(template string, params ...string) (string, error) {
	pairs := make([]string, 0, len(params))
	for i := 0; i+1 < len(params); i += 2 {
		if params[i+1] == "" {
			return "", fmt.Errorf("adminv1: empty %s", params[i])
		}
		pairs = append(pairs, "{"+params[i]+"}", url.PathEscape(params[i+1]))
	}
	return strings.NewReplacer(pairs...).Replace(template), nil
}

func (c *Client) ServerInfo(ctx context.Context) (ServerInfo, error) {
	return call[ServerInfo](ctx, c, callSpec{method: http.MethodGet, path: PathServerInfo, anonymous: true})
}

func (c *Client) Claim(ctx context.Context, req ClaimRequest) (DeviceCredentials, error) {
	return call[DeviceCredentials](ctx, c, callSpec{method: http.MethodPost, path: PathSetupClaim, body: req, anonymous: true})
}

func (c *Client) RequestPairing(ctx context.Context, req PairingRequest) (PairingRequested, error) {
	return call[PairingRequested](ctx, c, callSpec{method: http.MethodPost, path: PathPairingRequest, body: req, anonymous: true})
}

func (c *Client) CompletePairing(ctx context.Context, req PairingCompleteRequest) (DeviceCredentials, error) {
	return call[DeviceCredentials](ctx, c, callSpec{method: http.MethodPost, path: PathPairingComplete, body: req, anonymous: true})
}

func (c *Client) ListDevices(ctx context.Context) (DeviceList, error) {
	return call[DeviceList](ctx, c, callSpec{method: http.MethodGet, path: PathDevices})
}

func (c *Client) RenameDevice(ctx context.Context, deviceID string, req UpdateDeviceRequest) (Device, error) {
	path, err := pathWith(PathDevice, ParamDeviceID, deviceID)
	if err != nil {
		return Device{}, err
	}
	return call[Device](ctx, c, callSpec{method: http.MethodPatch, path: path, body: req})
}

func (c *Client) RevokeDevice(ctx context.Context, deviceID string, confirmLast bool) error {
	path, err := pathWith(PathDevice, ParamDeviceID, deviceID)
	if err != nil {
		return err
	}
	spec := callSpec{method: http.MethodDelete, path: path}
	if confirmLast {
		spec.query = url.Values{QueryConfirmLast: {"true"}}
	}
	return c.doJSON(ctx, spec, nil)
}

func (c *Client) RotateDeviceToken(ctx context.Context) (DeviceCredentials, error) {
	return call[DeviceCredentials](ctx, c, callSpec{method: http.MethodPost, path: PathDeviceSelfRotate})
}

func (c *Client) TelegramStatus(ctx context.Context) (TelegramStatus, error) {
	return call[TelegramStatus](ctx, c, callSpec{method: http.MethodGet, path: PathSetupTelegram})
}

func (c *Client) SaveTelegramCredentials(ctx context.Context, req TelegramCredentialsRequest) (TelegramStatus, error) {
	return call[TelegramStatus](ctx, c, callSpec{method: http.MethodPost, path: PathSetupTelegramApp, body: req})
}

func (c *Client) DeleteTelegramCredentials(ctx context.Context) (TelegramStatus, error) {
	return call[TelegramStatus](ctx, c, callSpec{method: http.MethodDelete, path: PathSetupTelegramApp})
}

func (c *Client) StartTelegramLogin(ctx context.Context, req TelegramLoginStartRequest) (TelegramStatus, error) {
	return call[TelegramStatus](ctx, c, callSpec{method: http.MethodPost, path: PathSetupTelegramPhone, body: req})
}

func (c *Client) SubmitTelegramCode(ctx context.Context, req TelegramCodeRequest) (TelegramStatus, error) {
	return call[TelegramStatus](ctx, c, callSpec{method: http.MethodPost, path: PathSetupTelegramCode, body: req})
}

func (c *Client) SubmitTelegramPassword(ctx context.Context, req TelegramPasswordRequest) (TelegramStatus, error) {
	return call[TelegramStatus](ctx, c, callSpec{method: http.MethodPost, path: PathSetupTelegramPassword, body: req})
}

func (c *Client) CancelTelegramLogin(ctx context.Context) (TelegramStatus, error) {
	return call[TelegramStatus](ctx, c, callSpec{method: http.MethodPost, path: PathSetupTelegramCancel})
}

func (c *Client) UnlinkTelegram(ctx context.Context) (TelegramStatus, error) {
	return call[TelegramStatus](ctx, c, callSpec{method: http.MethodPost, path: PathSetupTelegramUnlink, body: UnlinkRequest{Confirm: UnlinkConfirmation}})
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	return call[Status](ctx, c, callSpec{method: http.MethodGet, path: PathStatus})
}

func (c *Client) Usage(ctx context.Context) (Usage, error) {
	return call[Usage](ctx, c, callSpec{method: http.MethodGet, path: PathUsage})
}

func (c *Client) ListBuckets(ctx context.Context) (BucketList, error) {
	return call[BucketList](ctx, c, callSpec{method: http.MethodGet, path: PathBuckets})
}

func (c *Client) CreateBucket(ctx context.Context, req CreateBucketRequest) (Bucket, error) {
	return call[Bucket](ctx, c, callSpec{method: http.MethodPost, path: PathBuckets, body: req})
}

func (c *Client) GetBucket(ctx context.Context, name string) (Bucket, error) {
	path, err := pathWith(PathBucket, ParamBucket, name)
	if err != nil {
		return Bucket{}, err
	}
	return call[Bucket](ctx, c, callSpec{method: http.MethodGet, path: path})
}

func (c *Client) UpdateBucket(ctx context.Context, name string, req UpdateBucketRequest) (Bucket, error) {
	path, err := pathWith(PathBucket, ParamBucket, name)
	if err != nil {
		return Bucket{}, err
	}
	return call[Bucket](ctx, c, callSpec{method: http.MethodPatch, path: path, body: req})
}

func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	path, err := pathWith(PathBucket, ParamBucket, name)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, callSpec{method: http.MethodDelete, path: path}, nil)
}

func (c *Client) ListAccessKeys(ctx context.Context, includeRevoked bool) (AccessKeyList, error) {
	spec := callSpec{method: http.MethodGet, path: PathAccessKeys}
	if includeRevoked {
		spec.query = url.Values{QueryIncludeRevoked: {"true"}}
	}
	return call[AccessKeyList](ctx, c, spec)
}

func (c *Client) CreateAccessKey(ctx context.Context, req CreateAccessKeyRequest) (AccessKeyCreated, error) {
	return call[AccessKeyCreated](ctx, c, callSpec{method: http.MethodPost, path: PathAccessKeys, body: req})
}

func (c *Client) GetAccessKey(ctx context.Context, id string) (AccessKey, error) {
	path, err := pathWith(PathAccessKey, ParamAccessKeyID, id)
	if err != nil {
		return AccessKey{}, err
	}
	return call[AccessKey](ctx, c, callSpec{method: http.MethodGet, path: path})
}

func (c *Client) UpdateAccessKey(ctx context.Context, id string, req UpdateAccessKeyRequest) (AccessKey, error) {
	path, err := pathWith(PathAccessKey, ParamAccessKeyID, id)
	if err != nil {
		return AccessKey{}, err
	}
	return call[AccessKey](ctx, c, callSpec{method: http.MethodPatch, path: path, body: req})
}

func (c *Client) RevokeAccessKey(ctx context.Context, id string) (AccessKey, error) {
	path, err := pathWith(PathAccessKey, ParamAccessKeyID, id)
	if err != nil {
		return AccessKey{}, err
	}
	return call[AccessKey](ctx, c, callSpec{method: http.MethodDelete, path: path})
}

func (c *Client) Audit(ctx context.Context, q AuditQuery) (AuditPage, error) {
	query := url.Values{}
	if q.Cursor != "" {
		query.Set(QueryCursor, q.Cursor)
	}
	if q.Limit > 0 {
		query.Set(QueryLimit, strconv.Itoa(q.Limit))
	}
	if q.Action != "" {
		query.Set(QueryAction, q.Action)
	}
	if !q.Since.IsZero() {
		query.Set(QuerySince, q.Since.UTC().Format(timestampLayout))
	}
	if !q.Until.IsZero() {
		query.Set(QueryUntil, q.Until.UTC().Format(timestampLayout))
	}
	return call[AuditPage](ctx, c, callSpec{method: http.MethodGet, path: PathAudit, query: query})
}

func (c *Client) ReplicaStatus(ctx context.Context) (ReplicaStatus, error) {
	return call[ReplicaStatus](ctx, c, callSpec{method: http.MethodGet, path: PathReplica})
}

func (c *Client) StartReplicaLogin(ctx context.Context, req ReplicaLoginStartRequest) (ReplicaStatus, error) {
	return call[ReplicaStatus](ctx, c, callSpec{method: http.MethodPost, path: PathReplicaTelegramPhone, body: req})
}

func (c *Client) SubmitReplicaCode(ctx context.Context, req TelegramCodeRequest) (ReplicaStatus, error) {
	return call[ReplicaStatus](ctx, c, callSpec{method: http.MethodPost, path: PathReplicaTelegramCode, body: req})
}

func (c *Client) SubmitReplicaPassword(ctx context.Context, req TelegramPasswordRequest) (ReplicaStatus, error) {
	return call[ReplicaStatus](ctx, c, callSpec{method: http.MethodPost, path: PathReplicaTelegramPass, body: req})
}

func (c *Client) CancelReplicaLogin(ctx context.Context) (ReplicaStatus, error) {
	return call[ReplicaStatus](ctx, c, callSpec{method: http.MethodPost, path: PathReplicaTelegramCancel})
}

func (c *Client) UnlinkReplica(ctx context.Context) (ReplicaStatus, error) {
	return call[ReplicaStatus](ctx, c, callSpec{method: http.MethodPost, path: PathReplicaUnlink, body: UnlinkRequest{Confirm: UnlinkConfirmation}})
}
