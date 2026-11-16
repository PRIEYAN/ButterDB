package vm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

func fixedNow() time.Time { return testNow }

type fakeClient struct {
	buckets   []adminv1.Bucket
	listErr   error
	createErr error
	updateErr error
	deleteErr error

	created []adminv1.CreateBucketRequest
	updated []adminv1.UpdateBucketRequest
	deleted []string
	lists   int
}

func (f *fakeClient) ListBuckets(context.Context) (adminv1.BucketList, error) {
	f.lists++
	if f.listErr != nil {
		return adminv1.BucketList{}, f.listErr
	}
	return adminv1.BucketList{Buckets: append([]adminv1.Bucket(nil), f.buckets...)}, nil
}

func (f *fakeClient) CreateBucket(_ context.Context, req adminv1.CreateBucketRequest) (adminv1.Bucket, error) {
	f.created = append(f.created, req)
	if f.createErr != nil {
		return adminv1.Bucket{}, f.createErr
	}
	b := adminv1.Bucket{Name: req.Name, Policy: req.Policy, EncryptionDefault: req.EncryptionDefault}
	f.buckets = append(f.buckets, b)
	return b, nil
}

func (f *fakeClient) GetBucket(context.Context, string) (adminv1.Bucket, error) {
	return adminv1.Bucket{}, errors.New("unused")
}

func (f *fakeClient) UpdateBucket(_ context.Context, name string, req adminv1.UpdateBucketRequest) (adminv1.Bucket, error) {
	f.updated = append(f.updated, req)
	return adminv1.Bucket{Name: name}, f.updateErr
}

func (f *fakeClient) DeleteBucket(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return f.deleteErr
}

func apiError(status int, code adminv1.Code, details map[string]any) error {
	return &adminv1.APIError{Status: status, Body: adminv1.ErrorBody{Code: code, Details: details}}
}

func names(buckets []adminv1.Bucket) []string {
	out := make([]string, len(buckets))
	for i, b := range buckets {
		out[i] = b.Name
	}
	return out
}

func equalStrings(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

func TestListSorted(t *testing.T) {
	early := testNow.Add(-48 * time.Hour)
	late := testNow.Add(-1 * time.Hour)
	client := &fakeClient{buckets: []adminv1.Bucket{
		{Name: "photos", ObjectCount: 30, StoredBytes: 900, CreatedAt: late},
		{Name: "avatars", ObjectCount: 5, StoredBytes: 100, CreatedAt: late},
		{Name: "videos", ObjectCount: 30, StoredBytes: 5000, CreatedAt: early},
	}}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	cases := []struct {
		name   string
		sort   []SortColumn
		wantBy []string
		desc   bool
	}{
		{name: "default by name ascending", sort: nil, wantBy: []string{"avatars", "photos", "videos"}},
		{name: "name toggles to descending", sort: []SortColumn{ColumnName}, wantBy: []string{"videos", "photos", "avatars"}, desc: true},
		{name: "size ascending", sort: []SortColumn{ColumnSize}, wantBy: []string{"avatars", "photos", "videos"}},
		{name: "size descending", sort: []SortColumn{ColumnSize, ColumnSize}, wantBy: []string{"videos", "photos", "avatars"}, desc: true},
		{name: "objects ties break by name", sort: []SortColumn{ColumnObjects}, wantBy: []string{"avatars", "photos", "videos"}},
		{name: "created oldest first", sort: []SortColumn{ColumnCreated}, wantBy: []string{"videos", "avatars", "photos"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := New(client, fixedNow)
			v.Refresh(context.Background())
			for _, column := range tc.sort {
				v.Sort(column)
			}
			s := v.State().Get()
			if got := names(s.Buckets); !equalStrings(got, tc.wantBy) {
				t.Fatalf("order = %v, want %v", got, tc.wantBy)
			}
			if s.SortDescending != tc.desc {
				t.Fatalf("descending = %v, want %v", s.SortDescending, tc.desc)
			}
		})
	}
	if got := names(v.State().Get().Buckets); !equalStrings(got, []string{"avatars", "photos", "videos"}) {
		t.Fatalf("refresh order = %v", got)
	}
}

func TestListRefreshFailureShowsProblem(t *testing.T) {
	client := &fakeClient{listErr: apiError(http.StatusInternalServerError, adminv1.CodeInternal, nil)}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	s := v.State().Get()
	if s.Loading || s.Problem == nil || s.Problem.Code != adminv1.CodeInternal {
		t.Fatalf("state = %+v", s)
	}
}

func TestListCanceledShowsNoProblem(t *testing.T) {
	client := &fakeClient{listErr: context.Canceled}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	if s := v.State().Get(); s.Problem != nil || s.Loading {
		t.Fatalf("state = %+v", s)
	}
}

func TestNamePrecheck(t *testing.T) {
	cases := []struct {
		name   string
		bucket string
		want   string
	}{
		{name: "valid simple", bucket: "photos"},
		{name: "valid with dots and hyphens", bucket: "my-photos.2026"},
		{name: "valid with digits", bucket: "a1b"},
		{name: "valid at 63 characters", bucket: strings.Repeat("a", 63)},
		{name: "empty", bucket: "", want: reasonNameLength},
		{name: "too short", bucket: "ab", want: reasonNameLength},
		{name: "too long", bucket: strings.Repeat("a", 64), want: reasonNameLength},
		{name: "uppercase", bucket: "Photos", want: reasonNameChars},
		{name: "underscore", bucket: "my_photos", want: reasonNameChars},
		{name: "space", bucket: "my photos", want: reasonNameChars},
		{name: "starts with hyphen", bucket: "-photos", want: reasonNameEnds},
		{name: "ends with hyphen", bucket: "photos-", want: reasonNameEnds},
		{name: "starts with dot", bucket: ".photos", want: reasonNameEnds},
		{name: "ends with dot", bucket: "photos.", want: reasonNameEnds},
		{name: "double dot", bucket: "my..photos", want: reasonNameDots},
		{name: "ipv4 address", bucket: "192.168.1.1", want: reasonNameAddress},
		{name: "three digit groups is a name", bucket: "192.168.1", want: ""},
		{name: "four groups with a letter is a name", bucket: "192.168.1.x", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckName(tc.bucket); got != tc.want {
				t.Fatalf("CheckName(%q) = %q, want %q", tc.bucket, got, tc.want)
			}
		})
	}
}

