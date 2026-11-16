package vm

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func uploadOne(t *testing.T, v *ViewModel, name string, size int, prompts Prompts) UploadSpec {
	t.Helper()
	path := writeTempFile(t, name, size)
	spec, err := v.PrepareUpload("pics/", path)
	if err != nil {
		t.Fatalf("PrepareUpload: %v", err)
	}
	v.Upload(context.Background(), "photos", []UploadSpec{spec}, prompts)
	return spec
}

func TestUploadAsksBeforeReplace(t *testing.T) {
	cases := []struct {
		name     string
		choice   ReplaceChoice
		wantPuts int
		want     TransferState
	}{
		{"replace", ReplaceOnce, 1, TransferDone},
		{"skip", SkipOnce, 0, TransferSkipped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeClient()
			c.existing["pics/a.txt"] = adminv1.ObjectMeta{Key: "pics/a.txt", ETag: `"old"`}
			v, _ := newTestVM(t, c)
			asked := 0
			uploadOne(t, v, "a.txt", 10, Prompts{Replace: func(_ context.Context, key string, existing adminv1.ObjectMeta) ReplaceChoice {
				asked++
				if key != "pics/a.txt" || existing.ETag != `"old"` {
					t.Errorf("asked about %q %q", key, existing.ETag)
				}
				return tc.choice
			}})
			if asked != 1 {
				t.Fatalf("asked %d times", asked)
			}
			if c.putCount() != tc.wantPuts {
				t.Fatalf("puts = %d, want %d", c.putCount(), tc.wantPuts)
			}
			if item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt"); item.State != tc.want {
				t.Fatalf("state = %s, want %s", item.State, tc.want)
			}
		})
	}
}

func TestUploadReplaceAllAsksOnce(t *testing.T) {
	c := newFakeClient()
	c.existing["pics/a.txt"] = adminv1.ObjectMeta{ETag: `"1"`}
	c.existing["pics/b.txt"] = adminv1.ObjectMeta{ETag: `"2"`}
	v, _ := newTestVM(t, c)
	a, _ := v.PrepareUpload("pics/", writeTempFile(t, "a.txt", 4))
	b, _ := v.PrepareUpload("pics/", writeTempFile(t, "b.txt", 4))
	asked := 0
	v.Upload(context.Background(), "photos", []UploadSpec{a, b}, Prompts{Replace: func(context.Context, string, adminv1.ObjectMeta) ReplaceChoice {
		asked++
		return ReplaceAll
	}})
	if asked != 1 || c.putCount() != 2 {
		t.Fatalf("asked %d times, puts %d", asked, c.putCount())
	}
}

func TestUploadUsesIfMatch(t *testing.T) {
	c := newFakeClient()
	c.existing["pics/a.txt"] = adminv1.ObjectMeta{ETag: `"old"`}
	v, _ := newTestVM(t, c)
	uploadOne(t, v, "a.txt", 10, Prompts{Replace: func(context.Context, string, adminv1.ObjectMeta) ReplaceChoice { return ReplaceOnce }})
	opts := c.putAt(0).Opts
	if opts.IfMatch != `"old"` || opts.IfNoneMatchAny {
		t.Fatalf("options = %+v", opts)
	}
}

func TestUploadUsesIfNoneMatchForNewKey(t *testing.T) {
	c := newFakeClient()
	v, _ := newTestVM(t, c)
	uploadOne(t, v, "new.txt", 10, Prompts{})
	opts := c.putAt(0).Opts
	if !opts.IfNoneMatchAny || opts.IfMatch != "" {
		t.Fatalf("options = %+v", opts)
	}
	if c.putAt(0).Key != "pics/new.txt" || c.putAt(0).Size != 10 {
		t.Fatalf("put = %+v", c.putAt(0))
	}
}

