package vm

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type pendingServer struct {
	baseURL     string
	label       string
	trust       adminv1.Trust
	cert        *adminv1.CertificateInfo
	client      ConnectClient
	info        adminv1.ServerInfo
	existing    *model.Profile
	rebind      bool
	credentials *adminv1.DeviceCredentials
}

type ViewModel struct {
	deps    Dependencies
	state   *model.Observable[State]
	pubMu   sync.Mutex
	mu      sync.Mutex
	busy    bool
	pending *pendingServer
}

func New(deps Dependencies) (*ViewModel, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	initial := State{Step: StepProfiles, DefaultDeviceName: deps.DefaultDeviceName}
	return &ViewModel{deps: deps, state: model.NewObservable(initial)}, nil
}

func (v *ViewModel) State() *model.Observable[State] {
	return v.state
}

func (v *ViewModel) update(change func(*State)) {
	v.pubMu.Lock()
	defer v.pubMu.Unlock()
	s := v.state.Get()
	change(&s)
	v.state.Set(s)
}

func (v *ViewModel) acquire() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.busy {
		return false
	}
	v.busy = true
	return true
}

func (v *ViewModel) release() {
	v.mu.Lock()
	v.busy = false
	v.mu.Unlock()
}

func (v *ViewModel) begin() bool {
	if !v.acquire() {
		return false
	}
	v.update(func(s *State) {
		s.Loading = true
		s.Problem = nil
	})
	return true
}

func (v *ViewModel) end() {
	v.release()
	v.update(func(s *State) { s.Loading = false })
}

func (v *ViewModel) fail(err error) {
	problem := describe(err, v.deps.Now())
	v.update(func(s *State) { s.Problem = problem })
}

func (v *ViewModel) failWith(problem *model.Problem) {
	v.update(func(s *State) { s.Problem = problem })
}

func (v *ViewModel) setPending(p *pendingServer) {
	v.mu.Lock()
	v.pending = p
	v.mu.Unlock()
}

func (v *ViewModel) currentPending() *pendingServer {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.pending
}

func (v *ViewModel) Refresh(ctx context.Context) {
	rows, err := v.profileRows()
	v.update(func(s *State) {
		s.Profiles = rows
		if s.Step == "" {
			s.Step = StepProfiles
		}
	})
	if err != nil {
		v.fail(err)
	}
}

func (v *ViewModel) profileRows() ([]ProfileRow, error) {
	profiles, err := v.deps.Profiles.List()
	if err != nil {
		return nil, err
	}
	last, _ := v.deps.Profiles.LastUsed()
	rows := make([]ProfileRow, 0, len(profiles))
	for _, p := range profiles {
		rows = append(rows, ProfileRow{
			ServerID:  p.ServerID,
			Label:     p.Label,
			URL:       p.BaseURL,
			LastUsed:  p.LastUsedAt,
			TrustMode: p.Trust.Mode,
			Active:    p.ServerID == last,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastUsed.After(rows[j].LastUsed) })
	return rows, nil
}

func (v *ViewModel) refreshProfiles() {
	rows, err := v.profileRows()
	v.update(func(s *State) { s.Profiles = rows })
	if err != nil {
		v.fail(err)
	}
}

func (v *ViewModel) AutoConnect(ctx context.Context) {
	v.Refresh(ctx)
	last, err := v.deps.Profiles.LastUsed()
	if err != nil || last == "" {
		return
	}
	profile, err := v.deps.Profiles.Get(last)
	if err != nil {
		return
	}
	if _, err := v.deps.Tokens.forKind(profile.TokenStorage).Get(last); err != nil {
		return
	}
	v.Connect(ctx, last)
}

func (v *ViewModel) Cancel() {
	v.clearFlow()
	v.update(func(s *State) {
		s.Step = StepProfiles
		s.Problem = nil
		s.Notice = ""
		s.Mismatch = nil
		s.Certificate = nil
		s.Rebinding = false
		s.PlainHTTP = false
		s.URL = ""
	})
	v.refreshProfiles()
}

func (v *ViewModel) Disconnect() {
	v.Cancel()
}

func (v *ViewModel) clearFlow() {
	v.mu.Lock()
	if v.pending != nil && v.pending.credentials != nil {
		v.pending.credentials.DeviceToken = ""
	}
	v.pending = nil
	v.mu.Unlock()
	v.update(func(s *State) {
		s.SetupToken = ""
		s.PairingCode = ""
		s.PairingCodeComplete = false
		s.PairingRequested = false
		s.PairingExpiresAt = time.Time{}
		s.SavedMessagesEnabled = false
	})
}

func (v *ViewModel) SetSetupToken(token string) {
	if v.state.Get().SetupToken == token {
		return
	}
	v.update(func(s *State) { s.SetupToken = token })
}

func (v *ViewModel) SetPairingCode(raw string) string {
	formatted := FormatPairingCode(raw)
	if v.state.Get().PairingCode == formatted {
		return formatted
	}
	v.update(func(s *State) {
		s.PairingCode = formatted
		s.PairingCodeComplete = PairingCodeComplete(formatted)
	})
	return formatted
}

func (v *ViewModel) Forget(serverID string) {
	profile, err := v.deps.Profiles.Get(serverID)
	if err == nil {
		v.deps.Tokens.forKind(profile.TokenStorage).Delete(serverID)
	}
	if err := v.deps.Profiles.Delete(serverID); err != nil && !isProfileMissing(err) {
		v.fail(err)
		return
	}
	v.refreshProfiles()
}

func (v *ViewModel) Rename(serverID, label string) {
	label = strings.TrimSpace(label)
	if label == "" {
		return
	}
	profile, err := v.deps.Profiles.Get(serverID)
	if err != nil {
		v.fail(err)
		return
	}
	profile.Label = label
	if err := v.deps.Profiles.Put(profile); err != nil {
		v.fail(err)
		return
	}
	v.refreshProfiles()
}
