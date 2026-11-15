package core

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const (
	createPrompt = "type yes to create a new registry"
	adoptPrompt  = "channel id of the registry to adopt"
)

func TestSetupRegistryCreatesOnceAndRecordsState(t *testing.T) {
	w := newRegistryWorld(t)
	w.answer(createPrompt, "yes")
	outcome, err := SetupRegistry(context.Background(), w.deps(), RegistryOptions{})
	if err != nil {
		t.Fatalf("SetupRegistry: %v", err)
	}
	if !outcome.Created || outcome.ChannelID <= 0 || len(outcome.DeploymentID) != 26 {
		t.Fatalf("outcome = %+v, want a created registry with a deployment id", outcome)
	}
	info, err := os.Stat(w.statePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file = %v, %v; want mode 0600", info, err)
	}
	if got := w.count(`SELECT COUNT(*) FROM registry_markers`); got != 1 {
		t.Fatalf("registry markers = %d, want 1", got)
	}
	if got := w.count(`SELECT COUNT(*) FROM channels WHERE kind = 'registry' AND initial_sync_done = 1`); got != 1 {
		t.Fatalf("authoritative registry channels = %d, want 1", got)
	}
	again, err := SetupRegistry(context.Background(), w.deps(), RegistryOptions{})
	if err != nil || !again.Existing || again.ChannelID != outcome.ChannelID {
		t.Fatalf("second run = %+v, %v; want the existing registry", again, err)
	}
	if got := len(w.network.SentControls(outcome.ChannelID)); got != 1 {
		t.Fatalf("controls sent on the registry = %d, want 1", got)
	}
}

func TestSetupRegistryRefusesWithoutConfirmation(t *testing.T) {
	w := newRegistryWorld(t)
	w.answer(createPrompt, "no")
	_, err := SetupRegistry(context.Background(), w.deps(), RegistryOptions{})
	if !errors.Is(err, ErrRegistryChoiceUnset) {
		t.Fatalf("error = %v, want ErrRegistryChoiceUnset", err)
	}
	if _, statErr := os.Stat(w.statePath()); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("state file exists after a refused creation: %v", statErr)
	}
	if got := len(w.network.ChannelIDs()); got != 0 {
		t.Fatalf("channels created after a refusal = %d, want 0", got)
	}
}

