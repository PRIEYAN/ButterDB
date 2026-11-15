package buckets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func TestCreateBucketPublishesAndProjects(t *testing.T) {
	tests := []struct {
		name       string
		encryption string
		wantTypes  string
		wantMints  int
	}{
		{name: "plain bucket", encryption: bucket.EncryptionNone, wantTypes: "reg,bcreate", wantMints: 0},
		{name: "tde1 bucket publishes its key", encryption: bucket.EncryptionTDE1, wantTypes: "reg,bcreate,encfg", wantMints: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := newWorld(t)
			w.linkRegistry()
			info := w.createBucket("photos", test.encryption)

			if got := joinTypes(opTypes(w.registryTexts())); got != test.wantTypes {
				t.Fatalf("registry ops = %s, want %s", got, test.wantTypes)
			}
			if info.Name != "photos" || info.ChannelID <= 0 || info.DefaultEncryption != test.encryption || info.CacheMaxAge != 3600 {
				t.Fatalf("info = %+v", info)
			}
			if w.keys.minted != test.wantMints {
				t.Fatalf("keys minted = %d, want %d", w.keys.minted, test.wantMints)
			}
			row, found, err := w.channels.Channel(w.ctx, info.ChannelID)
			if err != nil || !found {
				t.Fatalf("channel row: found=%v err=%v", found, err)
			}
			if row.Kind != syncer.KindBucket || row.AccessHash == 0 || !row.InitialSyncDone || row.Title != "butterDB bucket photos" {
				t.Fatalf("channel row = %+v", row)
			}
			if got := w.count(`SELECT COUNT(*) FROM replay_log WHERE channel_id = ?1 AND op_type = 'bcreate'`, w.registryID()); got != 1 {
				t.Fatalf("bcreate replay rows = %d, want 1", got)
			}
			if got := w.count(`SELECT COUNT(*) FROM encryption_keys WHERE bucket_channel_id = ?1`, info.ChannelID); got != test.wantMints {
				t.Fatalf("encryption key rows = %d, want %d", got, test.wantMints)
			}
			if got := len(w.network.SentControls(info.ChannelID)); got != 0 {
				t.Fatalf("bucket channel received %d control messages, want 0", got)
			}
		})
	}
}

func TestCreateKeepsBucketWhenKeyPublishFails(t *testing.T) {
	w := newWorld(t)
	w.linkRegistry()
	w.keys.mintFail = errors.New("key file unavailable")

	info := w.createBucket("vault", bucket.EncryptionTDE1)

	if info.DefaultEncryption != bucket.EncryptionTDE1 {
		t.Fatalf("info = %+v", info)
	}
	if got := joinTypes(opTypes(w.registryTexts())); got != "reg,bcreate" {
		t.Fatalf("registry ops = %s", got)
	}
}

func TestCreateRejectsExistingNameAndFullDeployment(t *testing.T) {
	w := newWorld(t)
	w.linkRegistry()
	w.createBucket("photos", bucket.EncryptionNone)
	channelsBefore := len(w.network.ChannelIDs())

	_, err := w.service.Create(w.ctx, bucket.Spec{Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone})
	if !errors.Is(err, errs.ErrBucketExists) {
		t.Fatalf("duplicate create error = %v, want ErrBucketExists", err)
	}
	for i := 0; i < 99; i++ {
		w.exec(`INSERT INTO buckets (channel_id, name, policy, encryption_default, cache_max_age_seconds, created_msg_id, created_job_id, created_date, config_msg_id)
VALUES (?1, ?2, 'private', 'none', 3600, 1, '00000000-0000-4000-8000-000000000001', 0, 1)`, 9000+i, "filler-"+strings.Repeat("a", 3)+string(rune('a'+i/26))+string(rune('a'+i%26)))
	}
	_, err = w.service.Create(w.ctx, bucket.Spec{Name: "overflow", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone})
	if !errors.Is(err, ErrBucketLimitReached) {
		t.Fatalf("limit create error = %v, want ErrBucketLimitReached", err)
	}
	if got := len(w.network.ChannelIDs()); got != channelsBefore {
		t.Fatalf("channels = %d, want %d: a refused create must not create a channel", got, channelsBefore)
	}
}

