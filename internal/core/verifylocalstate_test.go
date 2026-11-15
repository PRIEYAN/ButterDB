package core

import (
	"context"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
)

func TestAppVerifyLocalState(t *testing.T) {
	ctx := context.Background()
	app := startApp(t, sessionDeps(t, privateDir(t)))
	if err := app.VerifyLocalState(ctx); err != nil {
		t.Fatalf("fresh data directory: %v", err)
	}
	session := openSessionFor(t, app, identity.RolePrimary)
	if err := app.VerifyLocalState(ctx); err != nil {
		t.Fatalf("control database without an account: %v", err)
	}
	err := session.accounts.Link(ctx, accountrepo.Record{Role: "primary", UserID: 777000123, Linked: true, LinkedAt: 1, BoundAt: 1})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	var startup *StartupError
	if err := app.VerifyLocalState(ctx); !errors.As(err, &startup) || startup.Reason != ReasonRegistryUnknown {
		t.Fatalf("linked account without state = %v, want registry_unknown", err)
	}
	writer, err := secretfile.New(fsx.OS{})
	if err != nil {
		t.Fatal(err)
	}
	stateFile, err := newRegistryStateFile(app.DataDir().Join(stateFileName), writer, unknownDeployment{})
	if err != nil {
		t.Fatal(err)
	}
	state := validState()
	state.KeyFingerprint = "bkf1_ffffffffffffffffffffffffffffffff"
	if err := stateFile.SaveRegistryState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := app.VerifyLocalState(ctx); !errors.As(err, &startup) || startup.Reason != ReasonKEKMismatch {
		t.Fatalf("foreign fingerprint = %v, want kek_mismatch", err)
	}
	state.KeyFingerprint = session.ring.FingerprintString()
	if err := stateFile.SaveRegistryState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := app.VerifyLocalState(ctx); err != nil {
		t.Fatalf("matching state: %v", err)
	}
}
