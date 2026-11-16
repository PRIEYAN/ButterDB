package phase09

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func TestStorageRoutesWaitForTheStorageRuntime(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.startAdmin()
	owner, _ := w.claim()

	status, err := owner.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != "unlinked" || status.Telegram.Linked || status.ServerVersion != "0.9.0" || status.Commit != "abc1234" || status.ServerID != w.serverID {
		t.Fatalf("status while unlinked = %+v", status)
	}
	_, err = owner.ListBuckets(ctx)
	requireCode(t, err, adminv1.CodeTelegramNotLinked)
	_, err = owner.Usage(ctx)
	requireCode(t, err, adminv1.CodeTelegramNotLinked)

	w.linkTelegram(owner)
	info, err := w.client("").ServerInfo(ctx)
	if err != nil || !info.TelegramLinked || len(info.PairingMethods) != 2 {
		t.Fatalf("server info after linking = %+v, %v", info, err)
	}
	status, err = owner.Status(ctx)
	if err != nil || status.State != "syncing" || !status.Telegram.Linked || status.Telegram.Account == nil || status.Telegram.Account.UserID != primaryUser {
		t.Fatalf("status linked without a runtime = %+v, %v", status, err)
	}
	if strings.Contains(status.Telegram.Account.PhoneMasked, phoneNumber) {
		t.Fatalf("status leaks the full phone number: %q", status.Telegram.Account.PhoneMasked)
	}
	resp := w.raw(http.MethodGet, adminv1.PathBuckets, w.mustToken(owner), nil)
	if body := resp.errorBody(t); resp.status != http.StatusServiceUnavailable || body.Code != adminv1.CodeServerNotReady || body.RetryAfterSeconds < 1 {
		t.Fatalf("buckets without a runtime: status %d body %+v", resp.status, body)
	}

	w.publishBackend()
	list, err := owner.ListBuckets(ctx)
	if err != nil || len(list.Buckets) != 0 {
		t.Fatalf("ListBuckets with a runtime = %+v, %v", list, err)
	}
	status, err = owner.Status(ctx)
	if err != nil || status.State != "ready" || !status.Telegram.Connected {
		t.Fatalf("status with a runtime = %+v, %v", status, err)
	}

	unlinked, err := owner.UnlinkTelegram(ctx)
	if err != nil || unlinked.State == adminv1.TelegramLinked {
		t.Fatalf("UnlinkTelegram = %+v, %v", unlinked, err)
	}
	_, err = owner.ListBuckets(ctx)
	requireCode(t, err, adminv1.CodeTelegramNotLinked)
}

func (w *world) mustToken(client *adminv1.Client) string {
	w.t.Helper()
	creds, err := client.RotateDeviceToken(context.Background())
	if err != nil {
		w.t.Fatalf("rotate for a raw token: %v", err)
	}
	client.SetToken(creds.DeviceToken)
	return creds.DeviceToken
}

