package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

type fakeBuckets struct {
	infos map[bucket.BucketName]bucket.Info
	err   error
}

func (f *fakeBuckets) Lookup(_ context.Context, name bucket.BucketName) (bucket.Info, bool, error) {
	if f.err != nil {
		return bucket.Info{}, false, f.err
	}
	info, ok := f.infos[name]
	return info, ok, nil
}

type listingBuckets struct {
	fakeBuckets
	listed []bucket.Info
}

func (l *listingBuckets) ListLive(context.Context) ([]bucket.Info, error) {
	return l.listed, nil
}

func newFake(policies map[string]string) *fakeBuckets {
	fake := &fakeBuckets{infos: map[bucket.BucketName]bucket.Info{}}
	for name, policy := range policies {
		fake.infos[bucket.BucketName(name)] = bucket.Info{Name: bucket.BucketName(name), Policy: policy}
	}
	return fake
}

func principal(t *testing.T, buckets []string, actions []string) Principal {
	t.Helper()
	scope, err := access.NewScope(buckets, actions)
	if err != nil {
		t.Fatal(err)
	}
	return Principal{Key: access.KeyInfo{Scope: scope}}
}

func TestScopeBucketOutsideDenied(t *testing.T) {
	evaluator := NewEvaluator(newFake(map[string]string{"avatars": bucket.PolicyPrivate}))
	p := principal(t, []string{"photos"}, []string{"read", "list", "write", "delete"})
	tests := []struct {
		name   string
		bucket bucket.BucketName
	}{
		{name: "existing bucket outside scope", bucket: "avatars"},
		{name: "missing bucket outside scope", bucket: "ghosts"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := evaluator.Authorize(context.Background(), p, test.bucket, access.ActionRead)
			if !errors.Is(err, errs.ErrAccessDenied) {
				t.Fatalf("Authorize() error = %v, want ErrAccessDenied", err)
			}
		})
	}
}

func TestScopeWildcard(t *testing.T) {
	evaluator := NewEvaluator(newFake(map[string]string{"photos": bucket.PolicyPrivate, "avatars": bucket.PolicyPublicRead}))
	p := principal(t, []string{"*"}, []string{"read"})
	for _, name := range []bucket.BucketName{"photos", "avatars"} {
		t.Run(string(name), func(t *testing.T) {
			info, err := evaluator.Authorize(context.Background(), p, name, access.ActionRead)
			if err != nil || info.Name != name {
				t.Fatalf("Authorize() = %+v, %v", info, err)
			}
		})
	}
}

func TestMissingInScopeBucketNotFound(t *testing.T) {
	tests := []struct {
		name    string
		buckets []string
		actions []string
	}{
		{name: "named scope with action", buckets: []string{"photos"}, actions: []string{"read"}},
		{name: "named scope without action", buckets: []string{"photos"}, actions: []string{"write"}},
		{name: "wildcard scope", buckets: []string{"*"}, actions: []string{"read"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluator := NewEvaluator(newFake(nil))
			_, err := evaluator.Authorize(context.Background(), principal(t, test.buckets, test.actions), "photos", access.ActionRead)
			if !errors.Is(err, errs.ErrNoSuchBucket) {
				t.Fatalf("Authorize() error = %v, want ErrNoSuchBucket", err)
			}
		})
	}
}

