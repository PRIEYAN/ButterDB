package vm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

func fixedNow() time.Time { return testNow }

type fakeClient struct {
	keys      []adminv1.AccessKey
	buckets   []string
	created   adminv1.AccessKeyCreated
	status    adminv1.Status
	statusErr error
	createErr error
	updateErr error
	revokeErr error

	createReqs []adminv1.CreateAccessKeyRequest
	updateReqs []adminv1.UpdateAccessKeyRequest
	revoked    []string
	listCalls  []bool
	statusHits int
}

func (f *fakeClient) ListAccessKeys(_ context.Context, includeRevoked bool) (adminv1.AccessKeyList, error) {
	f.listCalls = append(f.listCalls, includeRevoked)
	var out []adminv1.AccessKey
	for _, k := range f.keys {
		if includeRevoked || k.Status != "revoked" {
			out = append(out, k)
		}
	}
	return adminv1.AccessKeyList{AccessKeys: out}, nil
}

func (f *fakeClient) CreateAccessKey(_ context.Context, req adminv1.CreateAccessKeyRequest) (adminv1.AccessKeyCreated, error) {
	f.createReqs = append(f.createReqs, req)
	if f.createErr != nil {
		return adminv1.AccessKeyCreated{}, f.createErr
	}
	created := f.created
	created.AccessKey.Name, created.AccessKey.Scope = req.Name, req.Scope
	f.keys = append([]adminv1.AccessKey{created.AccessKey}, f.keys...)
	return created, nil
}

func (f *fakeClient) UpdateAccessKey(_ context.Context, id string, req adminv1.UpdateAccessKeyRequest) (adminv1.AccessKey, error) {
	f.updateReqs = append(f.updateReqs, req)
	return adminv1.AccessKey{AccessKeyID: id}, f.updateErr
}

func (f *fakeClient) RevokeAccessKey(_ context.Context, id string) (adminv1.AccessKey, error) {
	f.revoked = append(f.revoked, id)
	if f.revokeErr != nil {
		return adminv1.AccessKey{}, f.revokeErr
	}
	for i := range f.keys {
		if f.keys[i].AccessKeyID == id {
			f.keys[i].Status = "revoked"
		}
	}
	return adminv1.AccessKey{AccessKeyID: id, Status: "revoked"}, nil
}

func (f *fakeClient) ListBuckets(context.Context) (adminv1.BucketList, error) {
	var list adminv1.BucketList
	for _, name := range f.buckets {
		list.Buckets = append(list.Buckets, adminv1.Bucket{Name: name})
	}
	return list, nil
}

func (f *fakeClient) Status(context.Context) (adminv1.Status, error) {
	f.statusHits++
	return f.status, f.statusErr
}

func newCreated() adminv1.AccessKeyCreated {
	return adminv1.AccessKeyCreated{
		AccessKey:       adminv1.AccessKey{AccessKeyID: "BDBK7Q2M9X4T1W8Z3R6Y", Status: "active"},
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		S3Endpoint:      "https://s3.example.com",
		S3Region:        "us-east-1",
	}
}

func apiError(status int, code adminv1.Code, details map[string]any) error {
	return &adminv1.APIError{Status: status, Body: adminv1.ErrorBody{Code: code, Details: details}}
}

func readOnlyScope(buckets ...string) KeyScope {
	return KeyScope{Buckets: buckets, Actions: PresetReadOnly()}
}

