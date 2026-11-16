package phase09

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
)

func claimRequest(token string) adminv1.ClaimRequest {
	return adminv1.ClaimRequest{SetupToken: token, DeviceName: deviceName, Platform: "linux/amd64", ClientVersion: "0.9.0"}
}

func TestSetupTokenIsOneTime(t *testing.T) {
	ctx := context.Background()

	t.Run("unclaimed server reports its claim state without a token", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		info, err := w.client("").ServerInfo(ctx)
		if err != nil {
			t.Fatalf("ServerInfo: %v", err)
		}
		if info.State != "unclaimed" || info.TelegramLinked || len(info.PairingMethods) != 0 || info.ServerID != w.serverID {
			t.Fatalf("server info = %+v", info)
		}
		if info.TLS.Mode != "self-signed" || info.TLS.SPKISHA256 != w.spki {
			t.Fatalf("tls info = %+v, want pin %s", info.TLS, w.spki)
		}
	})

	t.Run("only the hash of the token is stored", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		token := w.setupToken()
		var stored []byte
		err := w.control.Read(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT token_hash FROM admin_setup_token WHERE id = 1`).Scan(&stored)
		})
		if err != nil {
			t.Fatalf("read stored token: %v", err)
		}
		want := session.HashSecret(session.KindSetup, token)
		if !bytes.Equal(stored, want[:]) || bytes.Contains(stored, []byte(token)) {
			t.Fatalf("stored hash %x, want %x", stored, want)
		}
		vector := session.HashSecret(session.KindSetup, "bdst1_"+repeat("a", 32))
		if got := hex.EncodeToString(vector[:]); got != "7473e863968ef3cb19b137d5e0f8efb2cf2240fc64aa835d9b0c44f7a37065ee" {
			t.Fatalf("setup hash vector = %s", got)
		}
	})

	t.Run("wrong token is rejected and does not burn the real one", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		_, err := w.client("").Claim(ctx, claimRequest("bdst1_"+repeat("a", 32)))
		requireCode(t, err, adminv1.CodeSetupTokenInvalid)
		if _, creds := w.claim(); creds.DeviceToken == "" {
			t.Fatal("the real token stopped working after a wrong guess")
		}
	})

	t.Run("expired token is rejected", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		token := w.setupToken()
		w.clk.Advance(25 * time.Hour)
		_, err := w.client("").Claim(ctx, claimRequest(token))
		requireCode(t, err, adminv1.CodeSetupTokenInvalid)
	})

	t.Run("a token claims exactly once", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		token := w.setupToken()
		anonymous := w.client("")
		creds, err := anonymous.Claim(ctx, claimRequest(token))
		if err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if creds.ServerID != w.serverID || creds.Device.CreatedVia != "setup" || !creds.Device.Current {
			t.Fatalf("claim result = %+v", creds)
		}
		_, err = anonymous.Claim(ctx, claimRequest(token))
		requireCode(t, err, adminv1.CodeAlreadyClaimed)
		_, err = anonymous.Claim(ctx, claimRequest("bdst1_"+repeat("b", 32)))
		requireCode(t, err, adminv1.CodeAlreadyClaimed)
		info, err := anonymous.ServerInfo(ctx)
		if err != nil || info.State != "claimed" {
			t.Fatalf("server info after claim = %+v, %v", info, err)
		}
		if len(info.PairingMethods) != 1 || info.PairingMethods[0] != "host" {
			t.Fatalf("pairing methods before linking = %v, want [host]", info.PairingMethods)
		}
		if _, err := w.client(creds.DeviceToken).ListDevices(ctx); err != nil {
			t.Fatalf("the claimed device cannot list devices: %v", err)
		}
	})
}

func repeat(text string, count int) string {
	out := make([]byte, 0, len(text)*count)
	for i := 0; i < count; i++ {
		out = append(out, text...)
	}
	return string(out)
}