func TestCreateIdempotentAfterCrash(t *testing.T) {
	spec := bucket.Spec{Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone, CacheMaxAge: 3600}
	tests := []struct {
		name         string
		arrange      func(t *testing.T, w *world)
		wantFirstErr bool
	}{
		{
			name: "send fails after the channel exists",
			arrange: func(t *testing.T, w *world) {
				fault := faketg.Fault{Kind: faketg.FaultError, Err: errors.New("connection reset")}
				if err := w.telegram.QueueFaults(faketg.MethodSendControlIdempotent, fault); err != nil {
					t.Fatal(err)
				}
			},
			wantFirstErr: true,
		},
		{
			name:         "projection fails after the send landed",
			arrange:      func(_ *testing.T, w *world) { w.writer.failNext = 1 },
			wantFirstErr: true,
		},
		{
			name: "outcome unknown is resolved in the same call",
			arrange: func(t *testing.T, w *world) {
				fault := faketg.Fault{Kind: faketg.FaultOutcomeUnknown, Delivered: true}
				if err := w.telegram.QueueFaults(faketg.MethodSendControlIdempotent, fault); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "accepted with an error response",
			arrange: func(t *testing.T, w *world) {
				if err := w.telegram.QueueFaults(faketg.MethodSendControlIdempotent, faketg.Fault{Kind: faketg.FaultAcceptedWithError}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := newWorld(t)
			w.linkRegistry()
			test.arrange(t, w)

			_, err := w.service.Create(w.ctx, spec)
			if (err != nil) != test.wantFirstErr {
				t.Fatalf("first create error = %v, wantErr %v", err, test.wantFirstErr)
			}
			w.clock.Advance(90 * time.Second)
			info, err := w.service.Create(w.ctx, spec)
			if test.wantFirstErr && err != nil {
				t.Fatalf("retry create: %v", err)
			}
			if !test.wantFirstErr && !errors.Is(err, errs.ErrBucketExists) {
				t.Fatalf("second create error = %v, want ErrBucketExists", err)
			}
			committed, found, lookupErr := w.repo.Lookup(w.ctx, "photos")
			if lookupErr != nil || !found {
				t.Fatalf("bucket not committed: found=%v err=%v", found, lookupErr)
			}
			if got := len(w.network.ChannelIDs()); got != 2 {
				t.Fatalf("telegram channels = %d, want 2 (registry and one bucket)", got)
			}
			if got := joinTypes(opTypes(w.registryTexts())); got != "reg,bcreate" {
				t.Fatalf("registry ops = %s, want reg,bcreate", got)
			}
			if test.wantFirstErr && info.ChannelID != committed.ChannelID {
				t.Fatalf("retry returned channel %d, committed channel %d", info.ChannelID, committed.ChannelID)
			}
			w.requireReplayMatchesTelegram()
			if rows := w.count(`SELECT COUNT(*) FROM replay_log WHERE op_type = 'bcreate'`); rows != 1 {
				t.Fatalf("bcreate replay rows = %d, want 1", rows)
			}
		})
	}
}

func TestDeleteRequiresEmpty(t *testing.T) {
	tests := []struct {
		name       string
		arrange    func(w *world, info bucket.Info)
		target     string
		wantErr    error
		wantBdel   bool
		wantQueued bool
	}{
		{
			name: "bucket with objects",
			arrange: func(w *world, info bucket.Info) {
				w.exec(`UPDATE buckets SET object_count = 3 WHERE channel_id = ?1`, info.ChannelID)
			},
			target:  "photos",
			wantErr: errs.ErrBucketNotEmpty,
		},
		{
			name:    "bucket with a non terminal job",
			arrange: func(w *world, info bucket.Info) { w.jobs.active[info.ChannelID] = true },
			target:  "photos",
			wantErr: errs.ErrBucketNotEmpty,
		},
		{
			name:    "unknown bucket",
			arrange: func(*world, bucket.Info) {},
			target:  "missing",
			wantErr: errs.ErrNoSuchBucket,
		},
		{
			name:       "empty bucket",
			arrange:    func(*world, bucket.Info) {},
			target:     "photos",
			wantBdel:   true,
			wantQueued: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := newWorld(t)
			w.linkRegistry()
			info := w.createBucket("photos", bucket.EncryptionNone)
			test.arrange(w, info)

			err := w.service.Delete(w.ctx, bucket.BucketName(test.target))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Delete error = %v, want %v", err, test.wantErr)
			}
			hasBdel := strings.Contains(joinTypes(opTypes(w.registryTexts())), "bdel")
			if hasBdel != test.wantBdel {
				t.Fatalf("bdel sent = %v, want %v (ops %s)", hasBdel, test.wantBdel, joinTypes(opTypes(w.registryTexts())))
			}
			queued := w.count(`SELECT COUNT(*) FROM pending_channel_cleanup WHERE channel_id = ?1`, info.ChannelID)
			if (queued == 1) != test.wantQueued {
				t.Fatalf("pending_channel_cleanup rows = %d, want queued=%v", queued, test.wantQueued)
			}
			if test.wantBdel {
				if _, found, _ := w.repo.Lookup(w.ctx, "photos"); found {
					t.Fatal("bucket is still live after delete")
				}
				if _, err := w.telegram.PeerOf(info.ChannelID); err != nil {
					t.Fatalf("delete must not remove the channel itself: %v", err)
				}
			}
		})
	}
}

