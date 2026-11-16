package app

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

const (
	testServerID   = "k3v9m2q8t4w7x1z5a6b2c3d4e5"
	testSetupToken = "bdst1_abcdefghijklmnopqrstuvwxyz234567"
	testDeviceID   = "a1b2c3d4e5f6g7h2"
	testDeviceTok  = "bdt1_a1b2c3d4e5f6g7h2_0123456789abcdefghijklmnopqrstuvwxyz0123456789ab"
	testLoginCode  = "12345"
	testAPIHash    = "0123456789abcdef0123456789abcdef"
	testS3Endpoint = "https://s3.example.test"
)

type storedObject struct {
	data        []byte
	contentType string
	modified    time.Time
	version     string
}

type fakeAdmin struct {
	t   *testing.T
	now time.Time
	srv *httptest.Server
	pin string

	mu           sync.Mutex
	claimed      bool
	linked       bool
	telegram     adminv1.TelegramStatus
	codeFailures int
	revoked      bool
	buckets      []adminv1.Bucket
	keys         []adminv1.AccessKey
	objects      map[string]storedObject
	calls        map[string]int
	tokensSeen   map[string]bool
	lastPresign  adminv1.PresignRequest
	putGate      chan struct{}
	putStarted   chan struct{}
	nextVersion  int
}

type fakeAdminOption func(*fakeAdmin)

func withClaimed() fakeAdminOption {
	return func(f *fakeAdmin) { f.claimed = true }
}

func withLinked() fakeAdminOption {
	return func(f *fakeAdmin) {
		f.claimed = true
		f.linked = true
		f.telegram = linkedStatus(f.now)
	}
}

func linkedStatus(now time.Time) adminv1.TelegramStatus {
	return adminv1.TelegramStatus{
		State:       adminv1.TelegramLinked,
		Next:        adminv1.NextNone,
		Credentials: adminv1.TelegramCredentials{APIID: 123456, APIHashSet: true},
		Account: &adminv1.TelegramAccount{
			UserID:      777,
			Username:    "butterowner",
			FirstName:   "Butter",
			PhoneMasked: "+44 ••• ••• 134",
			LinkedAt:    now,
			Connected:   true,
		},
	}
}

func newFakeAdmin(t *testing.T, now time.Time, opts ...fakeAdminOption) *fakeAdmin {
	t.Helper()
	f := &fakeAdmin{
		t:          t,
		now:        now,
		objects:    map[string]storedObject{},
		calls:      map[string]int{},
		tokensSeen: map[string]bool{},
		telegram:   adminv1.TelegramStatus{State: adminv1.TelegramNoCredentials, Next: adminv1.NextApp},
	}
	for _, opt := range opts {
		opt(f)
	}
	f.srv = httptest.NewTLSServer(f.routes())
	t.Cleanup(f.srv.Close)
	sum := sha256.Sum256(f.srv.Certificate().RawSubjectPublicKeyInfo)
	f.pin = "SHA256:" + hex.EncodeToString(sum[:])
	return f
}

func (f *fakeAdmin) host() string {
	return strings.TrimPrefix(f.srv.URL, "https://")
}

func (f *fakeAdmin) callCount(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method+" "+path]
}

func (f *fakeAdmin) callsWithPrefix(method, prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for call, n := range f.calls {
		if strings.HasPrefix(call, method+" "+prefix) {
			total += n
		}
	}
	return total
}

func (f *fakeAdmin) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, n := range f.calls {
		total += n
	}
	return total
}

func (f *fakeAdmin) setRevoked(revoked bool) {
	f.mu.Lock()
	f.revoked = revoked
	f.mu.Unlock()
}

func (f *fakeAdmin) object(bucket, key string) (storedObject, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[bucket+"\x00"+key]
	return o, ok
}

func (f *fakeAdmin) bucketNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.buckets))
	for _, b := range f.buckets {
		names = append(names, b.Name)
	}
	return names
}

func (f *fakeAdmin) keyIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.keys))
	for _, k := range f.keys {
		ids = append(ids, k.AccessKeyID)
	}
	return ids
}

func (f *fakeAdmin) tokenWasPresented(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokensSeen[token]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, code adminv1.Code, message string, details map[string]any) {
	writeJSON(w, status, adminv1.ErrorEnvelope{Error: adminv1.ErrorBody{
		Code:      code,
		Message:   message,
		Retryable: status == http.StatusUnprocessableEntity,
		Details:   details,
		RequestID: "req-test",
	}})
}

