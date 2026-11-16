package vm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func (v *ViewModel) Connect(ctx context.Context, serverID string) {
	if !v.begin() {
		return
	}
	defer v.end()
	profile, err := v.deps.Profiles.Get(serverID)
	if err != nil {
		v.fail(err)
		return
	}
	store := v.deps.Tokens.forKind(profile.TokenStorage)
	token, err := store.Get(serverID)
	if err != nil {
		v.startRepair(ctx, profile, fmt.Sprintf(textNoTokenStored, profile.Label))
		return
	}
	client, err := v.deps.Dialer.Dial(adminv1.ClientConfig{
		BaseURL:     profile.BaseURL,
		Trust:       profile.Trust,
		Token:       token,
		UserAgent:   v.deps.UserAgent,
		JSONTimeout: jsonTimeout,
	})
	if err != nil {
		v.fail(err)
		return
	}
	info, err := client.ServerInfo(ctx)
	if err != nil {
		v.handleConnectError(ctx, profile, err)
		return
	}
	if info.APIVersion != apiVersionV1 {
		v.failWith(dialogProblem(versionMismatchText(info.APIVersion)))
		return
	}
	if info.ServerID != "" && info.ServerID != profile.ServerID {
		v.failWith(dialogProblem(textServerIDChanged))
		return
	}
	notice, err := v.rotateIfStale(ctx, client, store, &profile)
	if err != nil {
		v.handleConnectError(ctx, profile, err)
		return
	}
	v.markConnected(profile, notice)
}

func (v *ViewModel) rotateIfStale(ctx context.Context, client ConnectClient, store model.TokenStore, profile *model.Profile) (string, error) {
	issued := profile.TokenIssuedAt
	if issued.IsZero() || v.deps.Now().Sub(issued) <= tokenRotationAge {
		return "", nil
	}
	creds, err := client.RotateDeviceToken(ctx)
	if err != nil {
		if isSessionEnding(err) {
			return "", err
		}
		return textRotateFailed, nil
	}
	if err := store.Set(profile.ServerID, creds.DeviceToken); err != nil {
		return textRotateNotSaved, nil
	}
	client.SetToken(creds.DeviceToken)
	profile.TokenIssuedAt = issuedAt(creds, v.deps.Now())
	return "", nil
}

func isSessionEnding(err error) bool {
	return adminv1.IsCode(err, adminv1.CodeUnauthenticated) || adminv1.IsCode(err, adminv1.CodeDeviceRevoked)
}

func (v *ViewModel) markConnected(profile model.Profile, notice string) {
	profile.LastUsedAt = v.deps.Now()
	if err := v.deps.Profiles.Put(profile); err != nil {
		v.fail(err)
		return
	}
	if err := v.deps.Profiles.SetLastUsed(profile.ServerID); err != nil {
		v.fail(err)
		return
	}
	v.update(func(s *State) {
		s.Step = StepProfiles
		s.Notice = notice
		s.Mismatch = nil
	})
	v.refreshProfiles()
	v.deps.OnConnected(profile)
}

func (v *ViewModel) handleConnectError(ctx context.Context, profile model.Profile, err error) {
	if v.routeSessionError(ctx, profile, err) {
		return
	}
	v.fail(err)
}

func (v *ViewModel) HandleSessionError(ctx context.Context, serverID string, err error) bool {
	profile, getErr := v.deps.Profiles.Get(serverID)
	if getErr != nil {
		return false
	}
	return v.routeSessionError(ctx, profile, err)
}

func (v *ViewModel) routeSessionError(ctx context.Context, profile model.Profile, err error) bool {
	var mismatch *adminv1.PinMismatchError
	switch {
	case errors.As(err, &mismatch) && mismatch != nil:
		v.blockOnMismatch(profile, mismatch)
		return true
	case isSessionEnding(err):
		v.deps.Tokens.forKind(profile.TokenStorage).Delete(profile.ServerID)
		v.startRepair(ctx, profile, signedOutNotice(err, profile.Label))
		return true
	default:
		return false
	}
}

func signedOutNotice(err error, label string) string {
	if adminv1.IsCode(err, adminv1.CodeDeviceRevoked) {
		return revokedNotice(err)
	}
	return fmt.Sprintf(textSignedOut, label)
}

func (v *ViewModel) blockOnMismatch(profile model.Profile, mismatch *adminv1.PinMismatchError) {
	v.setPending(&pendingServer{
		baseURL:  profile.BaseURL,
		label:    profile.Label,
		trust:    profile.Trust,
		existing: &profile,
		rebind:   true,
	})
	v.update(func(s *State) {
		s.Step = StepPinMismatch
		s.Problem = nil
		s.Mismatch = &MismatchView{
			ServerLabel: profile.Label,
			Expected:    FingerprintLines(mismatch.Expected),
			Received:    FingerprintLines(mismatch.Got),
		}
	})
}

func (v *ViewModel) startRepair(ctx context.Context, profile model.Profile, notice string) {
	pending := &pendingServer{
		baseURL:  profile.BaseURL,
		label:    profile.Label,
		trust:    profile.Trust,
		existing: &profile,
	}
	client, err := v.deps.Dialer.Dial(adminv1.ClientConfig{
		BaseURL:     profile.BaseURL,
		Trust:       profile.Trust,
		UserAgent:   v.deps.UserAgent,
		JSONTimeout: jsonTimeout,
	})
	if err != nil {
		v.fail(err)
		return
	}
	pending.client = client
	info, infoErr := client.ServerInfo(ctx)
	if infoErr != nil && v.routeSessionError(ctx, profile, infoErr) {
		return
	}
	pending.info = info
	v.setPending(pending)
	v.update(func(s *State) {
		s.Notice = notice
		s.ServerLabel = profile.Label
		s.Certificate = nil
		s.Mismatch = nil
		s.SavedMessagesEnabled = slices.Contains(info.PairingMethods, deliverySavedMsgs)
		s.Step = StepPair
		if info.State == "unclaimed" {
			s.Step = StepClaim
		}
	})
	if infoErr != nil {
		v.fail(infoErr)
	}
}

func (v *ViewModel) TrustNewKey(ctx context.Context) {
	pending := v.currentPending()
	if pending == nil || pending.existing == nil || v.state.Get().Step != StepPinMismatch {
		return
	}
	if !v.begin() {
		return
	}
	defer v.end()
	probe := &pendingServer{
		baseURL:  pending.existing.BaseURL,
		label:    pending.existing.Label,
		existing: pending.existing,
		rebind:   true,
	}
	v.probeCertificate(ctx, probe)
}
