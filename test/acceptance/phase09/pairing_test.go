package phase09

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
)

var savedCodePattern = regexp.MustCompile(`pairing code: ([0-9A-Z]{5}-[0-9A-Z]{5})`)

func pairingRequest() adminv1.PairingRequest {
	return adminv1.PairingRequest{Delivery: "saved_messages", DeviceName: "Home desktop", Platform: "linux/amd64"}
}

func completeRequest(code string) adminv1.PairingCompleteRequest {
	return adminv1.PairingCompleteRequest{Code: code, DeviceName: "Home desktop", Platform: "linux/amd64", ClientVersion: "0.9.0"}
}

func (w *world) latestSavedCode() string {
	w.t.Helper()
	saved := w.telegram.SavedMessages()
	if len(saved) == 0 {
		w.t.Fatal("no saved message was sent")
	}
	match := savedCodePattern.FindStringSubmatch(saved[len(saved)-1].Text)
	if match == nil {
		w.t.Fatalf("saved message %q carries no pairing code", saved[len(saved)-1].Text)
	}
	return match[1]
}

func TestDevicePairingCodeExpires(t *testing.T) {
	ctx := context.Background()

	t.Run("saved messages code works once inside its lifetime", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		owner, _ := w.claim()
		w.linkTelegram(owner)
		anonymous := w.client("")
		requested, err := anonymous.RequestPairing(ctx, pairingRequest())
		if err != nil {
			t.Fatalf("RequestPairing: %v", err)
		}
		if wantExpiry := w.clk.Now().Add(session.PairingCodeTTL); requested.ExpiresAt.Sub(wantExpiry).Abs() > time.Second {
			t.Fatalf("expires_at = %v, want about %v", requested.ExpiresAt, wantExpiry)
		}
		code := w.latestSavedCode()
		paired, err := anonymous.CompletePairing(ctx, completeRequest(code))
		if err != nil {
			t.Fatalf("CompletePairing: %v", err)
		}
		if paired.Device.CreatedVia != "pairing_saved_messages" || paired.ServerID != w.serverID || paired.DeviceToken == "" {
			t.Fatalf("pairing result = %+v", paired)
		}
		_, err = anonymous.CompletePairing(ctx, completeRequest(code))
		requireCode(t, err, adminv1.CodePairingCodeInvalid)
		if _, err := w.client(paired.DeviceToken).ListDevices(ctx); err != nil {
			t.Fatalf("paired device cannot list devices: %v", err)
		}
	})

	t.Run("saved messages code is dead after ten minutes", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		owner, _ := w.claim()
		w.linkTelegram(owner)
		anonymous := w.client("")
		if _, err := anonymous.RequestPairing(ctx, pairingRequest()); err != nil {
			t.Fatalf("RequestPairing: %v", err)
		}
		code := w.latestSavedCode()
		w.clk.Advance(session.PairingCodeTTL + time.Second)
		_, err := anonymous.CompletePairing(ctx, completeRequest(code))
		requireCode(t, err, adminv1.CodePairingCodeInvalid)
	})

	t.Run("host code from the control socket expires the same way", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		w.claim()
		kind, expired, expiresAt, err := session.RequestAdminToken(ctx, w.cfg.Admin.ControlSocket)
		if err != nil || kind != session.KindPairingCode {
			t.Fatalf("RequestAdminToken = %q, %q, %v", kind, expired, err)
		}
		if expiresAt.Sub(w.clk.Now()) > session.PairingCodeTTL+time.Second {
			t.Fatalf("host code expires at %v, more than ten minutes away", expiresAt)
		}
		w.clk.Advance(session.PairingCodeTTL + time.Second)
		anonymous := w.client("")
		_, err = anonymous.CompletePairing(ctx, completeRequest(expired))
		requireCode(t, err, adminv1.CodePairingCodeInvalid)
		_, fresh, _, err := session.RequestAdminToken(ctx, w.cfg.Admin.ControlSocket)
		if err != nil {
			t.Fatalf("second RequestAdminToken: %v", err)
		}
		paired, err := anonymous.CompletePairing(ctx, completeRequest(fresh))
		if err != nil {
			t.Fatalf("CompletePairing with a fresh host code: %v", err)
		}
		if paired.Device.CreatedVia != "pairing_host" {
			t.Fatalf("created_via = %q, want pairing_host", paired.Device.CreatedVia)
		}
	})

	t.Run("a pairing request before telegram is linked is refused and leaves no code", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		w.claim()
		_, err := w.client("").RequestPairing(ctx, pairingRequest())
		requireCode(t, err, adminv1.CodePairingUnavailable)
		if saved := w.telegram.SavedMessages(); len(saved) != 0 {
			t.Fatalf("a saved message was sent while unlinked: %v", saved)
		}
	})

	t.Run("an unclaimed server refuses pairing", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		_, err := w.client("").CompletePairing(ctx, completeRequest("ABCDE-FGHJK"))
		requireCode(t, err, adminv1.CodeNotClaimed)
	})
}