func TestCreateReturnsSecretOnce(t *testing.T) {
	client := &fakeClient{created: newCreated()}
	v := New(client, fixedNow)
	if !v.Create(context.Background(), NewKey{Name: "website", Scope: readOnlyScope("photos")}) {
		t.Fatalf("create failed: %+v", v.State().Get().FormProblem)
	}
	s := v.State().Get()
	if s.Secret == nil {
		t.Fatalf("secret missing after create")
	}
	want := Secret{
		AccessKeyID:     "BDBK7Q2M9X4T1W8Z3R6Y",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		S3Endpoint:      "https://s3.example.com",
		Region:          "us-east-1",
	}
	if *s.Secret != want {
		t.Fatalf("secret = %#v", *s.Secret)
	}
	if len(s.Keys) != 1 || s.Keys[0].AccessKeyID != want.AccessKeyID {
		t.Fatalf("list not refreshed: %+v", s.Keys)
	}
	for _, k := range s.Keys {
		if strings.Contains(fmt.Sprintf("%+v", k), want.SecretAccessKey) {
			t.Fatalf("secret leaked into the key list")
		}
	}
	v.Refresh(context.Background())
	if v.State().Get().Secret == nil {
		t.Fatalf("a refresh dropped the secret before it was acknowledged")
	}
	if v.Create(context.Background(), NewKey{Name: "second", Scope: readOnlyScope("photos")}) {
		t.Fatalf("a second key was created while the secret was still pending")
	}
	if len(client.createReqs) != 1 {
		t.Fatalf("create requests = %d", len(client.createReqs))
	}
}

func TestSecretIsRedactedWhenFormatted(t *testing.T) {
	secret := Secret{AccessKeyID: "BDBKID", SecretAccessKey: "topsecretvalue"}
	cases := []struct {
		name string
		text string
	}{
		{name: "percent v", text: fmt.Sprintf("%v", secret)},
		{name: "percent plus v", text: fmt.Sprintf("%+v", secret)},
		{name: "percent sharp v", text: fmt.Sprintf("%#v", secret)},
		{name: "pointer in state", text: fmt.Sprintf("%+v", State{Secret: &secret})},
		{name: "string", text: secret.String()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(tc.text, "topsecretvalue") {
				t.Fatalf("secret leaked: %s", tc.text)
			}
		})
	}
}

func TestSecretFallsBackToStatusEndpoints(t *testing.T) {
	created := newCreated()
	created.S3Endpoint, created.S3Region = "", ""
	client := &fakeClient{created: created}
	client.status.Endpoints = adminv1.Endpoints{S3PublicURL: "https://status.example.com", S3Region: "eu-west-1"}
	v := New(client, fixedNow)
	v.Create(context.Background(), NewKey{Name: "k", Scope: readOnlyScope("photos")})
	s := v.State().Get().Secret
	if s == nil || s.S3Endpoint != "https://status.example.com" || s.Region != "eu-west-1" {
		t.Fatalf("secret = %#v", s)
	}
}

func TestSecretSurvivesStatusFailure(t *testing.T) {
	created := newCreated()
	created.S3Endpoint = ""
	client := &fakeClient{created: created, statusErr: errors.New("status down")}
	v := New(client, fixedNow)
	if !v.Create(context.Background(), NewKey{Name: "k", Scope: readOnlyScope("photos")}) {
		t.Fatalf("create failed")
	}
	s := v.State().Get().Secret
	if s == nil || s.SecretAccessKey == "" {
		t.Fatalf("secret lost when status failed: %#v", s)
	}
	if s.S3Endpoint != "" {
		t.Fatalf("endpoint invented: %q", s.S3Endpoint)
	}
}

func TestSecretDroppedOnDone(t *testing.T) {
	client := &fakeClient{created: newCreated()}
	v := New(client, fixedNow)
	v.Create(context.Background(), NewKey{Name: "website", Scope: readOnlyScope("photos")})
	var seen []*Secret
	v.State().Subscribe(func(s State) { seen = append(seen, s.Secret) })
	v.Done()
	if v.State().Get().Secret != nil {
		t.Fatalf("secret kept after Done")
	}
	if len(seen) != 1 || seen[0] != nil {
		t.Fatalf("subscribers still saw the secret: %v", seen)
	}
	v.Refresh(context.Background())
	if v.State().Get().Secret != nil {
		t.Fatalf("secret came back")
	}
	if !v.Create(context.Background(), NewKey{Name: "second", Scope: readOnlyScope("photos")}) {
		t.Fatalf("a new key could not be created after Done")
	}
}