func (f *fakeAdmin) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[r.Method+" "+r.URL.Path]++
}

func (f *fakeAdmin) authorised(w http.ResponseWriter, r *http.Request) bool {
	token := strings.TrimPrefix(r.Header.Get(adminv1.HeaderAuthorization), adminv1.BearerPrefix)
	f.mu.Lock()
	revoked := f.revoked
	if token != "" {
		f.tokensSeen[token] = true
	}
	f.mu.Unlock()
	if revoked {
		writeAPIError(w, http.StatusForbidden, adminv1.CodeDeviceRevoked, "device revoked", map[string]any{"revoked_at": f.now.Format(time.RFC3339)})
		return false
	}
	if token != testDeviceTok {
		writeAPIError(w, http.StatusUnauthorized, adminv1.CodeUnauthenticated, "missing or invalid device token", nil)
		return false
	}
	return true
}

func (f *fakeAdmin) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if f.authorised(w, r) {
			h(w, r)
		}
	}
}

func (f *fakeAdmin) open(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		h(w, r)
	}
}

func (f *fakeAdmin) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+adminv1.PathServerInfo, f.open(f.serverInfo))
	mux.HandleFunc("POST "+adminv1.PathSetupClaim, f.open(f.claim))
	mux.HandleFunc("GET "+adminv1.PathDevices, f.authed(f.listDevices))
	mux.HandleFunc("GET "+adminv1.PathSetupTelegram, f.authed(f.telegramStatus))
	mux.HandleFunc("POST "+adminv1.PathSetupTelegramApp, f.authed(f.telegramSaveApp))
	mux.HandleFunc("POST "+adminv1.PathSetupTelegramPhone, f.authed(f.telegramStart))
	mux.HandleFunc("POST "+adminv1.PathSetupTelegramCode, f.authed(f.telegramCode))
	mux.HandleFunc("POST "+adminv1.PathSetupTelegramCancel, f.authed(f.telegramStatus))
	mux.HandleFunc("GET "+adminv1.PathStatus, f.authed(f.status))
	mux.HandleFunc("GET "+adminv1.PathUsage, f.authed(f.usage))
	mux.HandleFunc("GET "+adminv1.PathBuckets, f.authed(f.listBuckets))
	mux.HandleFunc("POST "+adminv1.PathBuckets, f.authed(f.createBucket))
	mux.HandleFunc("GET "+adminv1.PathBucket, f.authed(f.getBucket))
	mux.HandleFunc("PATCH "+adminv1.PathBucket, f.authed(f.updateBucket))
	mux.HandleFunc("DELETE "+adminv1.PathBucket, f.authed(f.deleteBucket))
	mux.HandleFunc("GET "+adminv1.PathAccessKeys, f.authed(f.listKeys))
	mux.HandleFunc("POST "+adminv1.PathAccessKeys, f.authed(f.createKey))
	mux.HandleFunc("DELETE "+adminv1.PathAccessKey, f.authed(f.revokeKey))
	mux.HandleFunc("GET "+adminv1.PathBucketObjects, f.authed(f.listObjects))
	mux.HandleFunc("POST "+adminv1.PathBucketObjectsDelete, f.authed(f.deleteObjects))
	mux.HandleFunc("PUT "+adminv1.PathBucketObject, f.authed(f.putObject))
	mux.HandleFunc("DELETE "+adminv1.PathBucketObject, f.authed(f.deleteObject))
	mux.HandleFunc("GET "+adminv1.PathBucketObject, f.authed(f.getObject))
	mux.HandleFunc("HEAD "+adminv1.PathBucketObject, f.authed(f.headObject))
	mux.HandleFunc("GET "+adminv1.PathBucketObjectMeta, f.authed(f.objectMeta))
	mux.HandleFunc("POST "+adminv1.PathBucketPresign, f.authed(f.presign))
	return mux
}

func (f *fakeAdmin) serverInfo(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	info := adminv1.ServerInfo{
		ServerID:       testServerID,
		APIVersion:     "v1",
		ServerVersion:  "0.1.0",
		State:          "unclaimed",
		TelegramLinked: f.linked,
		PairingMethods: []string{},
		TLS:            adminv1.TLSInfo{Mode: "self_signed", SPKISHA256: f.pin},
		Time:           f.now,
	}
	if f.claimed {
		info.State = "claimed"
		info.PairingMethods = []string{"host"}
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, info)
}