func TestAdminAPIEndToEnd(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.startAdmin()

	owner, creds := w.claim()
	w.linkTelegram(owner)
	backend := w.publishBackend()

	anonymous := w.client("")
	if _, err := anonymous.RequestPairing(ctx, pairingRequest()); err != nil {
		t.Fatalf("RequestPairing: %v", err)
	}
	second, err := anonymous.CompletePairing(ctx, completeRequest(w.latestSavedCode()))
	if err != nil {
		t.Fatalf("pair by saved messages: %v", err)
	}
	if err := owner.RevokeDevice(ctx, second.Device.DeviceID, false); err != nil {
		t.Fatalf("revoke second device: %v", err)
	}

	for _, encryption := range []string{"none", "tde1"} {
		bucketName := "photos-" + encryption
		created, err := owner.CreateBucket(ctx, adminv1.CreateBucketRequest{Name: bucketName, EncryptionDefault: encryption})
		if err != nil {
			t.Fatalf("CreateBucket %s: %v", bucketName, err)
		}
		if created.Name != bucketName || created.EncryptionDefault != encryption || created.Policy != "private" {
			t.Fatalf("bucket = %+v", created)
		}
		key, err := owner.CreateAccessKey(ctx, adminv1.CreateAccessKeyRequest{Name: "reader " + encryption, Scope: adminv1.Scope{Buckets: []string{bucketName}, Actions: []string{"read", "list"}}})
		if err != nil {
			t.Fatalf("CreateAccessKey: %v", err)
		}
		if key.S3Endpoint != s3PublicURL || key.S3Region != "us-east-1" || key.SecretAccessKey == "" {
			t.Fatalf("access key = %+v", key)
		}
		body := bytes.Repeat([]byte("butterdb admin api "), 20_000)
		sum := md5.Sum(body)
		wantETag := `"` + hex.EncodeToString(sum[:]) + `"`
		meta, err := owner.PutObject(ctx, bucketName, "albums/2026/cat.txt", bytes.NewReader(body), int64(len(body)), adminv1.PutObjectOptions{
			ContentType: "text/plain", UserMetadata: map[string]string{"camera": "x100v"}, IdempotencyKey: "upload-1-" + encryption,
		})
		if err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		if meta.ETag != wantETag || meta.SizeBytes != int64(len(body)) || meta.Encrypted != (encryption == "tde1") || meta.UserMetadata["camera"] != "x100v" {
			t.Fatalf("put result = %+v", meta)
		}
		replay, err := owner.PutObject(ctx, bucketName, "albums/2026/cat.txt", bytes.NewReader(body), int64(len(body)), adminv1.PutObjectOptions{
			ContentType: "text/plain", UserMetadata: map[string]string{"camera": "x100v"}, IdempotencyKey: "upload-1-" + encryption,
		})
		if err != nil || replay.VersionID != meta.VersionID {
			t.Fatalf("idempotent replay = %+v, %v; first %+v", replay, err, meta)
		}
		if _, err := owner.PutObject(ctx, bucketName, "albums/readme.txt", strings.NewReader("hello"), 5, adminv1.PutObjectOptions{}); err != nil {
			t.Fatalf("PutObject readme: %v", err)
		}

		listed, err := owner.ListObjects(ctx, bucketName, adminv1.ListObjectsOptions{Prefix: "albums/", Delimiter: "/"})
		if err != nil || len(listed.Objects) != 1 || listed.Objects[0].Key != "albums/readme.txt" || len(listed.CommonPrefixes) != 1 || listed.CommonPrefixes[0] != "albums/2026/" {
			t.Fatalf("ListObjects = %+v, %v", listed, err)
		}
		head, err := owner.HeadObject(ctx, bucketName, "albums/2026/cat.txt")
		if err != nil || head.ETag != wantETag || head.ContentType != "text/plain" {
			t.Fatalf("HeadObject = %+v, %v", head, err)
		}
		got, err := owner.ObjectMeta(ctx, bucketName, "albums/2026/cat.txt")
		if err != nil || got.SHA256 == "" || got.VersionID != meta.VersionID {
			t.Fatalf("ObjectMeta = %+v, %v", got, err)
		}

		w.clk.Advance(31 * time.Second)
		usage, err := owner.Usage(ctx)
		wantEncrypted := int64(0)
		if encryption == "tde1" {
			wantEncrypted = 2
		}
		if err != nil || usage.Objects != 2 || usage.StoredBytes <= int64(len(body)) || usage.EncryptedObjects != wantEncrypted {
			t.Fatalf("Usage after uploads = %+v, %v", usage, err)
		}

		download(t, owner, bucketName, "albums/2026/cat.txt", adminv1.GetObjectOptions{}, http.StatusOK, body)
		download(t, owner, bucketName, "albums/2026/cat.txt", adminv1.GetObjectOptions{RangeStart: 100, RangeEnd: 199}, http.StatusPartialContent, body[100:200])
		stream, err := owner.GetObject(ctx, bucketName, "albums/2026/cat.txt", adminv1.GetObjectOptions{IfNoneMatch: wantETag})
		if err != nil || stream.Status != http.StatusNotModified {
			t.Fatalf("conditional GetObject = %+v, %v", stream, err)
		}
		_ = stream.Body.Close()

		presigned, err := owner.Presign(ctx, bucketName, adminv1.PresignRequest{Key: "albums/readme.txt", AccessKeyID: key.AccessKey.AccessKeyID, ExpiresInSeconds: 600})
		if err != nil {
			t.Fatalf("Presign: %v", err)
		}
		if !strings.HasPrefix(presigned.URL, s3PublicURL+"/"+bucketName+"/albums/readme.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256") {
			t.Fatalf("presigned url = %q", presigned.URL)
		}
		fetchPresigned(t, backend.srv.URL, presigned.URL, "hello")

		if err := owner.DeleteObject(ctx, bucketName, "albums/readme.txt"); err != nil {
			t.Fatalf("DeleteObject: %v", err)
		}
		if err := owner.DeleteObject(ctx, bucketName, "albums/readme.txt"); err != nil {
			t.Fatalf("DeleteObject of a missing key: %v", err)
		}
		_, err = owner.HeadObject(ctx, bucketName, "albums/readme.txt")
		requireCode(t, err, adminv1.CodeObjectNotFound)
		result, err := owner.DeleteObjects(ctx, bucketName, adminv1.DeleteObjectsRequest{Keys: []string{"albums/2026/cat.txt", "never/existed"}})
		if err != nil || len(result.Deleted) != 2 || len(result.Errors) != 0 {
			t.Fatalf("DeleteObjects = %+v, %v", result, err)
		}
	}

	w.clk.Advance(31 * time.Second)
	usage, err := owner.Usage(ctx)
	if err != nil || usage.Buckets != 2 || usage.Objects != 0 || usage.ComputedAt.IsZero() {
		t.Fatalf("Usage = %+v, %v", usage, err)
	}
	status, err := owner.Status(ctx)
	if err != nil || status.Limits.MaxBuckets != 100 || status.Endpoints.S3PublicURL != s3PublicURL {
		t.Fatalf("Status = %+v, %v", status, err)
	}

	events := allAuditEvents(t, owner, 7)
	actions := map[string]bool{}
	for _, event := range events {
		actions[event.Action+"|"+event.Outcome] = true
	}
	for _, want := range []string{
		"setup.claim|success", "pairing.request|success", "pairing.complete|success", "device.revoke|success", "bucket.create|success",
		"access_key.create|success", "object.upload|success", "object.presign|success", "object.delete|success", "object.delete_batch|success",
		"telegram.login_success|success",
	} {
		if !actions[want] {
			t.Fatalf("audit log lacks %s", want)
		}
	}
	dump := new(strings.Builder)
	for _, event := range events {
		dump.WriteString(event.Target + event.ErrorCode + event.Action)
	}
	for _, secret := range []string{creds.DeviceToken, second.DeviceToken} {
		if strings.Contains(dump.String(), secret) {
			t.Fatal("an audit event carries a device token")
		}
	}
}

func download(t *testing.T, client *adminv1.Client, bucketName, key string, opts adminv1.GetObjectOptions, status int, want []byte) {
	t.Helper()
	stream, err := client.GetObject(context.Background(), bucketName, key, opts)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer stream.Body.Close()
	data, err := io.ReadAll(stream.Body)
	if err != nil || stream.Status != status || !bytes.Equal(data, want) {
		t.Fatalf("GetObject status %d, %d bytes (want %d), err %v", stream.Status, len(data), len(want), err)
	}
	if !stream.Meta.Encrypted && strings.Contains(bucketName, "tde1") || stream.Meta.Encrypted && !strings.Contains(bucketName, "tde1") {
		t.Fatalf("download encryption header = %v for bucket %s", stream.Meta.Encrypted, bucketName)
	}
}

func fetchPresigned(t *testing.T, s3URL, presigned, want string) {
	t.Helper()
	signed := strings.TrimPrefix(presigned, s3PublicURL)
	req, err := http.NewRequest(http.MethodGet, s3URL+signed, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = strings.TrimPrefix(s3PublicURL, "https://")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch presigned: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(data) != want {
		t.Fatalf("presigned download: status %d body %q", resp.StatusCode, data)
	}
}