func TestActionMatrix(t *testing.T) {
	evaluator := NewEvaluator(newFake(map[string]string{"photos": bucket.PolicyPrivate}))
	tests := []struct {
		name      string
		operation string
		required  access.Action
	}{
		{name: "GetObject", operation: "GetObject", required: access.ActionRead},
		{name: "HeadObject", operation: "HeadObject", required: access.ActionRead},
		{name: "GetObjectAcl", operation: "GetObjectAcl", required: access.ActionRead},
		{name: "GetObjectTagging", operation: "GetObjectTagging", required: access.ActionRead},
		{name: "CopyObject source", operation: "CopyObject source", required: access.ActionRead},
		{name: "UploadPartCopy source", operation: "UploadPartCopy source", required: access.ActionRead},
		{name: "ListObjectsV2", operation: "ListObjectsV2", required: access.ActionList},
		{name: "ListObjects", operation: "ListObjects", required: access.ActionList},
		{name: "HeadBucket", operation: "HeadBucket", required: access.ActionList},
		{name: "GetBucketLocation", operation: "GetBucketLocation", required: access.ActionList},
		{name: "ListMultipartUploads", operation: "ListMultipartUploads", required: access.ActionList},
		{name: "ListParts", operation: "ListParts", required: access.ActionList},
		{name: "GetBucketVersioning", operation: "GetBucketVersioning", required: access.ActionList},
		{name: "GetBucketPolicy", operation: "GetBucketPolicy", required: access.ActionList},
		{name: "GetBucketPolicyStatus", operation: "GetBucketPolicyStatus", required: access.ActionList},
		{name: "GetBucketAcl", operation: "GetBucketAcl", required: access.ActionList},
		{name: "GetBucketCors", operation: "GetBucketCors", required: access.ActionList},
		{name: "GetBucketEncryption", operation: "GetBucketEncryption", required: access.ActionList},
		{name: "other bucket configuration getter", operation: "GetBucketTagging", required: access.ActionList},
		{name: "PutObject", operation: "PutObject", required: access.ActionWrite},
		{name: "CopyObject destination", operation: "CopyObject destination", required: access.ActionWrite},
		{name: "UploadPartCopy destination", operation: "UploadPartCopy destination", required: access.ActionWrite},
		{name: "CreateMultipartUpload", operation: "CreateMultipartUpload", required: access.ActionWrite},
		{name: "UploadPart", operation: "UploadPart", required: access.ActionWrite},
		{name: "CompleteMultipartUpload", operation: "CompleteMultipartUpload", required: access.ActionWrite},
		{name: "AbortMultipartUpload", operation: "AbortMultipartUpload", required: access.ActionWrite},
		{name: "PutObjectAcl", operation: "PutObjectAcl", required: access.ActionWrite},
		{name: "PutBucketAcl", operation: "PutBucketAcl", required: access.ActionWrite},
		{name: "PutObjectTagging", operation: "PutObjectTagging", required: access.ActionWrite},
		{name: "DeleteObjectTagging", operation: "DeleteObjectTagging", required: access.ActionWrite},
		{name: "DeleteObject", operation: "DeleteObject", required: access.ActionDelete},
		{name: "DeleteObjects", operation: "DeleteObjects", required: access.ActionDelete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			granted := principal(t, []string{"photos"}, []string{test.required.String()})
			if _, err := evaluator.Authorize(context.Background(), granted, "photos", test.required); err != nil {
				t.Fatalf("Authorize() with %s granted: %v", test.required, err)
			}
			for _, other := range access.AllActions() {
				if other == test.required {
					continue
				}
				only := principal(t, []string{"photos"}, []string{other.String()})
				_, err := evaluator.Authorize(context.Background(), only, "photos", test.required)
				if !errors.Is(err, errs.ErrAccessDenied) {
					t.Fatalf("Authorize() holding only %s error = %v, want ErrAccessDenied", other, err)
				}
			}
		})
	}
}

func TestAnonymousPublicReadGetOnly(t *testing.T) {
	evaluator := NewEvaluator(newFake(map[string]string{"site": bucket.PolicyPublicRead}))
	info, err := evaluator.AuthorizeAnonymousRead(context.Background(), "site")
	if err != nil || info.Name != "site" {
		t.Fatalf("AuthorizeAnonymousRead() = %+v, %v", info, err)
	}
	anonymous := Principal{Anonymous: true}
	for _, action := range access.AllActions() {
		t.Run("authorize "+action.String(), func(t *testing.T) {
			if _, err := evaluator.Authorize(context.Background(), anonymous, "site", action); !errors.Is(err, errs.ErrAccessDenied) {
				t.Fatalf("Authorize() error = %v, want ErrAccessDenied", err)
			}
		})
	}
	t.Run("anonymous bucket listing", func(t *testing.T) {
		if _, err := evaluator.VisibleBuckets(context.Background(), anonymous); !errors.Is(err, errs.ErrAccessDenied) {
			t.Fatalf("VisibleBuckets() error = %v, want ErrAccessDenied", err)
		}
	})
}

