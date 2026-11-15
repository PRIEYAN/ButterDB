package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

func newRequest(method, host, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = host
	return r
}

func apiCode(t *testing.T, err error) s3err.Code {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return s3err.Map(err).Code
}

func TestPathStyleTargets(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   Target
	}{
		{"service", "/", Target{}},
		{"bucket", "/photos", Target{Bucket: "photos"}},
		{"bucket with trailing slash", "/photos/", Target{Bucket: "photos"}},
		{"object", "/photos/cat.jpg", Target{Bucket: "photos", Key: "cat.jpg", HasKey: true}},
		{"nested key", "/photos/2026/11/cat.jpg", Target{Bucket: "photos", Key: "2026/11/cat.jpg", HasKey: true}},
		{"percent decoded once", "/photos/a%20b%2Bc", Target{Bucket: "photos", Key: "a b+c", HasKey: true}},
		{"plus is literal", "/photos/a+b", Target{Bucket: "photos", Key: "a+b", HasKey: true}},
		{"double encoded stays encoded once", "/photos/a%2520b", Target{Bucket: "photos", Key: "a%20b", HasKey: true}},
		{"utf8 key", "/photos/%E2%9C%93.txt", Target{Bucket: "photos", Key: "✓.txt", HasKey: true}},
		{"trailing slash key", "/photos/dir/", Target{Bucket: "photos", Key: "dir/", HasKey: true}},
		{"query ignored", "/photos/cat.jpg?versionId=null", Target{Bucket: "photos", Key: "cat.jpg", HasKey: true}},
		{"dotted bucket", "/my.dotted.bucket/a", Target{Bucket: "my.dotted.bucket", Key: "a", HasKey: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTarget(newRequest(http.MethodGet, "203.0.113.10:9000", tc.target), Addressing{})
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseTarget = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPathStyleTargetErrors(t *testing.T) {
	cases := []struct {
		name   string
		target string
		code   s3err.Code
	}{
		{"uppercase bucket", "/Photos/a", s3err.CodeInvalidBucketName},
		{"short bucket", "/ab/a", s3err.CodeInvalidBucketName},
		{"encoded slash in bucket", "/pho%2Fto", s3err.CodeInvalidBucketName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTarget(newRequest(http.MethodGet, "localhost", tc.target), Addressing{})
			if got := apiCode(t, err); got != tc.code {
				t.Fatalf("code = %s, want %s", got, tc.code)
			}
		})
	}
}

func TestVirtualHostedTargets(t *testing.T) {
	addressing := Addressing{BaseDomains: []string{"s3.example.com"}}
	cases := []struct {
		name   string
		host   string
		target string
		want   Target
	}{
		{"object", "photos.s3.example.com", "/2026/cat.jpg", Target{Bucket: "photos", Key: "2026/cat.jpg", HasKey: true, VirtualHosted: true}},
		{"bucket with port", "photos.s3.example.com:9000", "/", Target{Bucket: "photos", VirtualHosted: true}},
		{"bucket empty path", "photos.s3.example.com", "/", Target{Bucket: "photos", VirtualHosted: true}},
		{"dotted bucket", "my.dotted.bucket.s3.example.com", "/a", Target{Bucket: "my.dotted.bucket", Key: "a", HasKey: true, VirtualHosted: true}},
		{"host case folded", "Photos.S3.Example.COM", "/a", Target{Bucket: "photos", Key: "a", HasKey: true, VirtualHosted: true}},
		{"base domain host is path style", "s3.example.com", "/photos/2026/cat.jpg", Target{Bucket: "photos", Key: "2026/cat.jpg", HasKey: true}},
		{"unrelated host is path style", "203.0.113.10:9000", "/photos/a", Target{Bucket: "photos", Key: "a", HasKey: true}},
		{"service on base domain", "s3.example.com", "/", Target{}},
		{"slash key kept", "photos.s3.example.com", "//a", Target{Bucket: "photos", Key: "/a", HasKey: true, VirtualHosted: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTarget(newRequest(http.MethodGet, tc.host, tc.target), addressing)
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseTarget = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLongestBaseDomainWins(t *testing.T) {
	cases := []struct {
		name    string
		domains []string
		host    string
		want    Target
	}{
		{"longer listed last", []string{"example.com", "s3.example.com"}, "photos.s3.example.com", Target{Bucket: "photos", Key: "a", HasKey: true, VirtualHosted: true}},
		{"longer listed first", []string{"s3.example.com", "example.com"}, "photos.s3.example.com", Target{Bucket: "photos", Key: "a", HasKey: true, VirtualHosted: true}},
		{"shorter matches other hosts", []string{"s3.example.com", "example.com"}, "photos.example.com", Target{Bucket: "photos", Key: "a", HasKey: true, VirtualHosted: true}},
		{"leading dot and case in config", []string{".S3.Example.com."}, "photos.s3.example.com", Target{Bucket: "photos", Key: "a", HasKey: true, VirtualHosted: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTarget(newRequest(http.MethodGet, tc.host, "/a"), Addressing{BaseDomains: tc.domains})
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseTarget = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEncodedSlashInKey(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		target  string
		wantKey string
	}{
		{"path style", "localhost", "/photos/a%2Fb/c", "a/b/c"},
		{"path style lowercase hex", "localhost", "/photos/a%2fb", "a/b"},
		{"virtual hosted", "photos.s3.example.com", "/a%2Fb", "a/b"},
		{"key made only of encoded slash", "localhost", "/photos/%2F", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTarget(newRequest(http.MethodGet, tc.host, tc.target), Addressing{BaseDomains: []string{"s3.example.com"}})
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			if got.Bucket != "photos" || got.Key != tc.wantKey || !got.HasKey {
				t.Fatalf("ParseTarget = %+v, want key %q", got, tc.wantKey)
			}
		})
	}
}

func TestNoPathCleaning(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		wantKey string
	}{
		{"double slash", "/photos/a//b", "a//b"},
		{"leading double slash in key", "/photos//a", "/a"},
		{"dot segment", "/photos/a/./b", "a/./b"},
		{"dot dot segment", "/photos/a/../b", "a/../b"},
		{"only dot dot", "/photos/..", ".."},
		{"encoded dot dot", "/photos/%2e%2e/x", "../x"},
		{"trailing double slash", "/photos/a//", "a//"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTarget(newRequest(http.MethodGet, "localhost", tc.target), Addressing{})
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			if got.Key != tc.wantKey || got.Bucket != "photos" {
				t.Fatalf("ParseTarget = %+v, want key %q", got, tc.wantKey)
			}
		})
	}
}

func TestLoopbackHostCheck(t *testing.T) {
	addressing := Addressing{
		BaseDomains:  []string{"s3.example.com"},
		AllowedHosts: []string{"files.example.org"},
		LoopbackOnly: true,
	}
	cases := []struct {
		name    string
		host    string
		allowed bool
	}{
		{"localhost", "localhost:9000", true},
		{"ipv4 loopback", "127.0.0.1:9000", true},
		{"ipv6 loopback", "[::1]:9000", true},
		{"ipv6 loopback without port", "[::1]", true},
		{"public url host", "files.example.org", true},
		{"public url host with port", "FILES.example.org:443", true},
		{"base domain", "s3.example.com", true},
		{"subdomain of base domain", "photos.s3.example.com", true},
		{"rebinding host", "attacker.example", false},
		{"suffix lookalike", "evils3.example.com", false},
		{"other ip", "203.0.113.10:9000", false},
		{"empty host", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTarget(newRequest(http.MethodGet, tc.host, "/photos/a"), addressing)
			if tc.allowed {
				if err != nil {
					t.Fatalf("ParseTarget: %v", err)
				}
				return
			}
			got := s3err.Map(err)
			if got.Code != s3err.CodeInvalidRequest || got.Status != http.StatusBadRequest || got.Message != "Host header is not served by this endpoint" {
				t.Fatalf("Map = %+v", got)
			}
		})
	}
	t.Run("not loopback accepts any host", func(t *testing.T) {
		open := Addressing{AllowedHosts: nil}
		if _, err := ParseTarget(newRequest(http.MethodGet, "attacker.example", "/photos/a"), open); err != nil {
			t.Fatalf("ParseTarget: %v", err)
		}
	})
}

func TestInvalidPercentEncoding(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"non hex digits", "a%zz"},
		{"truncated escape", "a%2"},
		{"lone percent", "%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeSegment(tc.raw)
			if got := apiCode(t, err); got != s3err.CodeInvalidURI {
				t.Fatalf("code = %s, want %s", got, s3err.CodeInvalidURI)
			}
		})
	}
}

func TestLiteralPercentSurvivesRoundTrip(t *testing.T) {
	r := newRequest(http.MethodGet, "localhost", "/photos/100%25%2F100%25")
	got, err := ParseTarget(r, Addressing{})
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if got.Key != "100%/100%" {
		t.Fatalf("Key = %q", got.Key)
	}
}
