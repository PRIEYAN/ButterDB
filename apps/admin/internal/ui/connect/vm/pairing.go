package vm

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const setupTokenPrefix = "bdst1_"

func (v *ViewModel) Claim(ctx context.Context, deviceName string) {
	pending := v.currentPending()
	if pending == nil || v.state.Get().Step != StepClaim {
		return
	}
	token := strings.TrimSpace(v.state.Get().SetupToken)
	if !strings.HasPrefix(token, setupTokenPrefix) {
		v.failWith(inlineProblem(FieldSetupToken, textSetupTokenFormat))
		return
	}
	name := strings.TrimSpace(deviceName)
	if !validDeviceName(name) {
		v.failWith(inlineProblem(FieldDeviceName, textDeviceNameInvalid))
		return
	}
	if !v.begin() {
		return
	}
	defer v.end()
	creds, err := pending.client.Claim(ctx, adminv1.ClaimRequest{
		SetupToken:    token,
		DeviceName:    name,
		Platform:      v.deps.Platform,
		ClientVersion: v.deps.ClientVersion,
	})
	if err != nil {
		v.handleEnrolmentError(ctx, pending, err)
		return
	}
	v.storeCredentials(pending, creds)
}

func (v *ViewModel) RequestPairing(ctx context.Context, deviceName string) {
	pending := v.currentPending()
	if pending == nil || v.state.Get().Step != StepPair {
		return
	}
	if !v.state.Get().SavedMessagesEnabled {
		v.failWith(describeCode(adminv1.CodePairingUnavailable, v.deps.Now()))
		return
	}
	name := strings.TrimSpace(deviceName)
	if !validDeviceName(name) {
		v.failWith(inlineProblem(FieldDeviceName, textDeviceNameInvalid))
		return
	}
	if !v.begin() {
		return
	}
	defer v.end()
	requested, err := pending.client.RequestPairing(ctx, adminv1.PairingRequest{
		Delivery:   deliverySavedMsgs,
		DeviceName: name,
		Platform:   v.deps.Platform,
	})
	if err != nil {
		v.handleEnrolmentError(ctx, pending, err)
		return
	}
	v.update(func(s *State) {
		s.PairingRequested = true
		s.PairingExpiresAt = requested.ExpiresAt
	})
}

func (v *ViewModel) CompletePairing(ctx context.Context, deviceName string) {
	pending := v.currentPending()
	if pending == nil || v.state.Get().Step != StepPair {
		return
	}
	code := v.state.Get().PairingCode
	if !PairingCodeComplete(code) {
		v.failWith(inlineProblem(FieldPairCode, textPairingCodeShort))
		return
	}
	name := strings.TrimSpace(deviceName)
	if !validDeviceName(name) {
		v.failWith(inlineProblem(FieldDeviceName, textDeviceNameInvalid))
		return
	}
	if !v.begin() {
		return
	}
	defer v.end()
	creds, err := pending.client.CompletePairing(ctx, adminv1.PairingCompleteRequest{
		Code:          code,
		DeviceName:    name,
		Platform:      v.deps.Platform,
		ClientVersion: v.deps.ClientVersion,
	})
	if err != nil {
		v.handleEnrolmentError(ctx, pending, err)
		return
	}
	v.storeCredentials(pending, creds)
}

func (v *ViewModel) handleEnrolmentError(ctx context.Context, pending *pendingServer, err error) {
	problem := describe(err, v.deps.Now())
	switch {
	case adminv1.IsCode(err, adminv1.CodeAlreadyClaimed):
		v.switchForm(ctx, pending, StepPair)
	case adminv1.IsCode(err, adminv1.CodeNotClaimed):
		v.switchForm(ctx, pending, StepClaim)
	case adminv1.IsCode(err, adminv1.CodePairingCodeInvalid):
		problem = pairingCodeProblem(problem)
		v.update(func(s *State) {
			s.PairingCode = ""
			s.PairingCodeComplete = false
		})
	}
	v.failWith(problem)
}

