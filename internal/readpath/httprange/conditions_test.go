package httprange

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

var (
	testNow          = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	testLastModified = time.Date(2026, 1, 1, 12, 0, 0, 500, time.UTC)
	testEntity       = Entity{ETag: "d41d8cd98f00b204e9800998ecf8427e", LastModified: testLastModified}
)

func httpDate(t time.Time) string {
	return t.UTC().Format(http.TimeFormat)
}

type outcome int

const (
	outcomeProceed outcome = iota
	outcomeNotModified
	outcomePreconditionFailed
	outcomeIgnoreRange
)

func TestConditionalPrecedence(t *testing.T) {
	matching := `"d41d8cd98f00b204e9800998ecf8427e"`
	other := `"ffffffffffffffffffffffffffffffff"`
	same := httpDate(testLastModified)
	before := httpDate(testLastModified.Add(-time.Hour))
	after := httpDate(testLastModified.Add(time.Hour))
	future := httpDate(testNow.Add(time.Hour))

	tests := []struct {
		name       string
		conditions Conditions
		want       outcome
		condition  string
	}{
		{name: "no conditions", want: outcomeProceed},
		{name: "step 1 if-match not matched", conditions: Conditions{IfMatch: []string{other}}, want: outcomePreconditionFailed, condition: "If-Match"},
		{name: "step 1 if-match matched", conditions: Conditions{IfMatch: []string{matching}}, want: outcomeProceed},
		{name: "step 1 if-match unquoted matched", conditions: Conditions{IfMatch: []string{"d41d8cd98f00b204e9800998ecf8427e"}}, want: outcomeProceed},
		{name: "step 1 if-match star matches existing object", conditions: Conditions{IfMatch: []string{"*"}}, want: outcomeProceed},
		{name: "step 1 if-match uses strong comparison", conditions: Conditions{IfMatch: []string{"W/" + matching}}, want: outcomePreconditionFailed, condition: "If-Match"},
		{name: "step 1 if-match list with one match", conditions: Conditions{IfMatch: []string{other + ", " + matching}}, want: outcomeProceed},
		{name: "step 1 matched skips step 2 so 200 despite false if-unmodified-since", conditions: Conditions{IfMatch: []string{matching}, IfUnmodifiedSince: before}, want: outcomeProceed},
		{name: "step 1 not matched wins over step 3", conditions: Conditions{IfMatch: []string{other}, IfNoneMatch: []string{matching}}, want: outcomePreconditionFailed, condition: "If-Match"},
		{name: "step 2 if-unmodified-since false", conditions: Conditions{IfUnmodifiedSince: before}, want: outcomePreconditionFailed, condition: "If-Unmodified-Since"},
		{name: "step 2 if-unmodified-since equal passes", conditions: Conditions{IfUnmodifiedSince: same}, want: outcomeProceed},
		{name: "step 2 if-unmodified-since later passes", conditions: Conditions{IfUnmodifiedSince: after}, want: outcomeProceed},
		{name: "step 2 sub second difference is ignored", conditions: Conditions{IfUnmodifiedSince: httpDate(testLastModified.Truncate(time.Second))}, want: outcomeProceed},
		{name: "step 2 future date is ignored", conditions: Conditions{IfUnmodifiedSince: future}, want: outcomeProceed},
		{name: "step 2 unparseable date is ignored", conditions: Conditions{IfUnmodifiedSince: "yesterday"}, want: outcomeProceed},
		{name: "step 2 wins over step 3", conditions: Conditions{IfUnmodifiedSince: before, IfNoneMatch: []string{matching}}, want: outcomePreconditionFailed, condition: "If-Unmodified-Since"},
		{name: "step 3 if-none-match matched", conditions: Conditions{IfNoneMatch: []string{matching}}, want: outcomeNotModified, condition: "If-None-Match"},
		{name: "step 3 if-none-match star", conditions: Conditions{IfNoneMatch: []string{"*"}}, want: outcomeNotModified, condition: "If-None-Match"},
		{name: "step 3 if-none-match weak comparison", conditions: Conditions{IfNoneMatch: []string{"W/" + matching}}, want: outcomeNotModified, condition: "If-None-Match"},
		{name: "step 3 if-none-match unquoted", conditions: Conditions{IfNoneMatch: []string{"d41d8cd98f00b204e9800998ecf8427e"}}, want: outcomeNotModified, condition: "If-None-Match"},
		{name: "step 3 if-none-match not matched", conditions: Conditions{IfNoneMatch: []string{other}}, want: outcomeProceed},
		{name: "step 3 not matched skips step 4 so 200 despite true if-modified-since", conditions: Conditions{IfNoneMatch: []string{other}, IfModifiedSince: after}, want: outcomeProceed},
		{name: "step 3 matched with step 4 true gives 304", conditions: Conditions{IfNoneMatch: []string{matching}, IfModifiedSince: after}, want: outcomeNotModified, condition: "If-None-Match"},
		{name: "step 4 if-modified-since not modified", conditions: Conditions{IfModifiedSince: after}, want: outcomeNotModified, condition: "If-Modified-Since"},
		{name: "step 4 if-modified-since equal", conditions: Conditions{IfModifiedSince: same}, want: outcomeNotModified, condition: "If-Modified-Since"},
		{name: "step 4 if-modified-since modified since", conditions: Conditions{IfModifiedSince: before}, want: outcomeProceed},
		{name: "step 4 future date is ignored", conditions: Conditions{IfModifiedSince: future}, want: outcomeProceed},
		{name: "step 4 unparseable date is ignored", conditions: Conditions{IfModifiedSince: "not a date"}, want: outcomeProceed},
		{name: "malformed if-none-match is ignored so step 4 applies", conditions: Conditions{IfNoneMatch: []string{`*, "a"`}, IfModifiedSince: after}, want: outcomeNotModified, condition: "If-Modified-Since"},
		{name: "malformed if-match is ignored so step 2 applies", conditions: Conditions{IfMatch: []string{`"unterminated`}, IfUnmodifiedSince: before}, want: outcomePreconditionFailed, condition: "If-Unmodified-Since"},
		{name: "step 5 if-range strong etag matches", conditions: Conditions{IfRange: matching}, want: outcomeProceed},
		{name: "step 5 if-range unquoted etag matches", conditions: Conditions{IfRange: "d41d8cd98f00b204e9800998ecf8427e"}, want: outcomeProceed},
		{name: "step 5 if-range other etag ignores range", conditions: Conditions{IfRange: other}, want: outcomeIgnoreRange},
		{name: "step 5 if-range weak etag ignores range", conditions: Conditions{IfRange: "W/" + matching}, want: outcomeIgnoreRange},
		{name: "step 5 if-range equal date honours range", conditions: Conditions{IfRange: same}, want: outcomeProceed},
		{name: "step 5 if-range other date ignores range", conditions: Conditions{IfRange: before}, want: outcomeIgnoreRange},
		{name: "step 5 if-range garbage ignores range", conditions: Conditions{IfRange: `"a" "b"`}, want: outcomeIgnoreRange},
		{name: "step 3 comes before step 5", conditions: Conditions{IfNoneMatch: []string{matching}, IfRange: other}, want: outcomeNotModified, condition: "If-None-Match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Evaluate(test.conditions, testEntity, testNow)
			switch test.want {
			case outcomePreconditionFailed:
				var failed *PreconditionError
				if !errors.Is(err, errs.ErrPreconditionFailed) || !errors.As(err, &failed) || failed.Condition != test.condition {
					t.Fatalf("err = %v, want precondition failure on %s", err, test.condition)
				}
			case outcomeNotModified:
				if err != nil || !result.NotModified || result.Condition != test.condition {
					t.Fatalf("result = %+v err = %v, want 304 on %s", result, err, test.condition)
				}
			case outcomeIgnoreRange:
				if err != nil || result.NotModified || !result.IgnoreRange {
					t.Fatalf("result = %+v err = %v, want ignored range", result, err)
				}
			default:
				if err != nil || result.NotModified || result.IgnoreRange {
					t.Fatalf("result = %+v err = %v, want proceed", result, err)
				}
			}
		})
	}
}