func TestNamePrecheckBlocksSending(t *testing.T) {
	client := &fakeClient{}
	v := New(client, fixedNow)
	in := DefaultNewBucket()
	in.Name = "Bad Name"
	if v.Create(context.Background(), in) {
		t.Fatalf("create succeeded for an invalid name")
	}
	if len(client.created) != 0 {
		t.Fatalf("invalid name was sent to the server")
	}
	s := v.State().Get()
	if s.Working || s.FormProblem == nil {
		t.Fatalf("state = %+v", s)
	}
	if s.FormProblem.Field != "name" || s.FormProblem.Presentation != model.PresentInline {
		t.Fatalf("problem = %+v", *s.FormProblem)
	}
	if !strings.Contains(s.FormProblem.Title, reasonNameChars) {
		t.Fatalf("title = %q", s.FormProblem.Title)
	}
}

func TestCachePrecheck(t *testing.T) {
	cases := []struct {
		name    string
		seconds int
		wantBad bool
	}{
		{name: "zero", seconds: 0},
		{name: "one hour", seconds: 3600},
		{name: "maximum", seconds: MaxCacheMaxAgeSeconds},
		{name: "negative", seconds: -1, wantBad: true},
		{name: "above maximum", seconds: MaxCacheMaxAgeSeconds + 1, wantBad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{}
			v := New(client, fixedNow)
			in := DefaultNewBucket()
			in.Name = "photos"
			in.CacheMaxAgeSeconds = tc.seconds
			ok := v.Create(context.Background(), in)
			if ok == tc.wantBad {
				t.Fatalf("Create ok = %v, wantBad %v", ok, tc.wantBad)
			}
			if tc.wantBad && len(client.created) != 0 {
				t.Fatalf("out of range cache max-age was sent")
			}
		})
	}
}

func TestCreateSendsSettingsAndRefreshes(t *testing.T) {
	client := &fakeClient{}
	v := New(client, fixedNow)
	in := NewBucket{Name: "photos", PublicRead: true, Encrypted: true, CacheMaxAgeSeconds: 86400}
	if !v.Create(context.Background(), in) {
		t.Fatalf("create failed: %+v", v.State().Get().FormProblem)
	}
	req := client.created[0]
	if req.Name != "photos" || req.Policy != PolicyPublicRead || req.EncryptionDefault != EncryptionTDE1 {
		t.Fatalf("request = %+v", req)
	}
	if req.CacheMaxAgeSeconds == nil || *req.CacheMaxAgeSeconds != 86400 {
		t.Fatalf("cache = %v", req.CacheMaxAgeSeconds)
	}
	if client.lists != 1 || len(v.State().Get().Buckets) != 1 {
		t.Fatalf("lists = %d, buckets = %d", client.lists, len(v.State().Get().Buckets))
	}
}