func pairingCodeProblem(p *model.Problem) *model.Problem {
	if p == nil {
		return nil
	}
	p.Field = FieldPairCode
	p.Presentation = model.PresentInline
	if p.Body == "" {
		p.Body = textPairingCodeRetry
	}
	return p
}

func (v *ViewModel) switchForm(ctx context.Context, pending *pendingServer, step Step) {
	if info, err := pending.client.ServerInfo(ctx); err == nil {
		pending.info = info
	}
	v.update(func(s *State) {
		s.Step = step
		s.SavedMessagesEnabled = slices.Contains(pending.info.PairingMethods, deliverySavedMsgs)
	})
}

func (v *ViewModel) clearSecrets() {
	v.update(func(s *State) {
		s.SetupToken = ""
		s.PairingCode = ""
		s.PairingCodeComplete = false
	})
}

func (v *ViewModel) storeCredentials(pending *pendingServer, creds adminv1.DeviceCredentials) {
	v.clearSecrets()
	store := v.deps.Tokens.Keychain
	err := store.Set(serverIDOf(pending, creds), creds.DeviceToken)
	switch {
	case err == nil:
		v.finishEnrolment(pending, creds, store)
	case errors.Is(err, model.ErrKeychainUnavailable):
		pending.credentials = &creds
		v.setPending(pending)
		v.update(func(s *State) { s.Step = StepStorageChoice })
	default:
		v.fail(err)
	}
}

func (v *ViewModel) ChooseStorage(ctx context.Context, kind model.TokenStorage) {
	pending := v.currentPending()
	if pending == nil || pending.credentials == nil || v.state.Get().Step != StepStorageChoice {
		return
	}
	if kind != model.TokenFile && kind != model.TokenMemory {
		return
	}
	if !v.begin() {
		return
	}
	defer v.end()
	creds := *pending.credentials
	store := v.deps.Tokens.forKind(kind)
	if err := store.Set(serverIDOf(pending, creds), creds.DeviceToken); err != nil {
		v.fail(err)
		return
	}
	pending.credentials = nil
	v.finishEnrolment(pending, creds, store)
}

func serverIDOf(pending *pendingServer, creds adminv1.DeviceCredentials) string {
	if pending.info.ServerID != "" {
		return pending.info.ServerID
	}
	return creds.ServerID
}

func (v *ViewModel) finishEnrolment(pending *pendingServer, creds adminv1.DeviceCredentials, store model.TokenStore) {
	serverID := serverIDOf(pending, creds)
	profile := v.buildProfile(pending, creds, store.Kind(), serverID)
	if err := v.deps.Profiles.Put(profile); err != nil {
		store.Delete(serverID)
		v.fail(err)
		return
	}
	if err := v.deps.Profiles.SetLastUsed(serverID); err != nil {
		v.fail(err)
		return
	}
	v.clearFlow()
	v.update(func(s *State) {
		s.Step = StepProfiles
		s.Notice = ""
	})
	v.refreshProfiles()
	v.deps.OnConnected(profile)
}

func (v *ViewModel) buildProfile(pending *pendingServer, creds adminv1.DeviceCredentials, kind model.TokenStorage, serverID string) model.Profile {
	now := v.deps.Now()
	profile := model.Profile{
		ServerID:      serverID,
		Label:         pending.label,
		BaseURL:       pending.baseURL,
		Trust:         pending.trust,
		DeviceID:      creds.Device.DeviceID,
		DeviceName:    creds.Device.Name,
		TokenStorage:  kind,
		TokenIssuedAt: issuedAt(creds, now),
		AddedAt:       now,
		LastUsedAt:    now,
	}
	if pending.existing != nil {
		profile.Label = pending.existing.Label
		profile.AddedAt = pending.existing.AddedAt
	}
	return profile
}

func issuedAt(creds adminv1.DeviceCredentials, now time.Time) time.Time {
	if creds.Device.TokenIssuedAt.IsZero() {
		return now
	}
	return creds.Device.TokenIssuedAt
}