func TestDeleteTombstonesDoNotCount(t *testing.T) {
	w := newWorld(t)
	w.linkRegistry()
	info := w.createBucket("photos", bucket.EncryptionNone)
	w.exec(`UPDATE buckets SET object_count = 0, stored_bytes = 0 WHERE channel_id = ?1`, info.ChannelID)

	if err := w.service.Delete(w.ctx, "photos"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	w.clock.Advance(time.Minute)
	again, err := w.service.Create(w.ctx, bucket.Spec{Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone})
	if err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
	if again.ChannelID == info.ChannelID {
		t.Fatal("a recreated bucket must get a new channel")
	}
}

func TestConfigureViaBcfgOnly(t *testing.T) {
	policy := bucket.PolicyPublicRead
	age := int64(86400)
	tde1 := bucket.EncryptionTDE1

	t.Run("full config travels as one bcfg through the projector", func(t *testing.T) {
		w := newWorld(t)
		w.linkRegistry()
		info := w.createBucket("photos", bucket.EncryptionNone)
		writesBefore := w.writer.writes
		w.clock.Advance(time.Minute)

		updated, err := w.service.Update(w.ctx, "photos", bucket.Patch{Policy: &policy, CacheMaxAge: &age})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if updated.Policy != policy || updated.CacheMaxAge != age || updated.DefaultEncryption != bucket.EncryptionNone || updated.ChannelID != info.ChannelID {
			t.Fatalf("updated = %+v", updated)
		}
		if got := w.writer.writes - writesBefore; got != 1 {
			t.Fatalf("projection writes = %d, want exactly the one projector transaction", got)
		}
		texts := w.registryTexts()
		last := texts[len(texts)-1]
		for _, want := range []string{"t=bcfg", "b=photos", "pol=public-read", "de=none", "cma=86400"} {
			if !strings.Contains(last, want) {
				t.Fatalf("bcfg %q is missing %q", last, want)
			}
		}
		if got := w.count(`SELECT COUNT(*) FROM replay_log WHERE op_type = 'bcfg'`); got != 1 {
			t.Fatalf("bcfg replay rows = %d, want 1", got)
		}
	})

	t.Run("switch to tde1 publishes the key first", func(t *testing.T) {
		w := newWorld(t)
		w.linkRegistry()
		w.createBucket("photos", bucket.EncryptionNone)

		if _, err := w.service.Update(w.ctx, "photos", bucket.Patch{DefaultEncryption: &tde1}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := joinTypes(opTypes(w.registryTexts())); got != "reg,bcreate,encfg,bcfg" {
			t.Fatalf("registry ops = %s", got)
		}
	})

	t.Run("switch to tde1 reuses an existing key", func(t *testing.T) {
		w := newWorld(t)
		w.linkRegistry()
		info := w.createBucket("photos", bucket.EncryptionNone)
		w.keys.present[info.ChannelID] = true

		if _, err := w.service.Update(w.ctx, "photos", bucket.Patch{DefaultEncryption: &tde1}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := joinTypes(opTypes(w.registryTexts())); got != "reg,bcreate,bcfg" {
			t.Fatalf("registry ops = %s", got)
		}
	})

	t.Run("failed key publish stops the switch", func(t *testing.T) {
		w := newWorld(t)
		w.linkRegistry()
		w.createBucket("photos", bucket.EncryptionNone)
		w.keys.mintFail = errors.New("key file unavailable")

		if _, err := w.service.Update(w.ctx, "photos", bucket.Patch{DefaultEncryption: &tde1}); err == nil {
			t.Fatal("Update succeeded without a key")
		}
		if got := joinTypes(opTypes(w.registryTexts())); got != "reg,bcreate" {
			t.Fatalf("registry ops = %s, want no bcfg", got)
		}
	})

	t.Run("empty patch sends nothing", func(t *testing.T) {
		w := newWorld(t)
		w.linkRegistry()
		w.createBucket("photos", bucket.EncryptionNone)

		if _, err := w.service.Update(w.ctx, "photos", bucket.Patch{}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := joinTypes(opTypes(w.registryTexts())); got != "reg,bcreate" {
			t.Fatalf("registry ops = %s", got)
		}
	})

	t.Run("invalid patch and unknown bucket", func(t *testing.T) {
		w := newWorld(t)
		w.linkRegistry()
		w.createBucket("photos", bucket.EncryptionNone)
		bad := "open"

		if _, err := w.service.Update(w.ctx, "photos", bucket.Patch{Policy: &bad}); !errors.Is(err, bucket.ErrInvalidPolicy) {
			t.Fatalf("invalid patch error = %v", err)
		}
		if _, err := w.service.Update(w.ctx, "missing", bucket.Patch{Policy: &policy}); !errors.Is(err, errs.ErrNoSuchBucket) {
			t.Fatalf("unknown bucket error = %v", err)
		}
	})
}

func TestWritesGatedOnAuthority(t *testing.T) {
	policy := bucket.PolicyPublicRead
	tests := []struct {
		name string
		call func(w *world) error
	}{
		{name: "create", call: func(w *world) error {
			_, err := w.service.Create(w.ctx, bucket.Spec{Name: "other", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone})
			return err
		}},
		{name: "update", call: func(w *world) error {
			_, err := w.service.Update(w.ctx, "photos", bucket.Patch{Policy: &policy})
			return err
		}},
		{name: "delete", call: func(w *world) error { return w.service.Delete(w.ctx, "photos") }},
		{name: "ensure key", call: func(w *world) error { return w.service.EnsureKey(w.ctx, 1) }},
		{name: "write check", call: func(w *world) error { return w.service.RequireWriteAuthority(w.ctx, 1) }},
	}
	breakages := []struct {
		name   string
		break_ func(w *world)
	}{
		{name: "registry scan pending", break_: func(w *world) {
			w.exec(`UPDATE channels SET initial_sync_done = 0 WHERE channel_id = ?1`, w.registryID())
		}},
		{name: "registry rebuild pending", break_: func(w *world) {
			w.exec(`UPDATE channels SET needs_projection_rebuild = 1 WHERE channel_id = ?1`, w.registryID())
		}},
	}
	for _, breakage := range breakages {
		for _, test := range tests {
			t.Run(breakage.name+" "+test.name, func(t *testing.T) {
				w := newWorld(t)
				w.linkRegistry()
				w.createBucket("photos", bucket.EncryptionNone)
				breakage.break_(w)
				channelsBefore, sentBefore := len(w.network.ChannelIDs()), len(w.registryTexts())

				err := test.call(w)
				if !errors.Is(err, syncer.ErrNoAuthority) || !errors.Is(err, errs.ErrSlowDown) {
					t.Fatalf("error = %v, want ErrNoAuthority wrapping ErrSlowDown", err)
				}
				if len(w.network.ChannelIDs()) != channelsBefore || len(w.registryTexts()) != sentBefore {
					t.Fatal("a gated write reached Telegram")
				}
			})
		}
	}

	t.Run("bucket without authority refuses writes and delete only for itself", func(t *testing.T) {
		w := newWorld(t)
		w.linkRegistry()
		busy := w.createBucket("busy", bucket.EncryptionNone)
		fine := w.createBucket("fine", bucket.EncryptionNone)
		w.exec(`UPDATE channels SET needs_projection_rebuild = 1 WHERE channel_id = ?1`, busy.ChannelID)

		if err := w.service.RequireWriteAuthority(w.ctx, busy.ChannelID); !errors.Is(err, syncer.ErrNoAuthority) {
			t.Fatalf("busy bucket error = %v", err)
		}
		if err := w.service.RequireWriteAuthority(w.ctx, fine.ChannelID); err != nil {
			t.Fatalf("other bucket error = %v", err)
		}
		if err := w.service.Delete(w.ctx, "busy"); !errors.Is(err, syncer.ErrNoAuthority) {
			t.Fatalf("Delete busy error = %v", err)
		}
		if err := w.service.Delete(w.ctx, "fine"); err != nil {
			t.Fatalf("Delete fine: %v", err)
		}
	})

	t.Run("no registry recorded", func(t *testing.T) {
		w := newWorld(t)
		_, err := w.service.Create(w.ctx, bucket.Spec{Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone})
		if !errors.Is(err, syncer.ErrNoAuthority) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestBucketNameValidated(t *testing.T) {
	names := []struct {
		name   string
		bucket string
	}{
		{name: "empty", bucket: ""},
		{name: "too short", bucket: "ab"},
		{name: "too long", bucket: strings.Repeat("a", 64)},
		{name: "uppercase", bucket: "Photos"},
		{name: "underscore", bucket: "my_bucket"},
		{name: "ip address", bucket: "192.168.5.4"},
		{name: "xn prefix", bucket: "xn--photos"},
		{name: "reserved suffix", bucket: "photos-s3alias"},
		{name: "double dot", bucket: "my..photos"},
		{name: "leading hyphen", bucket: "-photos"},
	}
	for _, test := range names {
		t.Run(test.name, func(t *testing.T) {
			w := newWorld(t)
			w.linkRegistry()
			channelsBefore, sentBefore := len(w.network.ChannelIDs()), len(w.registryTexts())
			name := bucket.BucketName(test.bucket)
			policy := bucket.PolicyPrivate

			_, createErr := w.service.Create(w.ctx, bucket.Spec{Name: name, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone})
			_, updateErr := w.service.Update(w.ctx, name, bucket.Patch{Policy: &policy})
			deleteErr := w.service.Delete(w.ctx, name)
			_, getErr := w.service.Get(w.ctx, name)
			for op, err := range map[string]error{"create": createErr, "update": updateErr, "delete": deleteErr, "get": getErr} {
				if !errors.Is(err, errs.ErrInvalidBucketName) || !errors.Is(err, bucket.ErrInvalidBucketName) {
					t.Fatalf("%s error = %v, want an invalid bucket name", op, err)
				}
			}
			if len(w.network.ChannelIDs()) != channelsBefore || len(w.registryTexts()) != sentBefore {
				t.Fatal("an invalid name reached Telegram")
			}
		})
	}
}

func TestCreateRejectsInvalidConfig(t *testing.T) {
	w := newWorld(t)
	w.linkRegistry()
	tests := []struct {
		name    string
		spec    bucket.Spec
		wantErr error
	}{
		{name: "policy", spec: bucket.Spec{Name: "photos", Policy: "open", DefaultEncryption: bucket.EncryptionNone}, wantErr: bucket.ErrInvalidPolicy},
		{name: "encryption", spec: bucket.Spec{Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: "aes"}, wantErr: bucket.ErrInvalidEncryption},
		{name: "cache age", spec: bucket.Spec{Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone, CacheMaxAge: -1}, wantErr: bucket.ErrInvalidCacheMaxAge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := w.service.Create(w.ctx, test.spec); !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
	if got := len(w.network.ChannelIDs()); got != 1 {
		t.Fatalf("channels = %d, want only the registry", got)
	}
}

func TestGetAndList(t *testing.T) {
	w := newWorld(t)
	w.linkRegistry()
	w.createBucket("zeta", bucket.EncryptionNone)
	w.createBucket("alpha", bucket.EncryptionNone)

	infos, err := w.service.List(w.ctx)
	if err != nil || len(infos) != 2 || infos[0].Name != "alpha" || infos[1].Name != "zeta" {
		t.Fatalf("List = %+v, %v", infos, err)
	}
	if info, err := w.service.Get(w.ctx, "zeta"); err != nil || info.Name != "zeta" {
		t.Fatalf("Get = %+v, %v", info, err)
	}
	if _, err := w.service.Get(w.ctx, "missing"); !errors.Is(err, errs.ErrNoSuchBucket) {
		t.Fatalf("Get missing error = %v", err)
	}
}

func TestStaleAccessHashIsRefreshed(t *testing.T) {
	w := newWorld(t)
	w.linkRegistry()
	w.exec(`UPDATE channels SET access_hash = 12345 WHERE channel_id = ?1`, w.registryID())

	w.createBucket("photos", bucket.EncryptionNone)

	row, _, err := w.channels.Channel(w.ctx, w.registryID())
	if err != nil || row.AccessHash == 12345 || row.AccessHash == 0 {
		t.Fatalf("registry access hash = %d, err %v", row.AccessHash, err)
	}
}

func TestSendFailureStopsBeforeProjection(t *testing.T) {
	w := newWorld(t)
	w.linkRegistry()
	flood := faketg.Fault{Kind: faketg.FaultFloodWait, Wait: 30 * time.Second}
	if err := w.telegram.QueueFaults(faketg.MethodSendControlIdempotent, flood); err != nil {
		t.Fatal(err)
	}
	writesBefore := w.writer.writes

	_, err := w.service.Create(w.ctx, bucket.Spec{Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone})
	var floodErr *tgclient.FloodWaitError
	if !errors.As(err, &floodErr) || floodErr.Duration != 30*time.Second {
		t.Fatalf("error = %v, want the flood wait to keep its type", err)
	}
	if w.writer.writes != writesBefore {
		t.Fatal("a failed send was projected")
	}
	if got := w.telegram.CallCount(faketg.MethodSendControlIdempotent); got != 2 {
		t.Fatalf("send calls = %d, want the registry marker and one refused bcreate", got)
	}
}

func TestContextCancelledWhileWaitingForRegistryGate(t *testing.T) {
	w := newWorld(t)
	w.linkRegistry()
	release, err := w.service.lockRegistry(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(w.ctx, 20*time.Millisecond)
	defer cancel()

	_, err = w.service.Create(ctx, bucket.Spec{Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestNewServiceRejectsNilDependencies(t *testing.T) {
	w := newWorld(t)
	mutations := map[string]func(d *Dependencies){
		"channels":     func(d *Dependencies) { d.Publish.Channels = nil },
		"resolver":     func(d *Dependencies) { d.Publish.Resolver = nil },
		"messages":     func(d *Dependencies) { d.Publish.Messages = nil },
		"sender":       func(d *Dependencies) { d.Publish.Sender = nil },
		"projector":    func(d *Dependencies) { d.Publish.Projector = nil },
		"writer":       func(d *Dependencies) { d.Publish.Writer = nil },
		"random id":    func(d *Dependencies) { d.Publish.StableRandomID = nil },
		"clock":        func(d *Dependencies) { d.Publish.Clock = nil },
		"logger":       func(d *Dependencies) { d.Publish.Logger = nil },
		"actor":        func(d *Dependencies) { d.Publish.ActorUserID = 0 },
		"buckets":      func(d *Dependencies) { d.Buckets = nil },
		"scanner":      func(d *Dependencies) { d.Scanner = nil },
		"gate":         func(d *Dependencies) { d.Gate = nil },
		"creator":      func(d *Dependencies) { d.Creator = nil },
		"keys":         func(d *Dependencies) { d.Keys = nil },
		"key presence": func(d *Dependencies) { d.KeyPresence = nil },
		"jobs":         func(d *Dependencies) { d.Jobs = nil },
		"write gates":  func(d *Dependencies) { d.WriteGates = nil },
		"random":       func(d *Dependencies) { d.Random = nil },
		"deployment":   func(d *Dependencies) { d.DeploymentID = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			deps := w.deps
			mutate(&deps)
			if _, err := NewService(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("error = %v, want ErrMissingDependency", err)
			}
		})
	}
}