func (f *fakeAdmin) claim(w http.ResponseWriter, r *http.Request) {
	var req adminv1.ClaimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, adminv1.CodeBadRequest, "bad body", nil)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed {
		writeAPIError(w, http.StatusConflict, adminv1.CodeAlreadyClaimed, "already claimed", nil)
		return
	}
	if req.SetupToken != testSetupToken {
		writeAPIError(w, http.StatusUnauthorized, adminv1.CodeSetupTokenInvalid, "setup token is not valid", nil)
		return
	}
	f.claimed = true
	writeJSON(w, http.StatusCreated, adminv1.DeviceCredentials{
		Device: adminv1.Device{
			DeviceID:      testDeviceID,
			Name:          req.DeviceName,
			Platform:      req.Platform,
			CreatedAt:     f.now,
			CreatedVia:    "setup_claim",
			LastSeenAt:    f.now,
			TokenIssuedAt: f.now,
			Current:       true,
		},
		DeviceToken: testDeviceTok,
		ServerID:    testServerID,
	})
}

func (f *fakeAdmin) listDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, adminv1.DeviceList{Devices: []adminv1.Device{{
		DeviceID:      testDeviceID,
		Name:          "Test laptop",
		Platform:      "linux/amd64",
		CreatedAt:     f.now,
		CreatedVia:    "setup_claim",
		LastSeenAt:    f.now,
		TokenIssuedAt: f.now,
		Current:       true,
	}}})
}

func (f *fakeAdmin) telegramStatus(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	status := f.telegram
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (f *fakeAdmin) telegramSaveApp(w http.ResponseWriter, r *http.Request) {
	var req adminv1.TelegramCredentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.APIID <= 0 || req.APIHash != testAPIHash {
		writeAPIError(w, http.StatusUnprocessableEntity, adminv1.CodeValidationFailed, "invalid app credentials", map[string]any{"field": "api_id"})
		return
	}
	f.mu.Lock()
	f.telegram = adminv1.TelegramStatus{
		State:       adminv1.TelegramReady,
		Next:        adminv1.NextPhone,
		Credentials: adminv1.TelegramCredentials{APIID: req.APIID, APIHashSet: true},
	}
	status := f.telegram
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (f *fakeAdmin) telegramStart(w http.ResponseWriter, r *http.Request) {
	var req adminv1.TelegramLoginStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Phone == "" {
		writeAPIError(w, http.StatusUnprocessableEntity, adminv1.CodeTelegramPhoneInvalid, "phone is not valid", nil)
		return
	}
	f.mu.Lock()
	creds := f.telegram.Credentials
	f.codeFailures = 0
	f.telegram = adminv1.TelegramStatus{
		State:       adminv1.TelegramAwaitingCode,
		Next:        adminv1.NextCode,
		Credentials: creds,
		Login: &adminv1.TelegramLogin{
			LoginID:              "login-1",
			PhoneMasked:          "+44 ••• ••• 134",
			CodeDelivery:         adminv1.CodeDelivery{Type: "app", Length: len(testLoginCode), TimeoutSeconds: 120},
			CodeAttemptsLeft:     5,
			PasswordAttemptsLeft: 5,
			StartedAt:            f.now,
			ExpiresAt:            f.now.Add(10 * time.Minute),
		},
	}
	status := f.telegram
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (f *fakeAdmin) telegramCode(w http.ResponseWriter, r *http.Request) {
	var req adminv1.TelegramCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, adminv1.CodeBadRequest, "bad body", nil)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.telegram.Login == nil || req.LoginID != f.telegram.Login.LoginID {
		writeAPIError(w, http.StatusConflict, adminv1.CodeTelegramLoginNotActive, "no login is active", nil)
		return
	}
	if req.Code != testLoginCode {
		f.codeFailures++
		left := 5 - f.codeFailures
		login := *f.telegram.Login
		login.CodeAttemptsLeft = left
		f.telegram.Login = &login
		writeAPIError(w, http.StatusUnprocessableEntity, adminv1.CodeTelegramCodeInvalid, "code is not right", map[string]any{"attempts_left": left})
		return
	}
	f.linked = true
	f.telegram = linkedStatus(f.now)
	writeJSON(w, http.StatusOK, f.telegram)
}

func (f *fakeAdmin) status(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	linked := f.linked
	f.mu.Unlock()
	status := adminv1.Status{
		ServerID:      testServerID,
		ServerVersion: "0.1.0",
		Commit:        "abc1234",
		StartedAt:     f.now.Add(-time.Hour),
		UptimeSeconds: 3600,
		State:         "ready",
		Telegram:      adminv1.TelegramHealth{Linked: linked, Connected: linked, SendRatePerMinute: 20},
		Components:    []adminv1.Component{{Name: "projection", Status: "ok", Since: f.now}},
		Endpoints:     adminv1.Endpoints{S3PublicURL: testS3Endpoint, S3Region: "us-east-1"},
		Limits: adminv1.Limits{
			MaxObjectBytes:       1900 * 1024 * 1024,
			MaxKeyBytes:          1024,
			MaxUserMetadataBytes: 2048,
			MaxPresignSeconds:    604800,
			MaxBuckets:           100,
			MaxDeleteBatch:       1000,
		},
		Replica: adminv1.ReplicaSummary{State: "not_linked"},
	}
	writeJSON(w, http.StatusOK, status)
}

func (f *fakeAdmin) usage(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	buckets := len(f.buckets)
	objects := int64(len(f.objects))
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, adminv1.Usage{Buckets: buckets, Objects: objects, ComputedAt: f.now})
}

func (f *fakeAdmin) listBuckets(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	list := adminv1.BucketList{Buckets: append([]adminv1.Bucket{}, f.buckets...)}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAdmin) createBucket(w http.ResponseWriter, r *http.Request) {
	var req adminv1.CreateBucketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, adminv1.CodeBadRequest, "bad body", nil)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.buckets {
		if b.Name == req.Name {
			writeAPIError(w, http.StatusConflict, adminv1.CodeBucketExists, "bucket exists", nil)
			return
		}
	}
	bucket := adminv1.Bucket{
		Name:               req.Name,
		CreatedAt:          f.now,
		Policy:             "private",
		EncryptionDefault:  "none",
		CacheMaxAgeSeconds: 3600,
		TelegramChannelID:  int64(1000 + len(f.buckets)),
	}
	if req.Policy != "" {
		bucket.Policy = req.Policy
	}
	if req.EncryptionDefault != "" {
		bucket.EncryptionDefault = req.EncryptionDefault
	}
	if req.CacheMaxAgeSeconds != nil {
		bucket.CacheMaxAgeSeconds = *req.CacheMaxAgeSeconds
	}
	f.buckets = append(f.buckets, bucket)
	writeJSON(w, http.StatusCreated, bucket)
}

