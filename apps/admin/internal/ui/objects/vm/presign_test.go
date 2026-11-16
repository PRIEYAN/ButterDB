package vm

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func accessKey(id, name, status string, buckets, actions []string) adminv1.AccessKey {
	return adminv1.AccessKey{AccessKeyID: id, Name: name, Status: status, Scope: adminv1.Scope{Buckets: buckets, Actions: actions}}
}

func TestPresignKeysFilteredByReadScope(t *testing.T) {
	revoked := time.Now()
	revokedKey := accessKey("K6", "revoked", "active", []string{"*"}, []string{"read"})
	revokedKey.RevokedAt = &revoked
	keys := []adminv1.AccessKey{
		accessKey("K1", "reader", "active", []string{"photos"}, []string{"read", "list"}),
		accessKey("K2", "all buckets", "active", []string{"*"}, []string{"read"}),
		accessKey("K3", "writer only", "active", []string{"photos"}, []string{"write", "list"}),
		accessKey("K4", "other bucket", "active", []string{"avatars"}, []string{"read"}),
		accessKey("K5", "revoked status", "revoked", []string{"photos"}, []string{"read"}),
		revokedKey,
	}
	want := []SigningKey{{ID: "K2", Name: "all buckets"}, {ID: "K1", Name: "reader"}}
	if got := FilterSigningKeys(keys, "photos"); !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterSigningKeys = %v, want %v", got, want)
	}
	c := newFakeClient()
	c.keys = adminv1.AccessKeyList{AccessKeys: keys}
	v, _ := newTestVM(t, c)
	got, problem := v.SigningKeys(context.Background(), "photos")
	if problem != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("SigningKeys = %v, %v", got, problem)
	}
	if got, _ := v.SigningKeys(context.Background(), "nothing"); len(got) != 1 || got[0].ID != "K2" {
		t.Fatalf("keys for an unscoped bucket = %v", got)
	}
}

func TestPresignValidatesAndSends(t *testing.T) {
	cases := []struct {
		name    string
		keyID   string
		seconds int
		field   string
		sent    bool
	}{
		{"no signing key", "", 3600, "signing_key", false},
		{"zero seconds", "K1", 0, "", false},
		{"too long", "K1", MaxPresignSeconds + 1, "", false},
		{"valid", "K1", 3600, "", true},
		{"minimum", "K1", 1, "", true},
		{"maximum", "K1", MaxPresignSeconds, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeClient()
			v, _ := newTestVM(t, c)
			out := v.Presign(context.Background(), "photos", "a/b.jpg", tc.keyID, tc.seconds)
			if tc.sent {
				if out.Problem != nil || len(c.presignReq) != 1 || out.URL.URL == "" {
					t.Fatalf("outcome = %+v", out)
				}
				if got := c.presignReq[0]; got.Key != "a/b.jpg" || got.AccessKeyID != "K1" || got.ExpiresInSeconds != tc.seconds {
					t.Fatalf("request = %+v", got)
				}
				return
			}
			if out.Problem == nil || len(c.presignReq) != 0 {
				t.Fatalf("invalid presign was sent: %+v", out)
			}
			if tc.field != "" && out.Problem.Field != tc.field {
				t.Fatalf("field = %q", out.Problem.Field)
			}
		})
	}
}

func TestExpiryOptions(t *testing.T) {
	options := ExpiryOptions()
	if len(options) != 4 || options[1].Seconds != DefaultPresignSeconds || options[3].Seconds != MaxPresignSeconds {
		t.Fatalf("options = %v", options)
	}
}
