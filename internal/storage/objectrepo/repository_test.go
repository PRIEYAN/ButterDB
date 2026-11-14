package objectrepo

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

func TestNewRejectsNilSource(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("error = %v, want ErrMissingDependency", err)
	}
}

func TestObjectMetaRoundTrip(t *testing.T) {
	repo, db := newRepo(t)
	encrypted := sampleMeta(testChannel, "enc/obj", 20)
	encrypted.Encrypted = true
	encrypted.EncryptionVersion = 1
	encrypted.KeyID = strings.Repeat("k", 16)
	encrypted.StoredSize = 99
	encrypted.UserMeta = map[string]string{"color": "red", "note": "caf\u00e9 \"quoted\""}
	encrypted.SystemMeta = map[string]string{"cache-control": "max-age=60"}

	zeroByte := sampleMeta(testChannel, "empty", 30)
	zeroByte.BodyMsgID = 0
	zeroByte.StoredSize = 0
	zeroByte.PlaintextSize = 0

	plain := sampleMeta(testChannel, "plain\xff", 40)
	plain.ContentType = "text/plain"

	tests := []struct {
		name string
		meta object.ObjectMeta
	}{
		{"encrypted with metadata", encrypted},
		{"zero byte without body", zeroByte},
		{"plain with raw byte key", plain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			putAll(t, db, tt.meta)
			got, err := repo.Get(context.Background(), tt.meta.ChannelID, tt.meta.Key)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if !reflect.DeepEqual(got, tt.meta) {
				t.Fatalf("round trip differs:\n got %+v\nwant %+v", got, tt.meta)
			}
		})
	}
}

func TestPutObjectReplacesCurrentVersion(t *testing.T) {
	repo, db := newRepo(t)
	first := sampleMeta(testChannel, "k", 10)
	second := sampleMeta(testChannel, "k", 20)
	second.ETag = strings.Repeat("c", 32)
	putAll(t, db, first)
	putAll(t, db, second)
	got, err := repo.Get(context.Background(), testChannel, "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PutMsgID != 20 || got.ETag != second.ETag {
		t.Fatalf("got version %d etag %q, want the second put", got.PutMsgID, got.ETag)
	}
}

func TestGetMissingKey(t *testing.T) {
	repo, db := newRepo(t)
	putAll(t, db, sampleMeta(testChannel+1, "k", 10))
	_, err := repo.Get(context.Background(), testChannel, "k")
	if !errors.Is(err, errs.ErrNoSuchKey) {
		t.Fatalf("error = %v, want ErrNoSuchKey", err)
	}
}

func TestDeleteObject(t *testing.T) {
	repo, db := newRepo(t)
	putAll(t, db, sampleMeta(testChannel, "k", 10))
	var removed, again bool
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		if removed, err = DeleteObject(context.Background(), tx, testChannel, "k"); err != nil {
			return err
		}
		again, err = DeleteObject(context.Background(), tx, testChannel, "k")
		return err
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !removed || again {
		t.Fatalf("removed = %v, again = %v; want true, false", removed, again)
	}
	if _, err := repo.Get(context.Background(), testChannel, "k"); !errors.Is(err, errs.ErrNoSuchKey) {
		t.Fatalf("get after delete = %v", err)
	}
}

func TestQueueCleanup(t *testing.T) {
	_, db := newRepo(t)
	putAll(t, db, sampleMeta(testChannel, "live", 11))
	tests := []struct {
		name   string
		item   Cleanup
		queued bool
	}{
		{"old put message", Cleanup{ChannelID: testChannel, MsgID: 7, Reason: "superseded", ObjectKey: "live", QueuedByMsgID: 11}, true},
		{"same message again", Cleanup{ChannelID: testChannel, MsgID: 7, Reason: "superseded", ObjectKey: "live", QueuedByMsgID: 11}, false},
		{"without key", Cleanup{ChannelID: testChannel, MsgID: 8, Reason: "orphan_body"}, true},
		{"live put message", Cleanup{ChannelID: testChannel, MsgID: 11, Reason: "superseded"}, false},
		{"live body message", Cleanup{ChannelID: testChannel, MsgID: 10, Reason: "superseded"}, false},
		{"same id in another channel", Cleanup{ChannelID: testChannel + 1, MsgID: 11, Reason: "deleted"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var queued bool
			err := db.Write(context.Background(), func(tx *sql.Tx) error {
				var err error
				queued, err = QueueCleanup(context.Background(), tx, tt.item)
				return err
			})
			if err != nil {
				t.Fatalf("queue: %v", err)
			}
			if queued != tt.queued {
				t.Fatalf("queued = %v, want %v", queued, tt.queued)
			}
		})
	}
}