func (f *fakeAdmin) getBucket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue(adminv1.ParamBucket)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.buckets {
		if b.Name == name {
			writeJSON(w, http.StatusOK, b)
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, adminv1.CodeBucketNotFound, "no such bucket", nil)
}

func (f *fakeAdmin) updateBucket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue(adminv1.ParamBucket)
	var req adminv1.UpdateBucketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, adminv1.CodeBadRequest, "bad body", nil)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.buckets {
		if f.buckets[i].Name != name {
			continue
		}
		if req.Policy != nil {
			f.buckets[i].Policy = *req.Policy
		}
		if req.EncryptionDefault != nil {
			f.buckets[i].EncryptionDefault = *req.EncryptionDefault
		}
		if req.CacheMaxAgeSeconds != nil {
			f.buckets[i].CacheMaxAgeSeconds = *req.CacheMaxAgeSeconds
		}
		writeJSON(w, http.StatusOK, f.buckets[i])
		return
	}
	writeAPIError(w, http.StatusNotFound, adminv1.CodeBucketNotFound, "no such bucket", nil)
}

func (f *fakeAdmin) revokeKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue(adminv1.ParamAccessKeyID)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.keys {
		if f.keys[i].AccessKeyID != id {
			continue
		}
		revoked := f.now
		f.keys[i].Status = "revoked"
		f.keys[i].RevokedAt = &revoked
		writeJSON(w, http.StatusOK, f.keys[i])
		return
	}
	writeAPIError(w, http.StatusNotFound, adminv1.CodeAccessKeyNotFound, "no such key", nil)
}

func (f *fakeAdmin) keyStatus(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.AccessKeyID == id {
			return k.Status
		}
	}
	return ""
}