func TestScopeAllBuckets(t *testing.T) {
	cases := []struct {
		name        string
		scope       KeyScope
		wantBuckets []string
		wantActions []string
	}{
		{
			name:        "all buckets sends the wildcard only",
			scope:       KeyScope{AllBuckets: true, Buckets: []string{"photos", "avatars"}, Actions: PresetReadOnly()},
			wantBuckets: []string{"*"},
			wantActions: []string{"read", "list"},
		},
		{
			name:        "listed buckets are sorted and deduplicated",
			scope:       KeyScope{Buckets: []string{"photos", "avatars", "photos"}, Actions: []string{"delete", "read"}},
			wantBuckets: []string{"avatars", "photos"},
			wantActions: []string{"read", "delete"},
		},
		{
			name:        "read and write preset is all four actions",
			scope:       KeyScope{AllBuckets: true, Actions: PresetReadWrite()},
			wantBuckets: []string{"*"},
			wantActions: []string{"read", "list", "write", "delete"},
		},
		{
			name:        "unknown actions are dropped",
			scope:       KeyScope{AllBuckets: true, Actions: []string{"read", "admin"}},
			wantBuckets: []string{"*"},
			wantActions: []string{"read"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{created: newCreated()}
			v := New(client, fixedNow)
			if !v.Create(context.Background(), NewKey{Name: "k", Scope: tc.scope}) {
				t.Fatalf("create failed: %+v", v.State().Get().FormProblem)
			}
			got := client.createReqs[0].Scope
			if !reflect.DeepEqual(got.Buckets, tc.wantBuckets) || !reflect.DeepEqual(got.Actions, tc.wantActions) {
				t.Fatalf("scope = %+v, want buckets %v actions %v", got, tc.wantBuckets, tc.wantActions)
			}
		})
	}
}

func TestScopePrecheckBlocksSending(t *testing.T) {
	cases := []struct {
		name      string
		in        NewKey
		wantCode  adminv1.Code
		wantField string
	}{
		{name: "no actions", in: NewKey{Name: "k", Scope: KeyScope{AllBuckets: true}}, wantCode: adminv1.CodeScopeInvalid, wantField: "scope"},
		{name: "no buckets", in: NewKey{Name: "k", Scope: KeyScope{Actions: PresetReadOnly()}}, wantCode: adminv1.CodeScopeInvalid, wantField: "scope"},
		{name: "empty name", in: NewKey{Name: "  ", Scope: readOnlyScope("photos")}, wantCode: adminv1.CodeValidationFailed, wantField: "name"},
		{name: "name too long", in: NewKey{Name: strings.Repeat("n", 65), Scope: readOnlyScope("photos")}, wantCode: adminv1.CodeValidationFailed, wantField: "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{created: newCreated()}
			v := New(client, fixedNow)
			if v.Create(context.Background(), tc.in) {
				t.Fatalf("create succeeded")
			}
			if len(client.createReqs) != 0 {
				t.Fatalf("invalid request was sent")
			}
			p := v.State().Get().FormProblem
			if p == nil || p.Code != tc.wantCode || p.Field != tc.wantField || p.Presentation != model.PresentInline {
				t.Fatalf("problem = %+v", p)
			}
		})
	}
}

func TestNameLimitCountsCharacters(t *testing.T) {
	if reason := CheckName(strings.Repeat("é", 64)); reason != "" {
		t.Fatalf("64 characters rejected: %s", reason)
	}
	if reason := CheckName(strings.Repeat("é", 65)); reason == "" {
		t.Fatalf("65 characters accepted")
	}
}

