package errs

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func declaredSentinels(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "errs.go", nil, 0)
	if err != nil {
		t.Fatalf("parse errs.go: %v", err)
	}
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

func TestEverySentinelHasCode(t *testing.T) {
	mapped := map[error]string{}
	for _, m := range catalogue() {
		mapped[m.sentinel] = m.code
	}
	declared := declaredSentinels(t)
	if len(declared) == 0 {
		t.Fatal("no sentinels found in errs.go")
	}
	if len(declared) != len(mapped) {
		t.Fatalf("declared %d sentinels, catalogue maps %d", len(declared), len(mapped))
	}
	for _, m := range catalogue() {
		t.Run(m.code, func(t *testing.T) {
			if m.sentinel == nil {
				t.Fatal("nil sentinel in catalogue")
			}
			if m.code == "" || m.code == CodeInternal {
				t.Fatalf("sentinel %v has no dedicated code", m.sentinel)
			}
			if got := Code(m.sentinel); got != m.code {
				t.Fatalf("Code(%v) = %q, want %q", m.sentinel, got, m.code)
			}
		})
	}
	for _, name := range declared {
		if !strings.HasPrefix(name, "Err") {
			t.Fatalf("var %s in errs.go is not a sentinel", name)
		}
	}
}

func TestCodesUnique(t *testing.T) {
	seenCode := map[string]error{}
	seenSentinel := map[error]bool{}
	for _, m := range catalogue() {
		if other, dup := seenCode[m.code]; dup {
			t.Fatalf("code %q used by %v and %v", m.code, other, m.sentinel)
		}
		if seenSentinel[m.sentinel] {
			t.Fatalf("sentinel %v mapped twice", m.sentinel)
		}
		seenCode[m.code] = m.sentinel
		seenSentinel[m.sentinel] = true
	}
	if _, dup := seenCode[CodeInternal]; dup {
		t.Fatalf("internal code %q is also a sentinel code", CodeInternal)
	}
}

func TestCodeUnwraps(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil error", nil, ""},
		{"bare sentinel", ErrNoSuchBucket, "bucket.not_found"},
		{"wrapped once", fmt.Errorf("objectrepo: get %q: %w", "k", ErrNoSuchKey), "object.not_found"},
		{"wrapped twice", fmt.Errorf("s3: %w", fmt.Errorf("objectrepo: %w", ErrObjectTooLarge)), "object.too_large"},
		{"joined", errors.Join(errors.New("other"), ErrSlowDown), "telegram.slow_down"},
		{"unmapped error", errors.New("boom"), "internal.error"},
		{"wrapped unmapped error", fmt.Errorf("x: %w", errors.New("boom")), "internal.error"},
		{"missing dependency", fmt.Errorf("put: %w: bodies", ErrMissingDependency), "internal.missing_dependency"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Code(tc.err); got != tc.want {
				t.Fatalf("Code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCatalogueCodesMatchStarterTable(t *testing.T) {
	want := map[error]string{
		ErrNoSuchBucket:       "bucket.not_found",
		ErrNoSuchKey:          "object.not_found",
		ErrBucketExists:       "bucket.exists",
		ErrBucketNotEmpty:     "bucket.not_empty",
		ErrInvalidBucketName:  "bucket.invalid_name",
		ErrInvalidKey:         "object.invalid_key",
		ErrObjectTooLarge:     "object.too_large",
		ErrAccessDenied:       "access.denied",
		ErrUnknownAccessKey:   "access.unknown_key",
		ErrSignatureMismatch:  "access.signature_mismatch",
		ErrRequestExpired:     "access.request_expired",
		ErrPreconditionFailed: "request.precondition_failed",
		ErrInvalidRange:       "request.invalid_range",
		ErrBadDigest:          "request.bad_digest",
		ErrNoSuchUpload:       "multipart.no_such_upload",
		ErrInvalidPart:        "multipart.invalid_part",
		ErrSlowDown:           "telegram.slow_down",
		ErrNotImplemented:     "request.not_implemented",
		ErrMissingDependency:  "internal.missing_dependency",
		ErrMetadataTooLarge:   "object.metadata_too_large",
		ErrEntityTooSmall:     "multipart.entity_too_small",
		ErrInvalidPartOrder:   "multipart.invalid_part_order",
		ErrUploadBusy:         "multipart.upload_busy",
		ErrServiceUnavailable: "server.unavailable",
	}
	for sentinel, code := range want {
		if got := Code(sentinel); got != code {
			t.Errorf("Code(%v) = %q, want %q", sentinel, got, code)
		}
	}
}
