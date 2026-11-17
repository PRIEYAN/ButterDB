package embedded

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func TestHostServesLoginAPIInProcessAndLocksTheDataHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "butterdb")
	host, err := Open(Options{DataHome: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	if _, err := Open(Options{DataHome: root}); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Open = %v, want ErrAlreadyRunning", err)
	}

	client, err := adminv1.NewClient(adminv1.ClientConfig{BaseURL: BaseURL, HTTPTransport: host, JSONTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	status, err := client.TelegramStatus(ctx)
	if err != nil {
		t.Fatalf("TelegramStatus: %v", err)
	}
	if status.Credentials.APIHashSet {
		t.Fatalf("fresh host reports saved credentials: %+v", status)
	}
	status, err = client.SaveTelegramCredentials(ctx, adminv1.TelegramCredentialsRequest{APIID: 12345, APIHash: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("SaveTelegramCredentials: %v", err)
	}
	if !status.Credentials.APIHashSet || status.Credentials.APIID != 12345 {
		t.Fatalf("credentials not saved: %+v", status)
	}
	if _, err := host.List(ctx); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("List before login = %v, want ErrNotLoggedIn", err)
	}
	if _, err := host.Create(ctx, "Project-A", []byte("long enough password")); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Create before login = %v, want ErrNotLoggedIn", err)
	}
	if err := host.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(Options{DataHome: root})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	client, _ = adminv1.NewClient(adminv1.ClientConfig{BaseURL: BaseURL, HTTPTransport: reopened, JSONTimeout: 10 * time.Second})
	status, err = client.TelegramStatus(ctx)
	if err != nil || status.Credentials.APIID != 12345 {
		t.Fatalf("after restart status = %+v err %v, want saved api_id", status, err)
	}
	for _, sub := range []string{"account/account.key", "account/control.db"} {
		info, err := os.Stat(filepath.Join(root, sub))
		if err != nil {
			t.Fatalf("missing %s: %v", sub, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s mode %v is readable by others", sub, info.Mode().Perm())
		}
	}
}
