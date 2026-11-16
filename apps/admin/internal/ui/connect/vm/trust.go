package vm

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func isProfileMissing(err error) bool {
	return errors.Is(err, model.ErrProfileNotFound)
}

func (v *ViewModel) Continue(ctx context.Context, rawURL, label string) {
	if !v.begin() {
		return
	}
	defer v.end()
	normalised, err := NormaliseURL(rawURL)
	if err != nil {
		v.failWith(urlProblem(err))
		return
	}
	v.update(func(s *State) {
		s.URL = normalised
		s.PlainHTTP = IsPlainHTTP(normalised)
	})
	pending := &pendingServer{baseURL: normalised, label: resolveLabel(label, normalised)}
	if IsPlainHTTP(normalised) {
		pending.trust = adminv1.Trust{Mode: adminv1.TrustSystem}
		v.inspectServer(ctx, pending)
		return
	}
	v.probeCertificate(ctx, pending)
}

func resolveLabel(label, normalised string) string {
	if trimmed := strings.TrimSpace(label); trimmed != "" {
		return trimmed
	}
	return hostLabel(normalised)
}

func (v *ViewModel) probeCertificate(ctx context.Context, pending *pendingServer) bool {
	info, err := v.deps.Dialer.Probe(ctx, pending.baseURL)
	if err != nil {
		v.fail(err)
		return false
	}
	pending.cert = &info
	v.setPending(pending)
	v.update(func(s *State) {
		s.Step = StepConfirmCertificate
		s.Rebinding = pending.rebind
		s.ServerLabel = pending.label
		s.Certificate = &CertificateView{
			Fingerprint:             FingerprintLines(info.DisplayFingerprint),
			Subject:                 info.Subject,
			DNSNames:                slices.Clone(info.DNSNames),
			IPAddresses:             slices.Clone(info.IPAddresses),
			NotAfter:                info.NotAfter,
			VerifiesWithSystemRoots: info.VerifiesWithSystemRoots,
		}
	})
	return true
}

func (v *ViewModel) ConfirmCertificate(ctx context.Context, pinKey bool) {
	pending := v.currentPending()
	if pending == nil || pending.cert == nil || v.state.Get().Step != StepConfirmCertificate {
		return
	}
	if !v.begin() {
		return
	}
	defer v.end()
	pending.trust = trustFor(*pending.cert, pinKey)
	v.inspectServer(ctx, pending)
}

func trustFor(cert adminv1.CertificateInfo, pinKey bool) adminv1.Trust {
	if cert.VerifiesWithSystemRoots && !pinKey {
		return adminv1.Trust{Mode: adminv1.TrustSystem}
	}
	return adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: cert.SPKISHA256}
}

func (v *ViewModel) inspectServer(ctx context.Context, pending *pendingServer) {
	client, err := v.deps.Dialer.Dial(adminv1.ClientConfig{
		BaseURL:     pending.baseURL,
		Trust:       pending.trust,
		UserAgent:   v.deps.UserAgent,
		JSONTimeout: jsonTimeout,
	})
	if err != nil {
		v.abortInspection(err, nil)
		return
	}
	info, err := client.ServerInfo(ctx)
	if err != nil {
		v.abortInspection(err, nil)
		return
	}
	if problem := v.checkServerInfo(pending, info); problem != nil {
		v.abortInspection(nil, problem)
		return
	}
	pending.client = client
	pending.info = info
	v.setPending(pending)
	v.routeAfterInspection(pending)
}

func (v *ViewModel) abortInspection(err error, problem *model.Problem) {
	v.clearFlow()
	if err != nil {
		problem = describe(err, v.deps.Now())
	}
	v.update(func(s *State) {
		s.Step = StepProfiles
		s.Certificate = nil
		s.Rebinding = false
		s.Problem = problem
	})
	v.refreshProfiles()
}

func (v *ViewModel) checkServerInfo(pending *pendingServer, info adminv1.ServerInfo) *model.Problem {
	if info.APIVersion != apiVersionV1 {
		return dialogProblem(versionMismatchText(info.APIVersion))
	}
	if info.ServerID == "" {
		return dialogProblem(textServerIDMissing)
	}
	reported := canonicalPin(info.TLS.SPKISHA256)
	if pending.cert != nil && reported != "" && reported != canonicalPin(pending.cert.SPKISHA256) {
		return dialogProblem(textPinDisagrees)
	}
	if pending.existing != nil && pending.existing.ServerID != info.ServerID {
		return dialogProblem(textServerIDChanged)
	}
	return nil
}

func (v *ViewModel) routeAfterInspection(pending *pendingServer) {
	if existing := v.existingProfile(pending); existing != nil {
		pending.existing = existing
		if v.hasStoredToken(*existing) {
			v.adoptKnownServer(pending)
			return
		}
	}
	v.update(func(s *State) {
		s.Certificate = nil
		s.Rebinding = false
		s.ServerLabel = pending.label
		s.SavedMessagesEnabled = slices.Contains(pending.info.PairingMethods, deliverySavedMsgs)
		if pending.info.State == "unclaimed" {
			s.Step = StepClaim
			return
		}
		s.Step = StepPair
	})
}

func (v *ViewModel) existingProfile(pending *pendingServer) *model.Profile {
	if pending.existing != nil {
		return pending.existing
	}
	profile, err := v.deps.Profiles.Get(pending.info.ServerID)
	if err != nil {
		return nil
	}
	return &profile
}

func (v *ViewModel) hasStoredToken(p model.Profile) bool {
	_, err := v.deps.Tokens.forKind(p.TokenStorage).Get(p.ServerID)
	return err == nil
}

func (v *ViewModel) adoptKnownServer(pending *pendingServer) {
	profile := *pending.existing
	profile.BaseURL = pending.baseURL
	profile.Trust = pending.trust
	profile.LastUsedAt = v.deps.Now()
	if err := v.deps.Profiles.Put(profile); err != nil {
		v.abortInspection(err, nil)
		return
	}
	if err := v.deps.Profiles.SetLastUsed(profile.ServerID); err != nil {
		v.abortInspection(err, nil)
		return
	}
	v.clearFlow()
	v.update(func(s *State) {
		s.Step = StepProfiles
		s.Certificate = nil
		s.Rebinding = false
		s.Mismatch = nil
	})
	v.refreshProfiles()
	v.deps.OnConnected(profile)
}