func TestUploadIdempotencyKeyStableAcrossRetries(t *testing.T) {
	c := newFakeClient()
	fail := true
	c.putHook = func(_ int, _ context.Context, body io.Reader, _ int64, _ adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
		_, _ = io.Copy(io.Discard, body)
		if fail {
			fail = false
			return adminv1.ObjectMeta{}, apiErr(http.StatusInternalServerError, adminv1.CodeInternal)
		}
		return adminv1.ObjectMeta{ETag: `"ok"`}, nil
	}
	v, _ := newTestVM(t, c)
	uploadOne(t, v, "a.txt", 10, Prompts{})
	item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt")
	if item.State != TransferFailed || item.Problem == nil {
		t.Fatalf("first attempt state = %s", item.State)
	}
	if !v.RetryUpload(context.Background(), item.ID, Prompts{}) {
		t.Fatal("RetryUpload refused a failed item")
	}
	first, second := c.putAt(0).Opts.IdempotencyKey, c.putAt(1).Opts.IdempotencyKey
	if first == "" || first != second {
		t.Fatalf("keys differ across retries: %q vs %q", first, second)
	}
	if item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt"); item.State != TransferDone {
		t.Fatalf("after retry state = %s", item.State)
	}
}

func TestIdempotencyConflictGetsNewKey(t *testing.T) {
	c := newFakeClient()
	c.putHook = func(_ int, _ context.Context, body io.Reader, _ int64, _ adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
		_, _ = io.Copy(io.Discard, body)
		return adminv1.ObjectMeta{}, apiErr(http.StatusConflict, adminv1.CodeIdempotencyConflict)
	}
	v, _ := newTestVM(t, c)
	uploadOne(t, v, "a.txt", 10, Prompts{})
	item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt")
	v.RetryUpload(context.Background(), item.ID, Prompts{})
	if c.putAt(0).Opts.IdempotencyKey == c.putAt(1).Opts.IdempotencyKey {
		t.Fatal("conflict reused the idempotency key")
	}
}

func TestCommitOutcomeUnknownRetries(t *testing.T) {
	c := newFakeClient()
	c.putHook = func(call int, _ context.Context, body io.Reader, _ int64, _ adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
		_, _ = io.Copy(io.Discard, body)
		if call < 2 {
			err := apiErr(http.StatusServiceUnavailable, adminv1.CodeCommitOutcomeUnknown)
			err.Body.RetryAfterSeconds = 9
			return adminv1.ObjectMeta{}, err
		}
		return adminv1.ObjectMeta{ETag: `"ok"`}, nil
	}
	v, sleeps := newTestVM(t, c)
	uploadOne(t, v, "a.txt", 10, Prompts{})
	if c.putCount() != 3 {
		t.Fatalf("puts = %d, want 3", c.putCount())
	}
	for i := 1; i < 3; i++ {
		if c.putAt(i).Opts.IdempotencyKey != c.putAt(0).Opts.IdempotencyKey {
			t.Fatalf("retry %d changed the idempotency key", i)
		}
	}
	waits := sleeps.all()
	if len(waits) != 2 || waits[0] != 9*time.Second {
		t.Fatalf("waits = %v", waits)
	}
	if item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt"); item.State != TransferDone {
		t.Fatalf("state = %s", item.State)
	}
}

func TestCommitOutcomeUnknownGivesUpAfterFiveRetries(t *testing.T) {
	c := newFakeClient()
	c.putHook = func(_ int, _ context.Context, body io.Reader, _ int64, _ adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
		_, _ = io.Copy(io.Discard, body)
		return adminv1.ObjectMeta{}, apiErr(http.StatusServiceUnavailable, adminv1.CodeCommitOutcomeUnknown)
	}
	v, sleeps := newTestVM(t, c)
	uploadOne(t, v, "a.txt", 10, Prompts{})
	if c.putCount() != 6 {
		t.Fatalf("puts = %d, want 6", c.putCount())
	}
	for _, wait := range sleeps.all() {
		if wait < minCommitRetryDelay {
			t.Fatalf("wait %v below the 5 s minimum", wait)
		}
	}
	if item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt"); item.State != TransferFailed {
		t.Fatalf("state = %s", item.State)
	}
}

