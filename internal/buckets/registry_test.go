package buckets

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
)

func TestRegistryCreatedOnceAtLink(t *testing.T) {
	t.Run("second link creates nothing", func(t *testing.T) {
		w := newWorld(t)
		first := w.linkRegistry()
		second := w.linkRegistry()

		if first != second {
			t.Fatalf("states differ: %+v vs %+v", first, second)
		}
		if got := len(w.network.ChannelIDs()); got != 1 {
			t.Fatalf("telegram channels = %d, want 1", got)
		}
		if got := w.telegram.CallCount(faketg.MethodCreateChannel); got != 1 {
			t.Fatalf("createChannel calls = %d, want 1", got)
		}
		if got := joinTypes(opTypes(w.registryTexts())); got != "reg" {
			t.Fatalf("registry ops = %s, want one marker", got)
		}
		if w.state.saves != 1 {
			t.Fatalf("state saves = %d, want 1", w.state.saves)
		}
	})

	t.Run("channel row, marker and state agree", func(t *testing.T) {
		w := newWorld(t)
		state := w.linkRegistry()

		row, found, err := w.channels.Channel(w.ctx, state.ChannelID)
		if err != nil || !found {
			t.Fatalf("registry row: found=%v err=%v", found, err)
		}
		if row.Kind != syncer.KindRegistry || row.AccessHash == 0 || row.AccessHash != state.AccessHash || !row.InitialSyncDone || row.Title != "butterDB registry" {
			t.Fatalf("registry row = %+v, state %+v", row, state)
		}
		if state.PrimaryUserID != testUserID || state.KeyFingerprint != testFingerprint || state.DeploymentID != testDeploymentID {
			t.Fatalf("state = %+v", state)
		}
		if got := w.count(`SELECT COUNT(*) FROM registry_markers WHERE channel_id = ?1 AND msg_id = ?2 AND deployment_id = ?3 AND kek_fingerprint = ?4 AND primary_user_id = ?5`,
			state.ChannelID, state.MarkerMsgID, testDeploymentID, testFingerprint, testUserID); got != 1 {
			t.Fatalf("matching marker rows = %d, want 1", got)
		}
		marker := w.registryTexts()[0]
		for _, want := range []string{"t=reg", "dep=" + testDeploymentID, "kfp=" + testFingerprint, "w=sys.setup"} {
			if !strings.Contains(marker, want) {
				t.Fatalf("marker %q is missing %q", marker, want)
			}
		}
		if len(w.gate.acquired) != 1 || w.gate.acquired[0] != state.ChannelID {
			t.Fatalf("gate acquisitions = %v", w.gate.acquired)
		}
	})

	t.Run("crash before state.json resumes the same channel and marker", func(t *testing.T) {
		w := newWorld(t)
		w.state.failNext = 1

		if _, err := w.provider.Ensure(w.ctx); err == nil {
			t.Fatal("Ensure succeeded although state.json could not be written")
		}
		w.clock.Advance(2 * time.Minute)
		state := w.linkRegistry()

		if got := len(w.network.ChannelIDs()); got != 1 {
			t.Fatalf("telegram channels = %d, want 1", got)
		}
		if got := joinTypes(opTypes(w.registryTexts())); got != "reg" {
			t.Fatalf("registry ops = %s, want one marker", got)
		}
		if got := w.count(`SELECT COUNT(*) FROM registry_markers`); got != 1 {
			t.Fatalf("marker rows = %d, want 1", got)
		}
		if state.MarkerMsgID <= 0 {
			t.Fatalf("state = %+v", state)
		}
		w.requireReplayMatchesTelegram()
	})

	t.Run("projection failure leaves no state file", func(t *testing.T) {
		w := newWorld(t)
		w.writer.failNext = 1

		if _, err := w.provider.Ensure(w.ctx); err == nil {
			t.Fatal("Ensure succeeded although the marker was not projected")
		}
		if w.state.present {
			t.Fatal("state.json written before the marker was projected")
		}
		w.clock.Advance(time.Minute)
		w.linkRegistry()
		if got := joinTypes(opTypes(w.registryTexts())); got != "reg" {
			t.Fatalf("registry ops = %s", got)
		}
		w.requireReplayMatchesTelegram()
	})
}

func TestNewRegistryProvisionerRejectsNilDependencies(t *testing.T) {
	w := newWorld(t)
	base := RegistryDependencies{
		Publish: w.deps.Publish, State: w.state, Scanner: w.channels, Gate: w.gate, Creator: w.telegram,
		DeploymentID: testDeploymentID, KeyFingerprint: testFingerprint,
	}
	mutations := map[string]func(d *RegistryDependencies){
		"publish":     func(d *RegistryDependencies) { d.Publish.Sender = nil },
		"state":       func(d *RegistryDependencies) { d.State = nil },
		"scanner":     func(d *RegistryDependencies) { d.Scanner = nil },
		"gate":        func(d *RegistryDependencies) { d.Gate = nil },
		"creator":     func(d *RegistryDependencies) { d.Creator = nil },
		"deployment":  func(d *RegistryDependencies) { d.DeploymentID = "" },
		"fingerprint": func(d *RegistryDependencies) { d.KeyFingerprint = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			deps := base
			mutate(&deps)
			if _, err := NewRegistryProvisioner(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("error = %v, want ErrMissingDependency", err)
			}
		})
	}
}
