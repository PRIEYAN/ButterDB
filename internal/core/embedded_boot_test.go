package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
)

func TestEmbeddedProjectBootStartsWithAlreadyLinkedAccount(t *testing.T) {
	w := newLinkWorld(t, 0)
	w.relay.links = nil
	w.link()
	title, err := buckets.ProjectTitle("archive")
	if err != nil {
		t.Fatal(err)
	}
	w.starter.deps.Embedded = &EmbeddedOptions{Registry: RegistryOptions{
		Create: true, Title: title, Password: []byte("long enough password"),
	}}
	w.starter.deps.Config.ServerKeyFile = filepath.Join(w.dir, "server.key")
	if err := w.starter.openAtBoot(context.Background()); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("open before registry creation = %v, want ErrNotLinked", err)
	}
	w.waitReady(w.waitBackend())
	if _, err := os.Stat(w.statePath()); err != nil {
		t.Fatalf("project registry state was not written: %v", err)
	}
	if report := w.starter.RegistryReport(); report.Status != RegistryVerified {
		t.Fatalf("registry report = %+v, want verified", report)
	}
}