func TestFloodWaitPausesQueue(t *testing.T) {
	c := newFakeClient()
	c.putHook = func(call int, _ context.Context, body io.Reader, _ int64, _ adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
		_, _ = io.Copy(io.Discard, body)
		if call == 0 {
			err := apiErr(http.StatusServiceUnavailable, adminv1.CodeTelegramFloodWait)
			err.Body.Details = map[string]any{"until": fixedNow.Add(40 * time.Second).Format(time.RFC3339Nano)}
			return adminv1.ObjectMeta{}, err
		}
		return adminv1.ObjectMeta{ETag: `"ok"`}, nil
	}
	v, sleeps := newTestVM(t, c)
	uploadOne(t, v, "a.txt", 10, Prompts{})
	waits := sleeps.all()
	if len(waits) != 1 || waits[0] != 40*time.Second {
		t.Fatalf("waits = %v", waits)
	}
	if c.putAt(0).Opts.IdempotencyKey != c.putAt(1).Opts.IdempotencyKey {
		t.Fatal("flood wait retry changed the idempotency key")
	}
}

func TestUploadTooLargeNotSent(t *testing.T) {
	c := newFakeClient()
	c.status.Limits.MaxObjectBytes = 100
	v, _ := newTestVM(t, c)
	uploadOne(t, v, "big.bin", 101, Prompts{})
	if c.putCount() != 0 || c.headCalls != 0 {
		t.Fatalf("oversized file reached the server: puts=%d heads=%d", c.putCount(), c.headCalls)
	}
	item, _ := transferByKey(v.Transfers().Get(), "pics/big.bin")
	if item.State != TransferFailed || item.Problem == nil || item.Problem.Code != adminv1.CodeObjectTooLarge {
		t.Fatalf("item = %+v", item)
	}
}

func TestUploadKeyAndMetadataPrechecks(t *testing.T) {
	cases := []struct {
		name string
		edit func(*UploadSpec)
		code adminv1.Code
	}{
		{"key too long", func(s *UploadSpec) { s.Key = string(make([]byte, 1025)) }, adminv1.CodeObjectKeyInvalid},
		{"empty key", func(s *UploadSpec) { s.Key = "" }, adminv1.CodeObjectKeyInvalid},
		{"metadata too large", func(s *UploadSpec) { s.Metadata = map[string]string{"a": string(make([]byte, 3000))} }, adminv1.CodeMetadataTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeClient()
			v, _ := newTestVM(t, c)
			spec, _ := v.PrepareUpload("", writeTempFile(t, "a.txt", 4))
			tc.edit(&spec)
			v.Upload(context.Background(), "photos", []UploadSpec{spec}, Prompts{})
			if c.putCount() != 0 {
				t.Fatal("invalid upload was sent")
			}
			items := v.Transfers().Get().Items
			if len(items) != 1 || items[0].Problem == nil || items[0].Problem.Code != tc.code {
				t.Fatalf("items = %+v", items)
			}
		})
	}
}

func TestCommittingNotCancellable(t *testing.T) {
	c := newFakeClient()
	release := make(chan struct{})
	c.putHook = func(_ int, _ context.Context, body io.Reader, _ int64, _ adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
		_, _ = io.Copy(io.Discard, body)
		<-release
		return adminv1.ObjectMeta{ETag: `"ok"`}, nil
	}
	v, _ := newTestVM(t, c)
	spec, _ := v.PrepareUpload("pics/", writeTempFile(t, "a.txt", 10))
	done := make(chan struct{})
	go func() {
		v.Upload(context.Background(), "photos", []UploadSpec{spec}, Prompts{})
		close(done)
	}()
	var id int
	eventually(t, "committing state", func() bool {
		item, ok := transferByKey(v.Transfers().Get(), "pics/a.txt")
		id = item.ID
		return ok && item.State == TransferCommitting
	})
	if item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt"); item.Cancellable {
		t.Fatal("committing item reports Cancellable")
	}
	if v.Cancel(id) {
		t.Fatal("Cancel accepted a committing upload")
	}
	close(release)
	<-done
	if item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt"); item.State != TransferDone {
		t.Fatalf("state = %s", item.State)
	}
}