func TestAnonymousPrivateDenied(t *testing.T) {
	evaluator := NewEvaluator(newFake(map[string]string{"vault": bucket.PolicyPrivate}))
	_, err := evaluator.AuthorizeAnonymousRead(context.Background(), "vault")
	if !errors.Is(err, errs.ErrAccessDenied) {
		t.Fatalf("AuthorizeAnonymousRead() error = %v, want ErrAccessDenied", err)
	}
}

func TestAnonymousMissingBucketDenied(t *testing.T) {
	evaluator := NewEvaluator(newFake(nil))
	_, err := evaluator.AuthorizeAnonymousRead(context.Background(), "ghosts")
	if !errors.Is(err, errs.ErrAccessDenied) || errors.Is(err, errs.ErrNoSuchBucket) {
		t.Fatalf("AuthorizeAnonymousRead() error = %v, want ErrAccessDenied only", err)
	}
}

func TestVisibleBucketsFiltersByScope(t *testing.T) {
	fake := newFake(map[string]string{"alpha": bucket.PolicyPrivate, "bravo": bucket.PolicyPrivate, "charlie": bucket.PolicyPublicRead})
	lister := &listingBuckets{fakeBuckets: *fake, listed: []bucket.Info{
		fake.infos["alpha"], fake.infos["bravo"], fake.infos["charlie"],
	}}
	tests := []struct {
		name    string
		source  BucketSource
		buckets []string
		want    []bucket.BucketName
		wantErr error
	}{
		{name: "wildcard through lister", source: lister, buckets: []string{"*"}, want: []bucket.BucketName{"alpha", "bravo", "charlie"}},
		{name: "named through lister", source: lister, buckets: []string{"charlie", "alpha", "ghosts"}, want: []bucket.BucketName{"alpha", "charlie"}},
		{name: "named through lookup", source: fake, buckets: []string{"charlie", "alpha", "ghosts"}, want: []bucket.BucketName{"alpha", "charlie"}},
		{name: "wildcard without lister", source: fake, buckets: []string{"*"}, wantErr: errs.ErrMissingDependency},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluator := NewEvaluator(test.source)
			got, err := evaluator.VisibleBuckets(context.Background(), principal(t, test.buckets, []string{"read"}))
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("VisibleBuckets() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("VisibleBuckets() = %+v, want %v", got, test.want)
			}
			for i, name := range test.want {
				if got[i].Name != name {
					t.Fatalf("VisibleBuckets()[%d] = %q, want %q", i, got[i].Name, name)
				}
			}
		})
	}
}

func TestLookupFailureIsNotAccessDecision(t *testing.T) {
	boom := errors.New("boom")
	evaluator := NewEvaluator(&fakeBuckets{err: boom})
	p := principal(t, []string{"photos"}, []string{"read"})
	if _, err := evaluator.Authorize(context.Background(), p, "photos", access.ActionRead); !errors.Is(err, boom) {
		t.Fatalf("Authorize() error = %v, want wrapped lookup failure", err)
	}
	if _, err := evaluator.AuthorizeAnonymousRead(context.Background(), "photos"); !errors.Is(err, boom) {
		t.Fatalf("AuthorizeAnonymousRead() error = %v, want wrapped lookup failure", err)
	}
}

func TestNilBucketSourceReportsMissingDependency(t *testing.T) {
	evaluator := NewEvaluator(nil)
	p := principal(t, []string{"photos"}, []string{"read"})
	if _, err := evaluator.Authorize(context.Background(), p, "photos", access.ActionRead); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("Authorize() error = %v, want ErrMissingDependency", err)
	}
}
