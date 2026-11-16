package vm

import (
	"context"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	pinPrefix      = "SHA256:"
	groupsPerLine  = 4
	groupWidth     = 4
	maxNameLength  = 64
	plainHTTPLabel = "http://"
)

type ReverifyStatus string

const (
	ReverifyNone          ReverifyStatus = ""
	ReverifyMatch         ReverifyStatus = "match"
	ReverifyChanged       ReverifyStatus = "changed"
	ReverifyNotApplicable ReverifyStatus = "not_applicable"
)

type ReverifyResult struct {
	Status   ReverifyStatus
	Expected string
	Got      string
}

type ConnectionState struct {
	Profile            model.Profile
	ServerVersion      string
	ServerState        string
	FingerprintDisplay string
	TokenStorage       model.TokenStorage
	CanMoveToKeychain  bool
	Reverify           ReverifyResult
}

func connectionFor(s Session, prev ConnectionState) ConnectionState {
	prev.Profile = s.Profile
	prev.TokenStorage = s.Profile.TokenStorage
	prev.FingerprintDisplay = ""
	if s.Profile.Trust.Mode == adminv1.TrustPinned {
		prev.FingerprintDisplay = DisplayFingerprint(s.Profile.Trust.SPKISHA256)
	}
	prev.CanMoveToKeychain = s.Keychain != nil && s.Tokens != nil && s.Profile.TokenStorage != model.TokenKeychain
	return prev
}

func DisplayFingerprint(pin string) string {
	hex := strings.TrimSpace(pin)
	if len(hex) >= len(pinPrefix) && strings.EqualFold(hex[:len(pinPrefix)], pinPrefix) {
		hex = hex[len(pinPrefix):]
	}
	var lines []string
	var groups []string
	for i := 0; i < len(hex); i += groupWidth {
		groups = append(groups, hex[i:min(i+groupWidth, len(hex))])
		if len(groups) == groupsPerLine {
			lines = append(lines, strings.Join(groups, " "))
			groups = nil
		}
	}
	if len(groups) > 0 {
		lines = append(lines, strings.Join(groups, " "))
	}
	return strings.Join(lines, "\n")
}

func (v *ViewModel) RenameServer(label string) {
	v.begin()
	label = strings.TrimSpace(label)
	if n := len([]rune(label)); n < 1 || n > maxNameLength {
		v.fail(inlineProblem(fieldName, textNameLength))
		return
	}
	s := v.snapshot()
	if s.Profiles == nil {
		v.fail(dialogProblem(textNotAvailable))
		return
	}
	updated := s.Profile
	updated.Label = label
	if err := s.Profiles.Put(updated); err != nil {
		v.fail(dialogProblem(textProfileNotSaved))
		return
	}
	v.setProfile(updated)
	v.finish(func(st *State) { st.Connection = connectionFor(v.snapshot(), st.Connection) }, nil)
}

func (v *ViewModel) setProfile(p model.Profile) {
	v.mu.Lock()
	v.session.Profile = p
	v.mu.Unlock()
}

func (v *ViewModel) Reverify(ctx context.Context) ReverifyStatus {
	v.begin()
	s := v.snapshot()
	if s.Prober == nil || strings.HasPrefix(s.Profile.BaseURL, plainHTTPLabel) {
		return v.storeReverify(ReverifyResult{Status: ReverifyNotApplicable}, nil)
	}
	info, err := s.Prober.Probe(ctx, s.Profile.BaseURL)
	if err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return ReverifyNone
	}
	return v.storeReverify(compareCertificate(s.Profile.Trust, info), nil)
}

func compareCertificate(trust adminv1.Trust, info adminv1.CertificateInfo) ReverifyResult {
	if trust.Mode == adminv1.TrustSystem {
		if info.VerifiesWithSystemRoots {
			return ReverifyResult{Status: ReverifyMatch}
		}
		return ReverifyResult{Status: ReverifyChanged, Got: DisplayFingerprint(info.SPKISHA256)}
	}
	result := ReverifyResult{Expected: DisplayFingerprint(trust.SPKISHA256), Got: DisplayFingerprint(info.SPKISHA256)}
	result.Status = ReverifyChanged
	if info.SPKISHA256 == trust.SPKISHA256 {
		result.Status = ReverifyMatch
	}
	return result
}

func (v *ViewModel) storeReverify(r ReverifyResult, problem *model.Problem) ReverifyStatus {
	v.finish(func(st *State) { st.Connection.Reverify = r }, problem)
	return r.Status
}

func (v *ViewModel) MoveToKeychain() bool {
	v.begin()
	s := v.snapshot()
	if s.Keychain == nil || s.Tokens == nil || s.Profiles == nil || s.Profile.TokenStorage == model.TokenKeychain {
		v.fail(dialogProblem(textNotAvailable))
		return false
	}
	token, err := s.Tokens.Get(s.Profile.ServerID)
	if err != nil {
		v.fail(bannerProblem(textKeychainMoveFails))
		return false
	}
	if err := s.Keychain.Set(s.Profile.ServerID, token); err != nil {
		v.fail(bannerProblem(textKeychainUnusable))
		return false
	}
	moved := s.Profile
	moved.TokenStorage = model.TokenKeychain
	if err := s.Profiles.Put(moved); err != nil {
		_ = s.Keychain.Delete(s.Profile.ServerID)
		v.fail(dialogProblem(textProfileNotSaved))
		return false
	}
	_ = s.Tokens.Delete(s.Profile.ServerID)
	v.mu.Lock()
	v.session.Profile = moved
	v.session.Tokens = s.Keychain
	v.mu.Unlock()
	v.finish(func(st *State) { st.Connection = connectionFor(v.snapshot(), st.Connection) }, nil)
	return true
}