func TestCreateShowsServerError(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantCode     adminv1.Code
		wantInline   bool
		wantField    string
		wantTitleHas string
	}{
		{
			name:         "name already exists is inline on name",
			err:          apiError(http.StatusConflict, adminv1.CodeBucketExists, nil),
			wantCode:     adminv1.CodeBucketExists,
			wantInline:   true,
			wantField:    "name",
			wantTitleHas: "A bucket with this name exists.",
		},
		{
			name:         "server disagrees about name",
			err:          apiError(http.StatusBadRequest, adminv1.CodeBucketNameInvalid, map[string]any{"reason": "reserved prefix"}),
			wantCode:     adminv1.CodeBucketNameInvalid,
			wantInline:   true,
			wantField:    "name",
			wantTitleHas: "reserved prefix",
		},
		{
			name:         "limit reached is a dialog problem",
			err:          apiError(http.StatusConflict, adminv1.CodeBucketLimitReached, map[string]any{"limit": float64(50)}),
			wantCode:     adminv1.CodeBucketLimitReached,
			wantTitleHas: "at most 50 buckets",
		},
		{
			name:     "telegram not linked is a route problem",
			err:      apiError(http.StatusConflict, adminv1.CodeTelegramNotLinked, nil),
			wantCode: adminv1.CodeTelegramNotLinked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{createErr: tc.err}
			v := New(client, fixedNow)
			in := DefaultNewBucket()
			in.Name = "photos"
			if v.Create(context.Background(), in) {
				t.Fatalf("create succeeded")
			}
			s := v.State().Get()
			if s.Working {
				t.Fatalf("still working")
			}
			p := s.Problem
			if tc.wantInline {
				p = s.FormProblem
				if s.Problem != nil {
					t.Fatalf("inline problem leaked into the screen problem: %+v", *s.Problem)
				}
			}
			if p == nil || p.Code != tc.wantCode {
				t.Fatalf("problem = %+v", p)
			}
			if tc.wantInline && p.Field != tc.wantField {
				t.Fatalf("field = %q, want %q", p.Field, tc.wantField)
			}
			if !strings.Contains(p.Title, tc.wantTitleHas) {
				t.Fatalf("title = %q, want it to contain %q", p.Title, tc.wantTitleHas)
			}
		})
	}
}

func TestCreateTransportErrorIsRetryableBanner(t *testing.T) {
	client := &fakeClient{createErr: context.DeadlineExceeded}
	v := New(client, fixedNow)
	in := DefaultNewBucket()
	in.Name = "photos"
	v.Create(context.Background(), in)
	p := v.State().Get().Problem
	if p == nil || !p.Retryable || p.Presentation != model.PresentBanner {
		t.Fatalf("problem = %+v", p)
	}
}

func TestUpdateSendsOnlyChangedFields(t *testing.T) {
	client := &fakeClient{buckets: []adminv1.Bucket{
		{Name: "photos", Policy: PolicyPrivate, EncryptionDefault: EncryptionNone, CacheMaxAgeSeconds: 3600},
	}}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	if !v.Update(context.Background(), "photos", Settings{PublicRead: true, Encrypted: false, CacheMaxAgeSeconds: 3600}) {
		t.Fatalf("update failed")
	}
	req := client.updated[0]
	if req.Policy == nil || *req.Policy != PolicyPublicRead {
		t.Fatalf("policy = %v", req.Policy)
	}
	if req.EncryptionDefault != nil || req.CacheMaxAgeSeconds != nil {
		t.Fatalf("unchanged fields were sent: %+v", req)
	}
}

func TestUpdateWithoutChangesSendsNothing(t *testing.T) {
	client := &fakeClient{buckets: []adminv1.Bucket{
		{Name: "photos", Policy: PolicyPrivate, EncryptionDefault: EncryptionTDE1, CacheMaxAgeSeconds: 60},
	}}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	if !v.Update(context.Background(), "photos", SettingsOf(client.buckets[0])) {
		t.Fatalf("update failed")
	}
	if len(client.updated) != 0 {
		t.Fatalf("an unchanged update was sent")
	}
}

