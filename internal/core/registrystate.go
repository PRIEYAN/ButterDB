package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

const (
	stateFileName     = "state.json"
	stateFileVersion  = 1
	fingerprintPrefix = "bkf1_"
	fingerprintLength = 37
)

var _ buckets.RegistryStateStore = (*registryStateFile)(nil)

type deploymentSource interface {
	DeploymentID(ctx context.Context) (string, error)
}

type stateFileWriter interface {
	Write(path string, data []byte) error
}

type registryStateFile struct {
	path       string
	writer     stateFileWriter
	deployment deploymentSource
}

type stateDocument struct {
	Version              *int    `json:"version"`
	RegistryChannelID    *int64  `json:"registry_channel_id"`
	RegistryAccessHash   *int64  `json:"registry_access_hash"`
	RegistryMarkerMsgID  *int64  `json:"registry_marker_msg_id"`
	PrimaryUserID        *int64  `json:"primary_user_id"`
	ServerKeyFingerprint *string `json:"server_key_fingerprint"`
}

func newRegistryStateFile(path string, writer *secretfile.Writer, deployment deploymentSource) (*registryStateFile, error) {
	switch {
	case path == "":
		return nil, fmt.Errorf("core: registry state: %w: path", errs.ErrMissingDependency)
	case writer == nil:
		return nil, fmt.Errorf("core: registry state: %w: writer", errs.ErrMissingDependency)
	case deployment == nil:
		return nil, fmt.Errorf("core: registry state: %w: deployment source", errs.ErrMissingDependency)
	}
	return &registryStateFile{path: path, writer: writer, deployment: deployment}, nil
}

func NewRegistryStateStore(path string, writer *secretfile.Writer, control *sqlitedb.DB, random io.Reader) (buckets.RegistryStateStore, error) {
	if control == nil {
		return nil, fmt.Errorf("core: registry state: %w: control database", errs.ErrMissingDependency)
	}
	meta, err := newAdminMeta(control, random)
	if err != nil {
		return nil, err
	}
	return newRegistryStateFile(path, writer, meta)
}

func (f *registryStateFile) LoadRegistryState(ctx context.Context) (buckets.RegistryState, bool, error) {
	raw, found, err := f.readRaw()
	if err != nil || !found {
		return buckets.RegistryState{}, false, err
	}
	state, err := decodeState(raw)
	if err != nil {
		return buckets.RegistryState{}, false, err
	}
	deploymentID, err := f.deployment.DeploymentID(ctx)
	if err != nil {
		return buckets.RegistryState{}, false, fmt.Errorf("core: read deployment id: %w", err)
	}
	state.DeploymentID = deploymentID
	return state, true, nil
}

func (f *registryStateFile) SaveRegistryState(_ context.Context, state buckets.RegistryState) error {
	if err := validateState(state); err != nil {
		return err
	}
	preserved, err := f.preservedFields()
	if err != nil {
		return err
	}
	fields := make(map[string]any, len(preserved)+6)
	for name, value := range preserved {
		fields[name] = value
	}
	fields["version"] = stateFileVersion
	fields["registry_channel_id"] = state.ChannelID
	fields["registry_access_hash"] = state.AccessHash
	fields["registry_marker_msg_id"] = state.MarkerMsgID
	fields["primary_user_id"] = state.PrimaryUserID
	fields["server_key_fingerprint"] = state.KeyFingerprint
	encoded, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return fmt.Errorf("core: encode state file: %w", err)
	}
	if err := f.writer.Write(f.path, append(encoded, '\n')); err != nil {
		return fmt.Errorf("core: write state file: %w", err)
	}
	return nil
}

func (f *registryStateFile) readRaw() ([]byte, bool, error) {
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, keyFailure(ReasonStateInvalid, "state file cannot be read")
	}
	return raw, true, nil
}

func (f *registryStateFile) preservedFields() (map[string]json.RawMessage, error) {
	raw, found, err := f.readRaw()
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if !found {
		return fields, nil
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, keyFailure(ReasonStateInvalid, "existing state file is not a json object")
	}
	return fields, nil
}

func decodeState(raw []byte) (buckets.RegistryState, error) {
	var doc stateDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&doc); err != nil {
		return buckets.RegistryState{}, keyFailure(ReasonStateInvalid, "state file does not parse")
	}
	if doc.Version == nil || *doc.Version != stateFileVersion ||
		doc.RegistryChannelID == nil || doc.RegistryAccessHash == nil || doc.RegistryMarkerMsgID == nil ||
		doc.PrimaryUserID == nil || doc.ServerKeyFingerprint == nil {
		return buckets.RegistryState{}, keyFailure(ReasonStateInvalid, "state file is missing a required field")
	}
	state := buckets.RegistryState{
		ChannelID:      *doc.RegistryChannelID,
		AccessHash:     *doc.RegistryAccessHash,
		MarkerMsgID:    *doc.RegistryMarkerMsgID,
		PrimaryUserID:  *doc.PrimaryUserID,
		KeyFingerprint: *doc.ServerKeyFingerprint,
	}
	if err := validateState(state); err != nil {
		return buckets.RegistryState{}, err
	}
	return state, nil
}

func validateState(state buckets.RegistryState) error {
	if state.ChannelID <= 0 || state.AccessHash == 0 || state.MarkerMsgID <= 0 || state.PrimaryUserID <= 0 ||
		len(state.KeyFingerprint) != fingerprintLength || !strings.HasPrefix(state.KeyFingerprint, fingerprintPrefix) {
		return keyFailure(ReasonStateInvalid, "state file holds an out of range value")
	}
	return nil
}
