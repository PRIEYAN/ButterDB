package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const testFingerprint = "bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"

type fixedAccounts struct {
	linked bool
}

func (f fixedAccounts) HasPrimaryAccount(context.Context) (bool, error) {
	return f.linked, nil
}

func newStateFile(t *testing.T, w *registryWorld) *registryStateFile {
	t.Helper()
	state, err := newRegistryStateFile(w.statePath(), w.writer, unknownDeployment{})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func validState() buckets.RegistryState {
	return buckets.RegistryState{
		ChannelID:      2208113456,
		AccessHash:     -6633117222220155911,
		MarkerMsgID:    2,
		PrimaryUserID:  777000123,
		KeyFingerprint: testFingerprint,
	}
}

func TestRegistryStateFileRoundTripPreservesUnknownFields(t *testing.T) {
	w := newRegistryWorld(t)
	state := newStateFile(t, w)
	ctx := context.Background()
	if err := state.SaveRegistryState(ctx, validState()); err != nil {
		t.Fatalf("save: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.readState(), &doc); err != nil {
		t.Fatal(err)
	}
	doc["future_field"] = map[string]any{"kept": true}
	extended, _ := json.Marshal(doc)
	if err := os.WriteFile(w.statePath(), extended, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := state.LoadRegistryState(ctx)
	if err != nil || !found {
		t.Fatalf("load = %v, %v; want the state tolerated with an unknown field", found, err)
	}
	loaded.MarkerMsgID = 9
	if err := state.SaveRegistryState(ctx, loaded); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !strings.Contains(string(w.readState()), `"future_field"`) {
		t.Fatalf("unknown field lost on rewrite: %s", w.readState())
	}
	info, err := os.Stat(w.statePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v, %v; want 0600", info, err)
	}
}

func TestRegistryStateFileRejectsInvalidContent(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"not json", `{`},
		{"wrong type", `{"version":1,"registry_channel_id":"x","registry_access_hash":1,"registry_marker_msg_id":2,"primary_user_id":3,"server_key_fingerprint":"bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"}`},
		{"missing field", `{"version":1,"registry_channel_id":5,"registry_access_hash":1,"registry_marker_msg_id":2,"primary_user_id":3}`},
		{"unsupported version", `{"version":2,"registry_channel_id":5,"registry_access_hash":1,"registry_marker_msg_id":2,"primary_user_id":3,"server_key_fingerprint":"bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"}`},
		{"bad fingerprint", `{"version":1,"registry_channel_id":5,"registry_access_hash":1,"registry_marker_msg_id":2,"primary_user_id":3,"server_key_fingerprint":"nope"}`},
		{"zero access hash", `{"version":1,"registry_channel_id":5,"registry_access_hash":0,"registry_marker_msg_id":2,"primary_user_id":3,"server_key_fingerprint":"bkf1_3e9a0c51b7d24f6e8a01c9d2b3f4e5a6"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRegistryWorld(t)
			if err := os.WriteFile(w.statePath(), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := newStateFile(t, w).LoadRegistryState(context.Background())
			var startup *StartupError
			if !errors.As(err, &startup) || startup.Reason != ReasonStateInvalid || !errors.Is(err, ErrKeyOrRegistry) {
				t.Fatalf("error = %v, want state_invalid", err)
			}
		})
	}
}

func TestRegistryStateFileMissingIsNotAnError(t *testing.T) {
	w := newRegistryWorld(t)
	_, found, err := newStateFile(t, w).LoadRegistryState(context.Background())
	if err != nil || found {
		t.Fatalf("load = %v, %v; want absent without error", found, err)
	}
}

func TestLocalStateCheckerReasonCodes(t *testing.T) {
	insertMarker := func(w *registryWorld, channelID, msgID int64, fingerprint string, userID int64, minReader int) {
		t.Helper()
		err := w.projectionDB.Write(context.Background(), func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(context.Background(),
				`INSERT INTO registry_markers (channel_id, msg_id, deployment_id, format_version, min_reader, kek_fingerprint, primary_user_id, writer, msg_date)
VALUES (?1, ?2, '6pzyoxbfwcf6ubwcq5gpvjg5c4', ?5, ?5, ?3, ?4, 'sys.setup', 1763000000)`, channelID, msgID, fingerprint, userID, minReader)
			return execErr
		})
		if err != nil {
			t.Fatalf("insert marker: %v", err)
		}
	}
	insertRegistryChannel := func(w *registryWorld, channelID int64) {
		t.Helper()
		err := w.projectionDB.Write(context.Background(), func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(context.Background(), `INSERT INTO channels (channel_id, kind, access_hash) VALUES (?1, 'registry', 5)`, channelID)
			return execErr
		})
		if err != nil {
			t.Fatalf("insert channel: %v", err)
		}
	}
	state := validState()
	cases := []struct {
		name     string
		prepare  func(w *registryWorld)
		fingerpr string
		linked   bool
		noState  bool
		want     StartupReason
		wantInc  bool
	}{
		{name: "healthy anchored state", prepare: func(w *registryWorld) {
			insertRegistryChannel(w, state.ChannelID)
			insertMarker(w, state.ChannelID, state.MarkerMsgID, state.KeyFingerprint, state.PrimaryUserID, 1)
		}},
		{name: "no state and no account", noState: true},
		{name: "registry unknown", noState: true, linked: true, want: ReasonRegistryUnknown},
		{name: "key file mismatch", fingerpr: "bkf1_ffffffffffffffffffffffffffffffff", want: ReasonKEKMismatch},
		{name: "marker message differs", prepare: func(w *registryWorld) {
			insertMarker(w, state.ChannelID, state.MarkerMsgID+1, state.KeyFingerprint, state.PrimaryUserID, 1)
		}, want: ReasonRegistryMarkerMismatch},
		{name: "marker user differs", prepare: func(w *registryWorld) {
			insertMarker(w, state.ChannelID, state.MarkerMsgID, state.KeyFingerprint, state.PrimaryUserID+1, 1)
		}, want: ReasonRegistryMarkerMismatch},
		{name: "format too new", prepare: func(w *registryWorld) {
			insertMarker(w, state.ChannelID, state.MarkerMsgID, state.KeyFingerprint, state.PrimaryUserID, wire.FormatVersion+1)
		}, want: ReasonFormatTooNew, wantInc: true},
		{name: "projection registry differs", prepare: func(w *registryWorld) {
			insertRegistryChannel(w, state.ChannelID+1)
		}, want: ReasonProjectionMismatch, wantInc: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRegistryWorld(t)
			stateFile := newStateFile(t, w)
			if !tc.noState {
				if err := stateFile.SaveRegistryState(context.Background(), state); err != nil {
					t.Fatal(err)
				}
			}
			if tc.prepare != nil {
				tc.prepare(w)
			}
			fingerprint := tc.fingerpr
			if fingerprint == "" {
				fingerprint = state.KeyFingerprint
			}
			checker, err := NewLocalStateChecker(LocalStateDependencies{
				State: stateFile, Fingerprint: fingerprint, Database: w.projectionDB, Accounts: fixedAccounts{linked: tc.linked},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = checker.Check(context.Background())
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			var startup *StartupError
			if !errors.As(err, &startup) || startup.Reason != tc.want {
				t.Fatalf("Check = %v, want reason %s", err, tc.want)
			}
			if errors.Is(err, ErrIncompatibleData) != tc.wantInc || errors.Is(err, ErrKeyOrRegistry) == tc.wantInc {
				t.Fatalf("Check = %v, wrong exit class (incompatible=%v)", err, tc.wantInc)
			}
		})
	}
}

func TestDeploymentIDIsMintedOnceAndOverwritable(t *testing.T) {
	w := newRegistryWorld(t)
	meta, err := newAdminMeta(w.controlDB, strings.NewReader(strings.Repeat("\x07", 64)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := meta.DeploymentID(ctx); err != nil || got != "" {
		t.Fatalf("empty store = %q, %v", got, err)
	}
	first, err := meta.EnsureDeploymentID(ctx)
	if err != nil || len(first) != 26 {
		t.Fatalf("minted = %q, %v; want 26 characters", first, err)
	}
	if again, _ := meta.EnsureDeploymentID(ctx); again != first {
		t.Fatalf("second ensure = %q, want the stored %q", again, first)
	}
	if err := meta.SetDeploymentID(ctx, "6pzyoxbfwcf6ubwcq5gpvjg5c4"); err != nil {
		t.Fatal(err)
	}
	if got, _ := meta.DeploymentID(ctx); got != "6pzyoxbfwcf6ubwcq5gpvjg5c4" {
		t.Fatalf("overwritten = %q", got)
	}
}
