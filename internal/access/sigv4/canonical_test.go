package sigv4

import (
	"net/http"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestCanonicalURIEncodesOnce(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain", raw: "http://h/bucket/key.txt", want: "/bucket/key.txt"},
		{name: "space encoded once", raw: "http://h/bucket/my%20photo.jpg", want: "/bucket/my%20photo.jpg"},
		{name: "percent sign encoded once", raw: "http://h/bucket/100%25", want: "/bucket/100%25"},
		{name: "dollar", raw: "http://h/test$file.text", want: "/test%24file.text"},
		{name: "plus", raw: "http://h/a%2Bb", want: "/a%2Bb"},
		{name: "unicode", raw: "http://h/b/%E2%82%AC", want: "/b/%E2%82%AC"},
		{name: "unreserved kept", raw: "http://h/a-b_c.d~e", want: "/a-b_c.d~e"},
		{name: "empty path", raw: "http://h", want: "/"},
		{name: "star and bang", raw: "http://h/a*b!c", want: "/a%2Ab%21c"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalURI(mustURL(t, test.raw))
			if err != nil || got != test.want {
				t.Fatalf("canonicalURI(%q) = %q, %v, want %q", test.raw, got, err, test.want)
			}
		})
	}
}

func TestCanonicalURINoNormalisation(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "double slash", raw: "http://h/b//key", want: "/b//key"},
		{name: "dot segment", raw: "http://h/b/./key", want: "/b/./key"},
		{name: "dot dot segment", raw: "http://h/b/../key", want: "/b/../key"},
		{name: "trailing slash", raw: "http://h/b/dir/", want: "/b/dir/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalURI(mustURL(t, test.raw))
			if err != nil || got != test.want {
				t.Fatalf("canonicalURI(%q) = %q, %v, want %q", test.raw, got, err, test.want)
			}
		})
	}
}

func TestCanonicalQueryUnknownParamsIncluded(t *testing.T) {
	params, err := decodeQuery("x-id=GetObject&prefix=a&versions&zeta=1&alpha=2")
	if err != nil {
		t.Fatal(err)
	}
	want := "alpha=2&prefix=a&versions=&x-id=GetObject&zeta=1"
	if got := canonicalQuery(params, ""); got != want {
		t.Fatalf("canonicalQuery() = %q, want %q", got, want)
	}
}

func TestCanonicalQuerySpaceIsPercent20(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "percent twenty", query: "prefix=a%20b", want: "prefix=a%20b"},
		{name: "plus decodes to space", query: "prefix=a+b", want: "prefix=a%20b"},
		{name: "literal plus", query: "prefix=a%2Bb", want: "prefix=a%2Bb"},
		{name: "slash encoded", query: "prefix=a/b", want: "prefix=a%2Fb"},
		{name: "sorted by name then value", query: "a=2&a=1&b=0", want: "a=1&a=2&b=0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params, err := decodeQuery(test.query)
			if err != nil {
				t.Fatal(err)
			}
			if got := canonicalQuery(params, ""); got != test.want {
				t.Fatalf("canonicalQuery(%q) = %q, want %q", test.query, got, test.want)
			}
		})
	}
}

func TestCanonicalQuerySkipsSignature(t *testing.T) {
	params, err := decodeQuery("b=1&X-Amz-Signature=abc&a=2")
	if err != nil {
		t.Fatal(err)
	}
	if got := canonicalQuery(params, "X-Amz-Signature"); got != "a=2&b=1" {
		t.Fatalf("canonicalQuery() = %q", got)
	}
}

func TestHostFromRequestHost(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkRequest{method: http.MethodGet, target: "/bucket/key", host: "signed.butter.test"}.build(t)
	req.Header.Set("Host", "ignored.butter.test")
	if _, err := env.verifier.Authenticate(t.Context(), req); err != nil {
		t.Fatalf("Authenticate() with Host header ignored: %v", err)
	}
	req.Host = "other.butter.test"
	_, err := env.verifier.Authenticate(t.Context(), req)
	requireReason(t, err, ReasonSignature)
}

func TestSignedHeaderMissing(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkRequest{method: http.MethodGet, target: "/bucket/key", headers: map[string]string{"X-Custom": "v"}}.build(t)
	if _, err := env.verifier.Authenticate(t.Context(), req); err != nil {
		t.Fatalf("Authenticate() before removal: %v", err)
	}
	req.Header.Del("X-Custom")
	_, err := env.verifier.Authenticate(t.Context(), req)
	typed := requireReason(t, err, ReasonSignature)
	if typed.AccessKeyID != testKeyID {
		t.Fatalf("Error.AccessKeyID = %q", typed.AccessKeyID)
	}
}

func TestHeaderValueWhitespaceCollapsed(t *testing.T) {
	env := newTestEnv(t, "us-east-1")
	req := sdkRequest{method: http.MethodPut, target: "/bucket/key", headers: map[string]string{"X-Amz-Meta-Note": "  a   b    c  "}}.build(t)
	if _, err := env.verifier.Authenticate(t.Context(), req); err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got := collapseSpaces("  a   b \t c  "); got != "a b c" {
		t.Fatalf("collapseSpaces() = %q", got)
	}
}

func TestCanonicalHeadersJoinRepeatedValues(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://h/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("X-Multi", " one ")
	req.Header.Add("X-Multi", "two  words")
	got, present := canonicalHeaders(req, []string{"host", "x-multi"})
	if !present || got != "host:h\nx-multi:one,two words\n" {
		t.Fatalf("canonicalHeaders() = %q, %v", got, present)
	}
}
