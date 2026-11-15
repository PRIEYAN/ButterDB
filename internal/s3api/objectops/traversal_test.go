package objectops

import (
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

func TestTraversalKeysNeverTouchFilesystem(t *testing.T) {
	keys := []string{"../../etc/passwd", `..\x`, "/abs", "a/../b", "./x", "..", "%2e%2e/x", "a//b"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			sandbox := t.TempDir()
			t.Chdir(sandbox)
			f := newFixture(t, fixtureOptions{})
			if rec := f.put(t, key, "payload", nil); rec.Code != 200 {
				t.Fatalf("put status = %d; body=%q", rec.Code, rec.Body.String())
			}
			if got := string(f.writer.last(t).req.Key); got != key {
				t.Fatalf("writer received key %q, want %q verbatim", got, key)
			}
			if rec := f.get(t, key, nil, fullAuth(0), ""); rec.Code != 200 || rec.Body.String() != "payload" {
				t.Fatalf("get status = %d body = %q", rec.Code, rec.Body.String())
			}
			if rec := f.head(t, key, nil, ""); rec.Code != 200 || rec.Header().Get("Content-Length") != "7" {
				t.Fatalf("head status = %d", rec.Code)
			}
			if rec := f.copyObject(t, "copy-of-"+key, "photos/"+escapeCopySource(key), nil); rec.Code != 200 {
				t.Fatalf("copy status = %d; body=%q", rec.Code, rec.Body.String())
			}
			if rec := f.deleteObject(t, key, ""); rec.Code != 204 {
				t.Fatalf("delete status = %d", rec.Code)
			}
			if rec := f.deleteObjects(t, deleteBody(true, key), nil); rec.Code != 200 {
				t.Fatalf("delete objects status = %d", rec.Code)
			}
			for _, call := range f.remover.calls {
				if call.key != key {
					t.Fatalf("remover received key %q, want %q verbatim", call.key, key)
				}
			}
			for _, opened := range f.store.opens {
				if opened != "photos/"+key {
					t.Fatalf("opener received %q", opened)
				}
			}
			entries, err := os.ReadDir(sandbox)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("handlers created files in the working directory: %v", entries)
			}
		})
	}
}

func escapeCopySource(key string) string {
	return url.PathEscape(key)
}

func TestTraversalRouteUsesTargetKeyOnly(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.store.add(testBucket, "../../etc/passwd", []byte("x"), nil)
	rec := f.do(t, f.handlers.GetObject, router.OpGetObject, "../../etc/passwd", request{method: http.MethodGet, target: "/photos/anything", auth: fullAuth(0)})
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
}
