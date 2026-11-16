package phase09

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/tlscert"
)

func TestCertificateFingerprintIsPinned(t *testing.T) {
	ctx := context.Background()

	t.Run("client pinned to the logged fingerprint succeeds", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		logged := w.logs.find("admin listener", "tls_fingerprint")
		if logged == "" || logged != w.spki {
			t.Fatalf("logged fingerprint %q, served pin %q", logged, w.spki)
		}
		if display := w.logs.find("admin listener", "tls_fingerprint_display"); display == "" || len(display) != 16*4+15 {
			t.Fatalf("logged display fingerprint %q", display)
		}
		info, err := w.client("").ServerInfo(ctx)
		if err != nil {
			t.Fatalf("pinned ServerInfo: %v", err)
		}
		if info.TLS.SPKISHA256 != logged {
			t.Fatalf("server-info pin %q differs from the logged pin %q", info.TLS.SPKISHA256, logged)
		}
		probed, err := adminv1.ProbeCertificate(ctx, w.baseURL)
		if err != nil {
			t.Fatalf("ProbeCertificate: %v", err)
		}
		if probed.SPKISHA256 != logged {
			t.Fatalf("probed pin %q, want %q", probed.SPKISHA256, logged)
		}
	})

	t.Run("a wrong pin is refused before any request reaches the handler", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		var requests atomic.Int64
		counting := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.admin.Handler().ServeHTTP(rw, r)
		})
		pair, err := tls.LoadX509KeyPair(filepath.Join(w.dir, "tls", "admin.crt"), filepath.Join(w.dir, "tls", "admin.key"))
		if err != nil {
			t.Fatalf("load admin certificate files: %v", err)
		}
		server := httptest.NewUnstartedServer(counting)
		server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
		server.StartTLS()
		defer server.Close()

		wrong := "SHA256:" + repeat("0", 64)
		bad, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: server.URL, Trust: adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: wrong}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = bad.ServerInfo(ctx)
		var mismatch *adminv1.PinMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("ServerInfo with a wrong pin = %v, want *PinMismatchError", err)
		}
		if mismatch.Expected != wrong || mismatch.Got != w.spki {
			t.Fatalf("mismatch = %+v, want expected %s got %s", mismatch, wrong, w.spki)
		}
		if requests.Load() != 0 {
			t.Fatalf("%d request(s) reached the handler despite the pin mismatch", requests.Load())
		}
		good, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: server.URL, Trust: adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: w.spki}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := good.ServerInfo(ctx); err != nil || requests.Load() != 1 {
			t.Fatalf("ServerInfo with the right pin = %v after %d request(s)", err, requests.Load())
		}
	})

	t.Run("the pin survives a restart and the listener refuses TLS 1.2", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		first := w.spki
		w.stopAdmin()
		w.startAdmin()
		if w.spki != first {
			t.Fatalf("pin changed across a restart: %s then %s", first, w.spki)
		}
		if _, err := w.client("").ServerInfo(ctx); err != nil {
			t.Fatalf("ServerInfo after restart: %v", err)
		}
		dialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}}
		if conn, err := dialer.DialContext(ctx, "tcp", w.admin.Addr()); err == nil {
			_ = conn.Close()
			t.Fatal("the admin listener completed a TLS 1.2 handshake")
		}
	})

	t.Run("the served key matches the certificate on disk", func(t *testing.T) {
		w := newWorld(t)
		w.startAdmin()
		material, err := tlscert.LoadOrCreate(w.dir, w.serverID, nil, w.clk.Now())
		if err != nil {
			t.Fatalf("LoadOrCreate: %v", err)
		}
		if got := tlscert.Fingerprint(material.SPKISHA256); got != w.spki {
			t.Fatalf("on-disk key pin %s, served pin %s", got, w.spki)
		}
	})
}

func TestCertificateFilesModeReloadsOnRequest(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	now := w.clk.Now()
	first, err := tlscert.LoadOrCreate(t.TempDir(), "files-mode", []string{"localhost"}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tlscert.LoadOrCreate(t.TempDir(), "files-mode-two", []string{"localhost"}, now)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(t.TempDir(), "chain.pem"), filepath.Join(t.TempDir(), "key.pem")
	install := func(material tlscert.Material) {
		t.Helper()
		chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: material.Certificate.Certificate[0]})
		der, err := x509.MarshalPKCS8PrivateKey(material.Certificate.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(certFile, chain, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	install(first)
	w.cfg.Admin.TLSMode = "files"
	w.cfg.Admin.TLSCertFile, w.cfg.Admin.TLSKeyFile = certFile, keyFile
	w.startAdmin()
	if w.spki != tlscert.Fingerprint(first.SPKISHA256) {
		t.Fatalf("served pin %s, want the first certificate %s", w.spki, tlscert.Fingerprint(first.SPKISHA256))
	}
	info, err := w.client("").ServerInfo(ctx)
	if err != nil || info.TLS.Mode != "files" || info.TLS.SPKISHA256 != w.spki {
		t.Fatalf("server info = %+v, %v", info, err)
	}

	install(second)
	if err := w.admin.ReloadCertificate(); err != nil {
		t.Fatalf("ReloadCertificate: %v", err)
	}
	reloaded := tlscert.Fingerprint(second.SPKISHA256)
	pin, _ := w.admin.Fingerprint()
	if pin != reloaded {
		t.Fatalf("pin after reload %s, want %s", pin, reloaded)
	}
	w.spki = reloaded
	if info, err := w.client("").ServerInfo(ctx); err != nil || info.TLS.SPKISHA256 != reloaded {
		t.Fatalf("server info after reload = %+v, %v", info, err)
	}
	stale, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: w.baseURL, Trust: adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: tlscert.Fingerprint(first.SPKISHA256)}})
	if err != nil {
		t.Fatal(err)
	}
	var mismatch *adminv1.PinMismatchError
	if _, err := stale.ServerInfo(ctx); !errors.As(err, &mismatch) {
		t.Fatalf("a client pinned to the old key = %v, want *PinMismatchError", err)
	}

	if err := os.WriteFile(keyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.admin.ReloadCertificate(); err == nil {
		t.Fatal("a broken key file reloaded")
	}
	if _, err := w.client("").ServerInfo(ctx); err != nil {
		t.Fatalf("a failed reload broke the running certificate: %v", err)
	}
}