func TestSetupRegistryAdoptsAfterOperatorConfirmation(t *testing.T) {
	origin := newRegistryWorld(t)
	origin.answer(createPrompt, "yes")
	created, err := SetupRegistry(context.Background(), origin.deps(), RegistryOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fresh := newRegistryWorld(t)
	fresh.network, fresh.client = origin.network, origin.client
	fresh.ring = origin.ring
	fresh.answer(adoptPrompt, "")
	_, err = SetupRegistry(context.Background(), fresh.deps(), RegistryOptions{})
	if !errors.Is(err, ErrRegistryChoiceUnset) {
		t.Fatalf("blank answer error = %v, want ErrRegistryChoiceUnset", err)
	}
	fresh.answer(adoptPrompt, itoa(created.ChannelID))
	adopted, err := SetupRegistry(context.Background(), fresh.deps(), RegistryOptions{})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !adopted.Adopted || adopted.ChannelID != created.ChannelID || adopted.DeploymentID != created.DeploymentID {
		t.Fatalf("adopted = %+v, want channel %d deployment %s", adopted, created.ChannelID, created.DeploymentID)
	}
	meta, err := newAdminMeta(fresh.controlDB, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := meta.DeploymentID(context.Background()); got != created.DeploymentID {
		t.Fatalf("admin_meta server_id = %q, want %q", got, created.DeploymentID)
	}
	if got := fresh.count(`SELECT COUNT(*) FROM channels WHERE kind = 'registry' AND channel_id = ?1`, created.ChannelID); got != 1 {
		t.Fatalf("registry channel rows after adoption = %d, want 1", got)
	}
}

func TestSetupRegistryAdoptsWithRegistryChannelFlag(t *testing.T) {
	origin := newRegistryWorld(t)
	origin.answer(createPrompt, "yes")
	created, err := SetupRegistry(context.Background(), origin.deps(), RegistryOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fresh := newRegistryWorld(t)
	fresh.network, fresh.client, fresh.ring = origin.network, origin.client, origin.ring
	adopted, err := SetupRegistry(context.Background(), fresh.deps(), RegistryOptions{RegistryChannelID: created.ChannelID})
	if err != nil || !adopted.Adopted {
		t.Fatalf("adopt by flag = %+v, %v", adopted, err)
	}
	other := newRegistryWorld(t)
	other.network, other.client, other.ring = origin.network, origin.client, origin.ring
	_, err = SetupRegistry(context.Background(), other.deps(), RegistryOptions{RegistryChannelID: created.ChannelID + 99})
	if !errors.Is(err, ErrKeyOrRegistry) {
		t.Fatalf("unknown channel error = %v, want ErrKeyOrRegistry", err)
	}
}

func TestSetupRegistryRejectsForeignKey(t *testing.T) {
	origin := newRegistryWorld(t)
	origin.answer(createPrompt, "yes")
	created, err := SetupRegistry(context.Background(), origin.deps(), RegistryOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	foreign := newRegistryWorld(t)
	foreign.network, foreign.client = origin.network, origin.client
	for name, opts := range map[string]RegistryOptions{
		"prompted": {},
		"flagged":  {RegistryChannelID: created.ChannelID},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := SetupRegistry(context.Background(), foreign.deps(), opts)
			if !errors.Is(err, ErrKeyOrRegistry) {
				t.Fatalf("error = %v, want ErrKeyOrRegistry", err)
			}
			if _, statErr := os.Stat(foreign.statePath()); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("state file written for a foreign registry: %v", statErr)
			}
		})
	}
}

func TestSetupRegistryRejectsMissingDependencies(t *testing.T) {
	w := newRegistryWorld(t)
	cases := []struct {
		name   string
		mutate func(*RegistrySetupDependencies)
	}{
		{"nil database", func(d *RegistrySetupDependencies) { d.Database = nil }},
		{"nil control", func(d *RegistrySetupDependencies) { d.Control = nil }},
		{"nil telegram", func(d *RegistrySetupDependencies) { d.Telegram = nil }},
		{"nil sender", func(d *RegistrySetupDependencies) { d.Sender = nil }},
		{"nil ring", func(d *RegistrySetupDependencies) { d.Ring = nil }},
		{"nil terminal", func(d *RegistrySetupDependencies) { d.Terminal = nil }},
		{"no user", func(d *RegistrySetupDependencies) { d.PrimaryUserID = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := w.deps()
			tc.mutate(&deps)
			if _, err := SetupRegistry(context.Background(), deps, RegistryOptions{}); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("error = %v, want ErrMissingDependency", err)
			}
		})
	}
}

func TestSetupRegistryResumesAfterCrashBeforeStateFile(t *testing.T) {
	w := newRegistryWorld(t)
	w.answer(createPrompt, "yes")
	first, err := SetupRegistry(context.Background(), w.deps(), RegistryOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Remove(w.statePath()); err != nil {
		t.Fatal(err)
	}
	w.answer(adoptPrompt, itoa(first.ChannelID))
	second, err := SetupRegistry(context.Background(), w.deps(), RegistryOptions{})
	if err != nil || !second.Adopted || second.ChannelID != first.ChannelID {
		t.Fatalf("recovery = %+v, %v; want the stray registry adopted", second, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(w.readState(), &doc); err != nil {
		t.Fatalf("state json: %v", err)
	}
	for _, field := range []string{"version", "registry_channel_id", "registry_access_hash", "registry_marker_msg_id", "primary_user_id", "server_key_fingerprint"} {
		if _, ok := doc[field]; !ok {
			t.Fatalf("state file lacks %s: %s", field, w.readState())
		}
	}
	if len(doc) != 6 {
		t.Fatalf("state file has %d fields, want exactly the 6 of E17", len(doc))
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