func TestUpdateBucketGoneRefreshesAndKeepsBanner(t *testing.T) {
	client := &fakeClient{
		buckets:   []adminv1.Bucket{{Name: "photos"}},
		updateErr: apiError(http.StatusNotFound, adminv1.CodeBucketNotFound, nil),
	}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	client.buckets = nil
	if v.Update(context.Background(), "photos", Settings{PublicRead: true, CacheMaxAgeSeconds: 10}) {
		t.Fatalf("update succeeded")
	}
	s := v.State().Get()
	if len(s.Buckets) != 0 {
		t.Fatalf("list was not refreshed: %v", names(s.Buckets))
	}
	if s.Problem == nil || s.Problem.Code != adminv1.CodeBucketNotFound || s.Problem.Presentation != model.PresentBanner {
		t.Fatalf("problem = %+v", s.Problem)
	}
}

func TestDeleteRefreshes(t *testing.T) {
	client := &fakeClient{buckets: []adminv1.Bucket{{Name: "photos"}, {Name: "avatars"}}}
	v := New(client, fixedNow)
	v.Refresh(context.Background())
	client.buckets = client.buckets[:1]
	if !v.Delete(context.Background(), "avatars") {
		t.Fatalf("delete failed")
	}
	if len(client.deleted) != 1 || client.deleted[0] != "avatars" {
		t.Fatalf("deleted = %v", client.deleted)
	}
	if got := len(v.State().Get().Buckets); got != 1 {
		t.Fatalf("buckets = %d after refresh", got)
	}
}

func TestDeleteNotEmpty(t *testing.T) {
	cases := []struct {
		name      string
		listed    []adminv1.Bucket
		details   map[string]any
		wantCount int64
		wantTitle string
	}{
		{
			name:      "server reports the count",
			details:   map[string]any{"object_count": float64(12)},
			wantCount: 12,
			wantTitle: "12 objects remain. Delete them first.",
		},
		{
			name:      "count falls back to the listed bucket",
			listed:    []adminv1.Bucket{{Name: "photos", ObjectCount: 7}},
			wantCount: 7,
			wantTitle: "7 objects remain. Delete them first.",
		},
		{
			name:      "count unknown",
			wantCount: -1,
			wantTitle: "This bucket still holds objects. Delete them first.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{
				buckets:   tc.listed,
				deleteErr: apiError(http.StatusConflict, adminv1.CodeBucketNotEmpty, tc.details),
			}
			v := New(client, fixedNow)
			v.Refresh(context.Background())
			if v.Delete(context.Background(), "photos") {
				t.Fatalf("delete succeeded")
			}
			s := v.State().Get()
			if s.NotEmpty == nil {
				t.Fatalf("not-empty state missing: %+v", s)
			}
			if s.NotEmpty.Bucket != "photos" || s.NotEmpty.ObjectCount != tc.wantCount {
				t.Fatalf("not empty = %+v", *s.NotEmpty)
			}
			if s.NotEmpty.Problem.Title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", s.NotEmpty.Problem.Title, tc.wantTitle)
			}
			if s.Problem != nil || s.Working {
				t.Fatalf("state = %+v", s)
			}
			if strings.Contains(s.NotEmpty.Problem.Title, "adminv1") {
				t.Fatalf("raw error text leaked: %q", s.NotEmpty.Problem.Title)
			}
			v.DismissNotEmpty()
			if v.State().Get().NotEmpty != nil {
				t.Fatalf("not-empty state not dismissed")
			}
		})
	}
}

func TestStateObservableNotifies(t *testing.T) {
	client := &fakeClient{buckets: []adminv1.Bucket{{Name: "photos"}}}
	v := New(client, fixedNow)
	var seen []bool
	cancel := v.State().Subscribe(func(s State) { seen = append(seen, s.Loading) })
	v.Refresh(context.Background())
	cancel()
	v.Refresh(context.Background())
	if len(seen) != 2 || !seen[0] || seen[1] {
		t.Fatalf("seen = %v", seen)
	}
}

func TestFormatCacheMaxAge(t *testing.T) {
	cases := []struct {
		name    string
		seconds int
		want    string
	}{
		{name: "zero", seconds: 0, want: "0 s"},
		{name: "seconds", seconds: 45, want: "45 s"},
		{name: "minutes", seconds: 300, want: "5 min"},
		{name: "one hour", seconds: 3600, want: "1 h"},
		{name: "ninety minutes", seconds: 5400, want: "90 min"},
		{name: "one day", seconds: 86400, want: "1 day"},
		{name: "seven days", seconds: 604800, want: "7 days"},
		{name: "odd seconds", seconds: 3601, want: "3601 s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatCacheMaxAge(tc.seconds); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCachePresetsAreInRange(t *testing.T) {
	for _, p := range CachePresets() {
		if reason := CheckCacheMaxAge(p.Seconds); reason != "" {
			t.Errorf("preset %q out of range: %s", p.Label, reason)
		}
	}
}
