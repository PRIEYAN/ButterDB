package vm

import (
	"errors"
	"testing"
)

func TestNormaliseURL(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"bare host gets https and default port", "butterdb.example", "https://butterdb.example:9443"},
		{"host and port", "butterdb.example:8443", "https://butterdb.example:8443"},
		{"ipv4 literal", "203.0.113.10", "https://203.0.113.10:9443"},
		{"ipv4 literal with port", "203.0.113.10:9443", "https://203.0.113.10:9443"},
		{"explicit https", "https://butterdb.example", "https://butterdb.example:9443"},
		{"path is stripped", "https://butterdb.example:9443/admin/v1/", "https://butterdb.example:9443"},
		{"query and fragment are stripped", "https://butterdb.example?x=1#frag", "https://butterdb.example:9443"},
		{"surrounding space is trimmed", "  butterdb.example  ", "https://butterdb.example:9443"},
		{"host is lower cased", "ButterDB.Example", "https://butterdb.example:9443"},
		{"scheme is lower cased", "HTTPS://butterdb.example", "https://butterdb.example:9443"},
		{"bare ipv6 literal", "2001:db8::1", "https://[2001:db8::1]:9443"},
		{"bracketed ipv6 with port", "[2001:db8::1]:8443", "https://[2001:db8::1]:8443"},
		{"loopback http keeps http", "http://localhost:9443", "http://localhost:9443"},
		{"loopback http default port", "http://127.0.0.1", "http://127.0.0.1:9443"},
		{"ipv6 loopback http", "http://[::1]:9443", "http://[::1]:9443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormaliseURL(tc.input)
			if err != nil {
				t.Fatalf("NormaliseURL(%q) error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("NormaliseURL(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNormaliseURLRejectsInvalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"only space", "   "},
		{"unsupported scheme", "ftp://butterdb.example"},
		{"credentials", "https://user:pw@butterdb.example"},
		{"no host", "https://"},
		{"port only", ":9443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := NormaliseURL(tc.input); !errors.Is(err, ErrInvalidURL) {
				t.Fatalf("NormaliseURL(%q) = %q, %v; want ErrInvalidURL", tc.input, got, err)
			}
		})
	}
}

func TestHTTPOnlyForLoopback(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		allowed bool
	}{
		{"localhost", "http://localhost:9443", true},
		{"localhost any case", "http://LocalHost:9443", true},
		{"ipv4 loopback", "http://127.0.0.1:9443", true},
		{"ipv6 loopback", "http://[::1]:9443", true},
		{"other loopback address", "http://127.0.0.2:9443", false},
		{"public host", "http://butterdb.example:9443", false},
		{"private address", "http://192.168.1.10:9443", false},
		{"https public host", "https://butterdb.example:9443", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormaliseURL(tc.input)
			if tc.allowed && err != nil {
				t.Fatalf("NormaliseURL(%q) error: %v", tc.input, err)
			}
			if !tc.allowed && !errors.Is(err, ErrPlainHTTPNotLoopback) {
				t.Fatalf("NormaliseURL(%q) error = %v, want ErrPlainHTTPNotLoopback", tc.input, err)
			}
		})
	}
}

func TestIsPlainHTTP(t *testing.T) {
	if !IsPlainHTTP("http://localhost:9443") || IsPlainHTTP("https://localhost:9443") {
		t.Fatalf("IsPlainHTTP misclassifies schemes")
	}
}
