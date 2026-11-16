package vm

import (
	"context"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type Client interface {
	ServerInfo(ctx context.Context) (adminv1.ServerInfo, error)
	ListDevices(ctx context.Context) (adminv1.DeviceList, error)
	RenameDevice(ctx context.Context, deviceID string, req adminv1.UpdateDeviceRequest) (adminv1.Device, error)
	RevokeDevice(ctx context.Context, deviceID string, confirmLast bool) error
	RotateDeviceToken(ctx context.Context) (adminv1.DeviceCredentials, error)
	TelegramStatus(ctx context.Context) (adminv1.TelegramStatus, error)
	UnlinkTelegram(ctx context.Context) (adminv1.TelegramStatus, error)
	Audit(ctx context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error)
}

type CertificateProber interface {
	Probe(ctx context.Context, baseURL string) (adminv1.CertificateInfo, error)
}

type Preferences interface {
	StringWithFallback(key, fallback string) string
	SetString(key, value string)
	IntWithFallback(key string, fallback int) int
	SetInt(key string, value int)
}

type Session struct {
	Profile            model.Profile
	Tokens             model.TokenStore
	Profiles           model.ProfileStore
	Keychain           model.TokenStore
	Prober             CertificateProber
	SetClientToken     func(token string)
	Preferences        Preferences
	AppVersion         string
	DefaultDownloadDir string
}

type State struct {
	Loading     bool
	Problem     *model.Problem
	Notice      string
	Connection  ConnectionState
	Device      DeviceState
	Devices     DevicesState
	Telegram    TelegramState
	Activity    ActivityState
	About       AboutState
	Preferences PreferencesState
}

type ViewModel struct {
	client Client
	now    func() time.Time
	state  *model.Observable[State]

	mu      sync.Mutex
	session Session

	stateMu sync.Mutex
}

func New(c Client, now func() time.Time, s Session) *ViewModel {
	if now == nil {
		now = time.Now
	}
	v := &ViewModel{client: c, now: now, session: s, state: model.NewObservable(State{})}
	v.state.Set(State{
		Connection:  connectionFor(s, ConnectionState{}),
		Preferences: v.readPreferences(),
		About:       aboutStatic(s.AppVersion, ""),
	})
	return v
}

func (v *ViewModel) State() *model.Observable[State] {
	return v.state
}

func (v *ViewModel) Now() time.Time {
	return v.now()
}

func (v *ViewModel) Refresh(ctx context.Context) {
	v.begin()
	info, infoErr := v.client.ServerInfo(ctx)
	devices, devicesErr := v.client.ListDevices(ctx)
	status, statusErr := v.client.TelegramStatus(ctx)
	v.finish(func(s *State) {
		if infoErr == nil {
			s.Connection = connectionFor(v.snapshot(), s.Connection)
			s.Connection.ServerVersion, s.Connection.ServerState = info.ServerVersion, info.State
			s.About = aboutStatic(v.snapshot().AppVersion, info.ServerVersion)
		}
		if devicesErr == nil {
			v.applyDevices(s, devices)
		}
		if statusErr == nil {
			s.Telegram = TelegramState{Loaded: true, Status: status}
		}
		s.Preferences = v.readPreferences()
	}, v.problemOf(infoErr, devicesErr, statusErr))
}

func (v *ViewModel) snapshot() Session {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.session
}

func (v *ViewModel) begin() {
	v.mutate(func(s *State) {
		s.Loading = true
		s.Problem = nil
		s.Notice = ""
	})
}

func (v *ViewModel) finish(apply func(*State), problem *model.Problem) {
	v.mutate(func(s *State) {
		apply(s)
		s.Loading = false
		s.Problem = problem
	})
}

func (v *ViewModel) fail(problem model.Problem) {
	v.finish(func(*State) {}, &problem)
}

func (v *ViewModel) mutate(fn func(*State)) {
	v.stateMu.Lock()
	s := v.state.Get()
	fn(&s)
	v.stateMu.Unlock()
	v.state.Set(s)
}

func (v *ViewModel) problemOf(errs ...error) *model.Problem {
	for _, err := range errs {
		if err == nil {
			continue
		}
		p := model.Describe(err, v.now())
		if p.Title == "" && p.Code == "" {
			continue
		}
		return &p
	}
	return nil
}