func TestCreateShowsServerError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantInline bool
		wantCode   adminv1.Code
	}{
		{name: "scope rejected inline", err: apiError(http.StatusBadRequest, adminv1.CodeScopeInvalid, nil), wantInline: true, wantCode: adminv1.CodeScopeInvalid},
		{name: "unknown bucket in scope is a validation error", err: apiError(http.StatusBadRequest, adminv1.CodeValidationFailed, map[string]any{"field": "scope", "reason": "no such bucket"}), wantInline: true, wantCode: adminv1.CodeValidationFailed},
		{name: "server error is a dialog", err: apiError(http.StatusInternalServerError, adminv1.CodeInternal, nil), wantCode: adminv1.CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{created: newCreated(), createErr: tc.err}
			v := New(client, fixedNow)
			if v.Create(context.Background(), NewKey{Name: "k", Scope: readOnlyScope("photos")}) {
				t.Fatalf("create succeeded")
			}
			s := v.State().Get()
			p := s.Problem
			if tc.wantInline {
				p = s.FormProblem
			}
			if p == nil || p.Code != tc.wantCode || s.Secret != nil || s.Working {
				t.Fatalf("state = %+v", s)
			}
		})
	}
}

func TestRevokeRefreshes(t *testing.T) {
	client := &fakeClient{keys: []adminv1.AccessKey{
		{AccessKeyID: "BDBKAAA", Name: "a", Status: "active"},
		{AccessKeyID: "BDBKBBB", Name: "b", Status: "active"},
	}}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	if got := len(v.State().Get().Keys); got != 2 {
		t.Fatalf("keys = %d", got)
	}
	if !v.Revoke(context.Background(), "BDBKAAA") {
		t.Fatalf("revoke failed")
	}
	if len(client.revoked) != 1 || client.revoked[0] != "BDBKAAA" {
		t.Fatalf("revoked = %v", client.revoked)
	}
	keys := v.State().Get().Keys
	if len(keys) != 1 || keys[0].AccessKeyID != "BDBKBBB" {
		t.Fatalf("list after revoke = %+v", keys)
	}
}

func TestRevokeNotFoundRefreshesAndKeepsBanner(t *testing.T) {
	client := &fakeClient{
		keys:      []adminv1.AccessKey{{AccessKeyID: "BDBKAAA", Status: "active"}},
		revokeErr: apiError(http.StatusNotFound, adminv1.CodeAccessKeyNotFound, nil),
	}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	client.keys = nil
	if v.Revoke(context.Background(), "BDBKAAA") {
		t.Fatalf("revoke succeeded")
	}
	s := v.State().Get()
	if len(s.Keys) != 0 {
		t.Fatalf("list not refreshed: %+v", s.Keys)
	}
	if s.Problem == nil || s.Problem.Code != adminv1.CodeAccessKeyNotFound || s.Problem.Presentation != model.PresentBanner {
		t.Fatalf("problem = %+v", s.Problem)
	}
}

func TestIncludeRevokedTogglesListCall(t *testing.T) {
	client := &fakeClient{keys: []adminv1.AccessKey{
		{AccessKeyID: "BDBKAAA", Status: "active"},
		{AccessKeyID: "BDBKBBB", Status: "revoked"},
	}}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	if len(v.State().Get().Keys) != 1 {
		t.Fatalf("revoked key listed by default")
	}
	v.SetIncludeRevoked(context.Background(), true)
	if len(v.State().Get().Keys) != 2 || !v.State().Get().IncludeRevoked {
		t.Fatalf("revoked keys not included: %+v", v.State().Get())
	}
	if !reflect.DeepEqual(client.listCalls, []bool{false, true}) {
		t.Fatalf("list calls = %v", client.listCalls)
	}
	v.Refresh(context.Background())
	if client.listCalls[len(client.listCalls)-1] != true {
		t.Fatalf("a plain refresh forgot the toggle")
	}
}