func (f *fakeAdmin) bucketPolicy(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.buckets {
		if b.Name == name {
			return b.Policy
		}
	}
	return ""
}

func (f *fakeAdmin) deleteBucket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue(adminv1.ParamBucket)
	f.mu.Lock()
	defer f.mu.Unlock()
	for key := range f.objects {
		if strings.HasPrefix(key, name+"\x00") {
			writeAPIError(w, http.StatusConflict, adminv1.CodeBucketNotEmpty, "bucket is not empty", map[string]any{"object_count": 1})
			return
		}
	}
	for i, b := range f.buckets {
		if b.Name == name {
			f.buckets = append(f.buckets[:i], f.buckets[i+1:]...)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, adminv1.CodeBucketNotFound, "no such bucket", nil)
}

func (f *fakeAdmin) listKeys(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	list := adminv1.AccessKeyList{AccessKeys: append([]adminv1.AccessKey{}, f.keys...)}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

func (f *fakeAdmin) createKey(w http.ResponseWriter, r *http.Request) {
	var req adminv1.CreateAccessKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, adminv1.CodeBadRequest, "bad body", nil)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := adminv1.AccessKey{
		AccessKeyID: "BDKEYAAAAAAAAAA" + strconv.Itoa(len(f.keys)),
		Name:        req.Name,
		CreatedAt:   f.now,
		Status:      "active",
		Scope:       req.Scope,
	}
	f.keys = append(f.keys, key)
	writeJSON(w, http.StatusCreated, adminv1.AccessKeyCreated{
		AccessKey:       key,
		SecretAccessKey: "s3cr3t-shown-once-0123456789abcdefghijklmnop",
		S3Endpoint:      testS3Endpoint,
		S3Region:        "us-east-1",
	})
}

func (f *fakeAdmin) listObjects(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue(adminv1.ParamBucket)
	prefix := r.URL.Query().Get(adminv1.QueryPrefix)
	delimiter := r.URL.Query().Get(adminv1.QueryDelimiter)
	list := adminv1.ObjectList{Bucket: bucket, Prefix: prefix, Delimiter: delimiter, Objects: []adminv1.ObjectSummary{}, CommonPrefixes: []string{}}
	f.mu.Lock()
	folders := map[string]bool{}
	var keys []string
	for composite := range f.objects {
		b, key, _ := strings.Cut(composite, "\x00")
		if b == bucket && strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		rest := strings.TrimPrefix(key, prefix)
		if delimiter != "" {
			if cut := strings.Index(rest, delimiter); cut >= 0 {
				folders[prefix+rest[:cut+1]] = true
				continue
			}
		}
		o := f.objects[bucket+"\x00"+key]
		list.Objects = append(list.Objects, adminv1.ObjectSummary{
			Key:          key,
			SizeBytes:    int64(len(o.data)),
			ETag:         md5Hex(o.data),
			LastModified: o.modified,
			ContentType:  o.contentType,
		})
	}
	f.mu.Unlock()
	for folder := range folders {
		list.CommonPrefixes = append(list.CommonPrefixes, folder)
	}
	sort.Strings(list.CommonPrefixes)
	writeJSON(w, http.StatusOK, list)
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func (f *fakeAdmin) putObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue(adminv1.ParamBucket)
	key := r.URL.Query().Get(adminv1.QueryKey)
	f.mu.Lock()
	gate, started := f.putGate, f.putStarted
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, adminv1.CodeBadRequest, "body read failed", nil)
		return
	}
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	composite := bucket + "\x00" + key
	if _, exists := f.objects[composite]; exists && r.Header.Get("If-None-Match") == "*" {
		writeAPIError(w, http.StatusPreconditionFailed, adminv1.CodePreconditionFailed, "object exists", nil)
		return
	}
	f.nextVersion++
	stored := storedObject{
		data:        data,
		contentType: r.Header.Get("Content-Type"),
		modified:    f.now,
		version:     "v" + strconv.Itoa(f.nextVersion),
	}
	f.objects[composite] = stored
	writeJSON(w, http.StatusOK, f.metaOf(bucket, key, stored))
}

