package objectops

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

type deleteResultDocument struct {
	XMLName xml.Name `xml:"DeleteResult"`
	Deleted []struct {
		Key string `xml:"Key"`
	} `xml:"Deleted"`
	Errors []struct {
		Key     string `xml:"Key"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

func deleteBody(quiet bool, keys ...string) string {
	var b strings.Builder
	b.WriteString(`<Delete xmlns="` + xmlcodec.Namespace + `">`)
	if quiet {
		b.WriteString("<Quiet>true</Quiet>")
	}
	for _, key := range keys {
		var escaped strings.Builder
		_ = xml.EscapeText(&escaped, []byte(key))
		b.WriteString("<Object><Key>" + escaped.String() + "</Key></Object>")
	}
	b.WriteString("</Delete>")
	return b.String()
}

func (f *fixture) deleteObjects(t *testing.T, body string, headers map[string]string) *deadlineRecorder {
	t.Helper()
	return f.do(t, f.handlers.DeleteObjects, router.OpDeleteObjects, "", request{
		method: http.MethodPost, target: "/photos?delete", body: body, headers: headers, auth: fullAuth(int64(len(body))),
	})
}

func decodeDeleteResult(t *testing.T, rec *deadlineRecorder) deleteResultDocument {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), `<?xml version="1.0" encoding="UTF-8"?>`+"\n") {
		t.Fatalf("body does not start with the xml declaration: %q", rec.Body.String())
	}
	var doc deleteResultDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return doc
}

func TestDeleteObjectsQuiet(t *testing.T) {
	cases := []struct {
		name        string
		quiet       bool
		failing     map[string]error
		wantDeleted []string
		wantErrors  []string
	}{
		{"verbose lists every deleted key", false, nil, []string{"a", "b", "c"}, nil},
		{"quiet lists nothing on success", true, nil, nil, nil},
		{"quiet still lists failures", true, map[string]error{"b": errs.ErrNoSuchBucket}, nil, []string{"b"}},
		{"verbose lists successes and failures", false, map[string]error{"b": errs.ErrNoSuchBucket}, []string{"a", "c"}, []string{"b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			f.remover.errFor = tc.failing
			doc := decodeDeleteResult(t, f.deleteObjects(t, deleteBody(tc.quiet, "a", "b", "c"), nil))
			var deleted, failed []string
			for _, entry := range doc.Deleted {
				deleted = append(deleted, entry.Key)
			}
			for _, entry := range doc.Errors {
				failed = append(failed, entry.Key)
			}
			if strings.Join(deleted, ",") != strings.Join(tc.wantDeleted, ",") || strings.Join(failed, ",") != strings.Join(tc.wantErrors, ",") {
				t.Fatalf("deleted = %v errors = %v", deleted, failed)
			}
			if len(f.remover.calls) != 3 {
				t.Fatalf("remover calls = %d", len(f.remover.calls))
			}
		})
	}
}

func TestDeleteObjects1001Keys(t *testing.T) {
	keys := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("key-%04d", i)
		}
		return out
	}
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantCalls  int
	}{
		{"1000 keys", deleteBody(true, keys(1000)...), 200, "", 1000},
		{"1001 keys", deleteBody(true, keys(1001)...), 400, "MalformedXML", 0},
		{"no keys", deleteBody(false), 400, "MalformedXML", 0},
		{"empty body", "", 400, "MissingRequestBodyError", 0},
		{"unknown root", `<Remove><Object><Key>a</Key></Object></Remove>`, 400, "MalformedXML", 0},
		{"doctype", `<!DOCTYPE Delete [<!ENTITY x "y">]><Delete><Object><Key>a</Key></Object></Delete>`, 400, "MalformedXML", 0},
		{"not xml", `{"keys": ["a"]}`, 400, "MalformedXML", 0},
		{"namespace is optional", `<Delete><Object><Key>a</Key></Object></Delete>`, 200, "", 1},
		{"body over 2 MiB", `<Delete>` + strings.Repeat(" ", 2<<20) + `</Delete>`, 400, "MaxMessageLengthExceeded", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})
			rec := f.deleteObjects(t, tc.body, nil)
			if tc.wantCode != "" {
				expectError(t, rec, tc.wantStatus, tc.wantCode)
			} else if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d", rec.Code)
			}
			if len(f.remover.calls) != tc.wantCalls {
				t.Fatalf("remover calls = %d, want %d", len(f.remover.calls), tc.wantCalls)
			}
		})
	}
}

func TestDeleteObjectsPerKeyResults(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.remover.errFor = map[string]error{"slow": errs.ErrSlowDown}
	body := `<Delete>` +
		`<Object><Key>fine</Key></Object>` +
		`<Object><Key>bad&#127;key</Key></Object>` +
		`<Object><Key>` + strings.Repeat("k", 1025) + `</Key></Object>` +
		`<Object><Key>versioned</Key><VersionId>9</VersionId></Object>` +
		`<Object><Key>null-version</Key><VersionId>null</VersionId></Object>` +
		`<Object><Key>slow</Key></Object>` +
		`<Object><Key>after-budget</Key></Object>` +
		`<Object><Key>also-after-budget</Key></Object>` +
		`</Delete>`
	rec := f.deleteObjects(t, body, nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	var doc deleteResultDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	wantDeleted := []string{"fine", "null-version"}
	if len(doc.Deleted) != len(wantDeleted) {
		t.Fatalf("deleted = %+v", doc.Deleted)
	}
	for i, key := range wantDeleted {
		if doc.Deleted[i].Key != key {
			t.Fatalf("deleted[%d] = %q, want %q", i, doc.Deleted[i].Key, key)
		}
	}
	wantErrors := []struct{ key, code string }{
		{"bad\x7fkey", "InvalidArgument"},
		{strings.Repeat("k", 1025), "KeyTooLongError"},
		{"versioned", "InvalidArgument"},
		{"slow", "SlowDown"},
		{"after-budget", "SlowDown"},
		{"also-after-budget", "SlowDown"},
	}
	if len(doc.Errors) != len(wantErrors) {
		t.Fatalf("errors = %+v", doc.Errors)
	}
	for i, want := range wantErrors {
		if doc.Errors[i].Key != want.key || doc.Errors[i].Code != want.code || doc.Errors[i].Message == "" {
			t.Fatalf("errors[%d] = %+v, want %+v", i, doc.Errors[i], want)
		}
	}
	for _, call := range f.remover.calls {
		if call.key == "after-budget" || call.key == "also-after-budget" {
			t.Fatalf("remover was called for %q after the budget was spent", call.key)
		}
	}
}

func TestDeleteObjectsChecksDeletePerKey(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.evaluator.denyNth = 3
	doc := decodeDeleteResult(t, f.deleteObjects(t, deleteBody(false, "one", "two", "three"), nil))
	if len(doc.Errors) != 1 || doc.Errors[0].Key != "two" || doc.Errors[0].Code != "AccessDenied" {
		t.Fatalf("errors = %+v", doc.Errors)
	}
	if len(doc.Deleted) != 2 {
		t.Fatalf("deleted = %+v", doc.Deleted)
	}
	if len(f.evaluator.calls) != 4 {
		t.Fatalf("evaluator calls = %d, want one for the request and one per key", len(f.evaluator.calls))
	}
	for _, call := range f.remover.calls {
		if call.key == "two" {
			t.Fatal("remover was called for a key without the delete action")
		}
	}
}

func TestDeleteObjectsContentMD5(t *testing.T) {
	body := deleteBody(false, "a")
	sum := md5.Sum([]byte(body))
	other := md5.Sum([]byte("other"))
	t.Run("matching digest", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{})
		rec := f.deleteObjects(t, body, map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(sum[:])})
		decodeDeleteResult(t, rec)
	})
	t.Run("mismatching digest", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{})
		expectError(t, f.deleteObjects(t, body, map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString(other[:])}), 400, "BadDigest")
		if len(f.remover.calls) != 0 {
			t.Fatal("remover was called after a digest mismatch")
		}
	})
	t.Run("malformed digest", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{})
		expectError(t, f.deleteObjects(t, body, map[string]string{"Content-MD5": "%%"}), 400, "InvalidDigest")
	})
}

func TestDeleteObjectsAccess(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	rec := f.do(t, f.handlers.DeleteObjects, router.OpDeleteObjects, "", request{
		method: http.MethodPost, target: "/photos?delete", body: deleteBody(false, "a"), auth: authFor([]string{"*"}, []string{"read", "write"}, 10),
	})
	expectError(t, rec, 403, "AccessDenied")
	if len(f.remover.calls) != 0 {
		t.Fatal("remover was called")
	}
}