func TestUpdateSendsOnlyChangedParts(t *testing.T) {
	existing := adminv1.AccessKey{
		AccessKeyID: "BDBKAAA", Name: "site", Status: "active",
		Scope: adminv1.Scope{Buckets: []string{"photos"}, Actions: []string{"read", "list"}},
	}
	cases := []struct {
		name      string
		edit      KeyEdit
		wantName  bool
		wantScope bool
		wantSent  bool
	}{
		{name: "nothing changed", edit: KeyEdit{Name: "site", Scope: readOnlyScope("photos")}},
		{name: "same scope in another order", edit: KeyEdit{Name: "site", Scope: KeyScope{Buckets: []string{"photos"}, Actions: []string{"list", "read"}}}},
		{name: "rename only", edit: KeyEdit{Name: "site v2", Scope: readOnlyScope("photos")}, wantName: true, wantSent: true},
		{name: "scope only", edit: KeyEdit{Name: "site", Scope: KeyScope{AllBuckets: true, Actions: PresetReadWrite()}}, wantScope: true, wantSent: true},
		{name: "both", edit: KeyEdit{Name: "other", Scope: readOnlyScope("avatars")}, wantName: true, wantScope: true, wantSent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{keys: []adminv1.AccessKey{existing}}
			v := New(client, fixedNow)
			v.Refresh(context.Background())
			if !v.Update(context.Background(), "BDBKAAA", tc.edit) {
				t.Fatalf("update failed: %+v", v.State().Get().FormProblem)
			}
			if sent := len(client.updateReqs) == 1; sent != tc.wantSent {
				t.Fatalf("sent = %v, want %v", sent, tc.wantSent)
			}
			if !tc.wantSent {
				return
			}
			req := client.updateReqs[0]
			if (req.Name != nil) != tc.wantName || (req.Scope != nil) != tc.wantScope {
				t.Fatalf("request = %+v", req)
			}
		})
	}
}

func TestUpdateScopeInvalidFromServerIsInline(t *testing.T) {
	client := &fakeClient{
		keys:      []adminv1.AccessKey{{AccessKeyID: "BDBKAAA", Name: "site", Status: "active"}},
		updateErr: apiError(http.StatusBadRequest, adminv1.CodeScopeInvalid, nil),
	}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	if v.Update(context.Background(), "BDBKAAA", KeyEdit{Name: "site", Scope: readOnlyScope("photos")}) {
		t.Fatalf("update succeeded")
	}
	if p := v.State().Get().FormProblem; p == nil || p.Field != "scope" {
		t.Fatalf("form problem = %+v", p)
	}
}

func TestLoadBucketsSorted(t *testing.T) {
	client := &fakeClient{buckets: []string{"videos", "avatars", "photos"}}
	v := New(client, fixedNow)
	v.LoadBuckets(context.Background())
	if got := v.State().Get().BucketNames; !reflect.DeepEqual(got, []string{"avatars", "photos", "videos"}) {
		t.Fatalf("buckets = %v", got)
	}
}

func TestScopeTexts(t *testing.T) {
	cases := []struct {
		name        string
		scope       adminv1.Scope
		wantBuckets string
		wantActions string
	}{
		{name: "all buckets", scope: adminv1.Scope{Buckets: []string{"*"}, Actions: []string{"delete", "read"}}, wantBuckets: "All buckets", wantActions: "Read, Delete"},
		{name: "listed", scope: adminv1.Scope{Buckets: []string{"photos", "avatars"}, Actions: []string{"read", "list", "write", "delete"}}, wantBuckets: "photos, avatars", wantActions: "Read, List, Write, Delete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BucketsText(tc.scope); got != tc.wantBuckets {
				t.Errorf("buckets = %q, want %q", got, tc.wantBuckets)
			}
			if got := ActionsText(tc.scope); got != tc.wantActions {
				t.Errorf("actions = %q, want %q", got, tc.wantActions)
			}
		})
	}
}

func TestScopeOfRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		wire adminv1.Scope
	}{
		{name: "wildcard", wire: adminv1.Scope{Buckets: []string{"*"}, Actions: []string{"read"}}},
		{name: "listed", wire: adminv1.Scope{Buckets: []string{"avatars", "photos"}, Actions: []string{"read", "write"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScopeOf(tc.wire).Wire(); !reflect.DeepEqual(got, tc.wire) {
				t.Fatalf("round trip = %+v, want %+v", got, tc.wire)
			}
		})
	}
}

func TestEnvVarCopyFormat(t *testing.T) {
	cases := []struct {
		name   string
		secret Secret
		want   string
	}{
		{
			name: "all fields",
			secret: Secret{
				AccessKeyID: "BDBK7Q2M9X4T1W8Z3R6Y", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
				S3Endpoint: "https://s3.example.com", Region: "us-east-1",
			},
			want: "AWS_ACCESS_KEY_ID=BDBK7Q2M9X4T1W8Z3R6Y\n" +
				"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n" +
				"AWS_ENDPOINT_URL_S3=https://s3.example.com\n" +
				"AWS_REGION=us-east-1",
		},
		{
			name:   "unknown endpoint and region are omitted",
			secret: Secret{AccessKeyID: "BDBKID", SecretAccessKey: "sek"},
			want:   "AWS_ACCESS_KEY_ID=BDBKID\nAWS_SECRET_ACCESS_KEY=sek",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.secret.EnvBlock(); got != tc.want {
				t.Fatalf("env block =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

type fakeClipboard struct{ text string }

func (c *fakeClipboard) SetText(text string) { c.text = text }
func (c *fakeClipboard) Text() string        { return c.text }

type fakeScheduler struct {
	jobs    []*fakeJob
	delays  []time.Duration
	stopped int
}

type fakeJob struct {
	run     func()
	stopped bool
}

func (s *fakeScheduler) schedule(after time.Duration, run func()) func() {
	job := &fakeJob{run: run}
	s.jobs = append(s.jobs, job)
	s.delays = append(s.delays, after)
	return func() {
		job.stopped = true
		s.stopped++
	}
}

func (s *fakeScheduler) fire(i int) {
	if !s.jobs[i].stopped {
		s.jobs[i].run()
	}
}

func TestClipboardGuardClearsUnchangedText(t *testing.T) {
	clip := &fakeClipboard{}
	sched := &fakeScheduler{}
	g := NewClipboardGuard(clip, sched.schedule, ClipboardClearAfter)
	g.SetText("the secret")
	if clip.text != "the secret" || g.Text() != "the secret" {
		t.Fatalf("clipboard = %q", clip.text)
	}
	if sched.delays[0] != 60*time.Second {
		t.Fatalf("delay = %v", sched.delays[0])
	}
	sched.fire(0)
	if clip.text != "" {
		t.Fatalf("clipboard not cleared: %q", clip.text)
	}
}

func TestClipboardGuardLeavesForeignText(t *testing.T) {
	clip := &fakeClipboard{}
	sched := &fakeScheduler{}
	g := NewClipboardGuard(clip, sched.schedule, ClipboardClearAfter)
	g.SetText("the secret")
	clip.text = "something the user copied later"
	sched.fire(0)
	if clip.text != "something the user copied later" {
		t.Fatalf("clipboard overwritten: %q", clip.text)
	}
}

func TestClipboardGuardRestartsTimerOnEachCopy(t *testing.T) {
	clip := &fakeClipboard{}
	sched := &fakeScheduler{}
	g := NewClipboardGuard(clip, sched.schedule, ClipboardClearAfter)
	g.SetText("first")
	g.SetText("second")
	if sched.stopped != 1 || !sched.jobs[0].stopped {
		t.Fatalf("first timer not stopped: %d", sched.stopped)
	}
	sched.fire(0)
	if clip.text != "second" {
		t.Fatalf("stopped timer cleared the clipboard: %q", clip.text)
	}
	sched.fire(1)
	if clip.text != "" {
		t.Fatalf("clipboard not cleared: %q", clip.text)
	}
}