func TestConditionalZeroLastModifiedIgnoresDates(t *testing.T) {
	entity := Entity{ETag: "abc"}
	conditions := Conditions{IfUnmodifiedSince: httpDate(testLastModified), IfModifiedSince: httpDate(testLastModified), IfRange: httpDate(testLastModified)}
	result, err := Evaluate(conditions, entity, testNow)
	if err != nil || result.NotModified || !result.IgnoreRange {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestConditionsFromHeader(t *testing.T) {
	header := http.Header{}
	header.Add("If-Match", `"a"`)
	header.Add("If-Match", `"b"`)
	header.Set("If-None-Match", `"c"`)
	header.Set("If-Modified-Since", "ims")
	header.Set("If-Unmodified-Since", "ius")
	header.Set("If-Range", "ir")
	got := ConditionsFromHeader(header)
	if len(got.IfMatch) != 2 || got.IfNoneMatch[0] != `"c"` || got.IfModifiedSince != "ims" || got.IfUnmodifiedSince != "ius" || got.IfRange != "ir" {
		t.Fatalf("conditions = %+v", got)
	}
}

func TestEvaluateCopySourceTreats304AsPreconditionFailure(t *testing.T) {
	tests := []struct {
		name      string
		header    map[string]string
		condition string
		fails     bool
	}{
		{name: "none", header: map[string]string{}},
		{name: "if-match fails", header: map[string]string{"x-amz-copy-source-if-match": `"zzz"`}, condition: "x-amz-copy-source-if-match", fails: true},
		{name: "if-none-match matched", header: map[string]string{"x-amz-copy-source-if-none-match": `"d41d8cd98f00b204e9800998ecf8427e"`}, condition: "x-amz-copy-source-if-none-match", fails: true},
		{name: "if-modified-since not modified", header: map[string]string{"x-amz-copy-source-if-modified-since": httpDate(testLastModified.Add(time.Hour))}, condition: "x-amz-copy-source-if-modified-since", fails: true},
		{name: "if-unmodified-since modified", header: map[string]string{"x-amz-copy-source-if-unmodified-since": httpDate(testLastModified.Add(-time.Hour))}, condition: "x-amz-copy-source-if-unmodified-since", fails: true},
		{name: "if-match passes", header: map[string]string{"x-amz-copy-source-if-match": `"d41d8cd98f00b204e9800998ecf8427e"`}},
		{name: "plain if-match is not a copy source condition", header: map[string]string{"If-Match": `"zzz"`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{}
			for name, value := range test.header {
				header.Set(name, value)
			}
			err := EvaluateCopySource(ConditionsFromCopySourceHeader(header), testEntity, testNow)
			if !test.fails {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			var failed *PreconditionError
			if !errors.Is(err, errs.ErrPreconditionFailed) || !errors.As(err, &failed) || failed.Condition != test.condition {
				t.Fatalf("err = %v, want precondition failure on %s", err, test.condition)
			}
		})
	}
}

func TestEntityFromMeta(t *testing.T) {
	entity := EntityFromMeta(object.ObjectMeta{ETag: "abc", MsgDate: 1767268800})
	if entity.ETag != "abc" || !entity.LastModified.Equal(time.Unix(1767268800, 0)) || entity.LastModified.Location() != time.UTC {
		t.Fatalf("entity = %+v", entity)
	}
}

func TestSetValidatorsAndNotModified(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteNotModified(recorder, testEntity, "public, max-age=60", "Wed, 01 Jul 2026 00:00:00 GMT")
	header := recorder.Header()
	if recorder.Code != http.StatusNotModified || recorder.Body.Len() != 0 {
		t.Fatalf("code %d body %q", recorder.Code, recorder.Body.String())
	}
	if header.Get("ETag") != `"d41d8cd98f00b204e9800998ecf8427e"` || header.Get("Last-Modified") != "Thu, 01 Jan 2026 12:00:00 GMT" {
		t.Fatalf("validators = %v", header)
	}
	if header.Get("Cache-Control") != "public, max-age=60" || header.Get("Expires") != "Wed, 01 Jul 2026 00:00:00 GMT" {
		t.Fatalf("headers = %v", header)
	}

	bare := httptest.NewRecorder()
	WriteNotModified(bare, Entity{ETag: `"quoted"`}, "", "")
	if bare.Header().Get("ETag") != `"quoted"` || bare.Header().Get("Last-Modified") != "" || bare.Header().Get("Cache-Control") != "" {
		t.Fatalf("bare headers = %v", bare.Header())
	}
}

func TestCacheControlPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		sources CacheControlSources
		want    string
	}{
		{name: "none", want: ""},
		{name: "private bucket ignores max age", sources: CacheControlSources{PublicMaxAgeSeconds: 60}, want: ""},
		{name: "override wins", sources: CacheControlSources{Override: "no-store", Stored: "max-age=5", PublicRead: true, PublicMaxAgeSeconds: 60}, want: "no-store"},
		{name: "stored beats public", sources: CacheControlSources{Stored: "max-age=5", PublicRead: true, PublicMaxAgeSeconds: 60}, want: "max-age=5"},
		{name: "public with max age", sources: CacheControlSources{PublicRead: true, PublicMaxAgeSeconds: 3600}, want: "public, max-age=3600"},
		{name: "public with zero max age", sources: CacheControlSources{PublicRead: true}, want: "no-cache"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CacheControl(test.sources); got != test.want {
				t.Fatalf("CacheControl = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNeverImmutable(t *testing.T) {
	for _, maxAge := range []int{-1, 0, 1, 60, 3600, 86400, 31536000, 1 << 30} {
		for _, publicRead := range []bool{false, true} {
			got := CacheControl(CacheControlSources{PublicRead: publicRead, PublicMaxAgeSeconds: maxAge})
			if strings.Contains(strings.ToLower(got), "immutable") {
				t.Fatalf("CacheControl(publicRead=%v, maxAge=%d) = %q contains immutable", publicRead, maxAge, got)
			}
		}
	}
}