func TestSendingUploadCancels(t *testing.T) {
	c := newFakeClient()
	started := make(chan struct{})
	c.putHook = func(_ int, ctx context.Context, body io.Reader, _ int64, _ adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
		buf := make([]byte, 2)
		_, _ = body.Read(buf)
		close(started)
		<-ctx.Done()
		return adminv1.ObjectMeta{}, ctx.Err()
	}
	v, _ := newTestVM(t, c)
	spec, _ := v.PrepareUpload("pics/", writeTempFile(t, "a.txt", 10))
	done := make(chan struct{})
	go func() {
		v.Upload(context.Background(), "photos", []UploadSpec{spec}, Prompts{})
		close(done)
	}()
	<-started
	item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt")
	if !v.Cancel(item.ID) {
		t.Fatal("Cancel refused a sending upload")
	}
	<-done
	if item, _ := transferByKey(v.Transfers().Get(), "pics/a.txt"); item.State != TransferCancelled {
		t.Fatalf("state = %s", item.State)
	}
}

func TestUploadConcurrencyLimit(t *testing.T) {
	c := newFakeClient()
	running, peak := 0, 0
	release := make(chan struct{})
	c.putHook = func(_ int, _ context.Context, body io.Reader, _ int64, _ adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
		c.mu.Lock()
		running++
		peak = max(peak, running)
		c.mu.Unlock()
		_, _ = io.Copy(io.Discard, body)
		<-release
		c.mu.Lock()
		running--
		c.mu.Unlock()
		return adminv1.ObjectMeta{}, nil
	}
	v, _ := newTestVM(t, c)
	var specs []UploadSpec
	for _, name := range []string{"a", "b", "c", "d"} {
		spec, _ := v.PrepareUpload("", writeTempFile(t, name, 4))
		specs = append(specs, spec)
	}
	done := make(chan struct{})
	go func() {
		v.Upload(context.Background(), "photos", specs, Prompts{})
		close(done)
	}()
	eventually(t, "two uploads running", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return running == 2
	})
	close(release)
	<-done
	if peak != 2 {
		t.Fatalf("peak concurrency = %d, want 2", peak)
	}
}

func TestPreconditionFailedReasks(t *testing.T) {
	c := newFakeClient()
	c.existing["pics/a.txt"] = adminv1.ObjectMeta{ETag: `"v1"`}
	c.putHook = func(call int, _ context.Context, body io.Reader, _ int64, _ adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
		_, _ = io.Copy(io.Discard, body)
		if call == 0 {
			c.mu.Lock()
			c.existing["pics/a.txt"] = adminv1.ObjectMeta{ETag: `"v2"`}
			c.mu.Unlock()
			return adminv1.ObjectMeta{}, apiErr(http.StatusPreconditionFailed, adminv1.CodePreconditionFailed)
		}
		return adminv1.ObjectMeta{ETag: `"ok"`}, nil
	}
	v, _ := newTestVM(t, c)
	asked := 0
	uploadOne(t, v, "a.txt", 4, Prompts{Replace: func(context.Context, string, adminv1.ObjectMeta) ReplaceChoice {
		asked++
		return ReplaceAll
	}})
	if asked != 2 || c.putAt(1).Opts.IfMatch != `"v2"` {
		t.Fatalf("asked=%d second If-Match=%q", asked, c.putAt(1).Opts.IfMatch)
	}
}

func TestParseMetadata(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		want    map[string]string
		wantErr bool
	}{
		{"empty", "  \n", nil, false},
		{"pairs lowercased", "Camera=x100v\nlens = 23mm", map[string]string{"camera": "x100v", "lens": "23mm"}, false},
		{"missing equals", "camera", nil, true},
		{"empty name", "=x", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, problem := ParseMetadata(tc.text)
			if (problem != nil) != tc.wantErr {
				t.Fatalf("problem = %v", problem)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Fatalf("got[%s] = %q", k, got[k])
				}
			}
		})
	}
}

func TestContentTypeDetection(t *testing.T) {
	if got := detectContentType("photo.png", nil); got != "image/png" {
		t.Fatalf("by extension = %q", got)
	}
	if got := detectContentType("noext", []byte("%PDF-1.4")); got != "application/pdf" {
		t.Fatalf("sniffed = %q", got)
	}
}