func (f *fakeAdmin) metaOf(bucket, key string, o storedObject) adminv1.ObjectMeta {
	sum := sha256.Sum256(o.data)
	return adminv1.ObjectMeta{
		Bucket:       bucket,
		Key:          key,
		VersionID:    o.version,
		SizeBytes:    int64(len(o.data)),
		StoredBytes:  int64(len(o.data)),
		ETag:         `"` + md5Hex(o.data) + `"`,
		SHA256:       hex.EncodeToString(sum[:]),
		ContentType:  o.contentType,
		LastModified: o.modified,
		UserMetadata: map[string]string{},
	}
}

func (f *fakeAdmin) lookup(r *http.Request) (string, string, storedObject, bool) {
	bucket := r.PathValue(adminv1.ParamBucket)
	key := r.URL.Query().Get(adminv1.QueryKey)
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[bucket+"\x00"+key]
	return bucket, key, o, ok
}

func (f *fakeAdmin) headObject(w http.ResponseWriter, r *http.Request) {
	_, _, o, ok := f.lookup(r)
	if !ok {
		w.Header().Set(adminv1.HeaderErrorCode, string(adminv1.CodeObjectNotFound))
		w.WriteHeader(http.StatusNotFound)
		return
	}
	setObjectHeaders(w, o)
	w.WriteHeader(http.StatusOK)
}

func setObjectHeaders(w http.ResponseWriter, o storedObject) {
	w.Header().Set("ETag", `"`+md5Hex(o.data)+`"`)
	w.Header().Set("Content-Type", o.contentType)
	w.Header().Set("Last-Modified", o.modified.UTC().Format(http.TimeFormat))
	w.Header().Set(adminv1.HeaderVersionID, o.version)
}

func (f *fakeAdmin) getObject(w http.ResponseWriter, r *http.Request) {
	_, _, o, ok := f.lookup(r)
	if !ok {
		writeAPIError(w, http.StatusNotFound, adminv1.CodeObjectNotFound, "no such object", nil)
		return
	}
	setObjectHeaders(w, o)
	w.Header().Set("Content-Length", strconv.Itoa(len(o.data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(o.data)
}

func (f *fakeAdmin) objectMeta(w http.ResponseWriter, r *http.Request) {
	bucket, key, o, ok := f.lookup(r)
	if !ok {
		writeAPIError(w, http.StatusNotFound, adminv1.CodeObjectNotFound, "no such object", nil)
		return
	}
	writeJSON(w, http.StatusOK, f.metaOf(bucket, key, o))
}

func (f *fakeAdmin) deleteObject(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue(adminv1.ParamBucket)
	key := r.URL.Query().Get(adminv1.QueryKey)
	f.mu.Lock()
	delete(f.objects, bucket+"\x00"+key)
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeAdmin) deleteObjects(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue(adminv1.ParamBucket)
	var req adminv1.DeleteObjectsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, adminv1.CodeBadRequest, "bad body", nil)
		return
	}
	result := adminv1.DeleteObjectsResult{Deleted: []string{}, Errors: []adminv1.DeleteObjectError{}}
	f.mu.Lock()
	for _, key := range req.Keys {
		delete(f.objects, bucket+"\x00"+key)
		result.Deleted = append(result.Deleted, key)
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, result)
}

func (f *fakeAdmin) presign(w http.ResponseWriter, r *http.Request) {
	var req adminv1.PresignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, adminv1.CodeBadRequest, "bad body", nil)
		return
	}
	bucket := r.PathValue(adminv1.ParamBucket)
	f.mu.Lock()
	f.lastPresign = req
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, adminv1.PresignedURL{
		URL:       testS3Endpoint + "/" + bucket + "/" + req.Key + "?X-Amz-Signature=canned",
		Method:    http.MethodGet,
		ExpiresAt: f.now.Add(time.Duration(req.ExpiresInSeconds) * time.Second),
	})
}

func (f *fakeAdmin) seedBucket(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buckets = append(f.buckets, adminv1.Bucket{
		Name: name, CreatedAt: f.now, Policy: "private", EncryptionDefault: "none",
		CacheMaxAgeSeconds: 3600, TelegramChannelID: int64(1000 + len(f.buckets)),
	})
}

func (f *fakeAdmin) seedKey(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, adminv1.AccessKey{
		AccessKeyID: "BDKEYAAAAAAAAAA" + strconv.Itoa(len(f.keys)),
		Name:        name,
		CreatedAt:   f.now,
		Status:      "active",
		Scope:       adminv1.Scope{Buckets: []string{"*"}, Actions: []string{"read", "list"}},
	})
}
