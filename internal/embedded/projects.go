package embedded

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/syncer/reconcile"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	serverKeyName   = "server.key"
	projectFileName = "project.json"
	stateFileName   = "state.json"
)

type projectFile struct {
	Name       string `json:"name"`
	ChannelID  int64  `json:"channel_id"`
	LastOpened int64  `json:"last_opened"`
}

type projectDir struct {
	path string
	file projectFile
}

func (d projectDir) keyPath() string {
	return filepath.Join(d.path, serverKeyName)
}

func (d projectDir) channelID() int64 {
	if id := stateChannelID(d.path); id != 0 {
		return id
	}
	return d.file.ChannelID
}

func (d projectDir) hasState() bool {
	_, err := os.Stat(filepath.Join(d.path, stateFileName))
	return err == nil
}

func (d projectDir) save() error {
	raw, err := json.Marshal(d.file)
	if err != nil {
		return err
	}
	tmp := filepath.Join(d.path, projectFileName+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("embedded: write project file: %w", err)
	}
	return os.Rename(tmp, filepath.Join(d.path, projectFileName))
}

func stateChannelID(dir string) int64 {
	raw, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		return 0
	}
	var state struct {
		ChannelID int64 `json:"registry_channel_id"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return 0
	}
	return state.ChannelID
}

func (h *Host) localProjects() ([]projectDir, error) {
	entries, err := os.ReadDir(h.projectsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("embedded: list projects: %w", err)
	}
	var out []projectDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := projectDir{path: filepath.Join(h.projectsDir(), e.Name())}
		if raw, err := os.ReadFile(filepath.Join(dir.path, projectFileName)); err == nil {
			_ = json.Unmarshal(raw, &dir.file)
		}
		out = append(out, dir)
	}
	return out, nil
}

func (h *Host) localFor(channelID int64) (projectDir, bool, error) {
	dirs, err := h.localProjects()
	if err != nil {
		return projectDir{}, false, err
	}
	for _, d := range dirs {
		if d.channelID() == channelID {
			return d, true, nil
		}
	}
	return projectDir{}, false, nil
}

func (h *Host) newLocal(name string, channelID int64) (projectDir, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return projectDir{}, err
	}
	dir := projectDir{path: filepath.Join(h.projectsDir(), "p"+hex.EncodeToString(suffix)), file: projectFile{Name: name, ChannelID: channelID}}
	if err := ensureDir(dir.path); err != nil {
		return projectDir{}, err
	}
	return dir, dir.save()
}

func projectID(channelID int64) ProjectID {
	return ProjectID(strconv.FormatInt(channelID, 10))
}

func parseProjectID(id ProjectID) (int64, error) {
	v, err := strconv.ParseInt(string(id), 10, 64)
	if err != nil || v <= 0 {
		return 0, ErrProjectNotFound
	}
	return v, nil
}

func (h *Host) discover(ctx context.Context) ([]reconcile.Candidate, error) {
	rec, err := h.primaryAccount(ctx)
	if err != nil {
		return nil, err
	}
	transport, err := h.session().StorageTransport(ctx)
	if err != nil {
		return nil, err
	}
	discoverer, err := reconcile.NewDiscoverer(reconcile.DiscoveryDependencies{
		Telegram: transport.Channels, Identity: reconcile.Identity{UserID: rec.UserID}, Logger: h.account.Logger(),
	})
	if err != nil {
		return nil, err
	}
	all, err := discoverer.Discover(ctx)
	if err != nil {
		return nil, err
	}
	return reconcile.Eligible(all), nil
}

func (h *Host) candidate(ctx context.Context, channelID int64) (reconcile.Candidate, error) {
	cands, err := h.discover(ctx)
	if err != nil {
		return reconcile.Candidate{}, err
	}
	for _, c := range cands {
		if c.Peer.ChannelID == channelID {
			return c, nil
		}
	}
	return reconcile.Candidate{}, ErrProjectNotFound
}

func (h *Host) describe(c reconcile.Candidate, local projectDir, hasLocal bool) Project {
	p := Project{
		ID:          projectID(c.Peer.ChannelID),
		Name:        buckets.ProjectNameFromTitle(c.Title),
		ChannelID:   c.Peer.ChannelID,
		CreatedAt:   time.Unix(c.CreatedAt, 0),
		LiveBuckets: c.LiveBuckets,
	}
	if hasLocal && local.file.LastOpened > 0 {
		p.LastOpened = time.Unix(local.file.LastOpened, 0)
	}
	keyMatches := false
	if hasLocal {
		if fp, err := keyring.KeyFileFingerprint(local.keyPath()); err == nil && fp == c.KeyFingerprint {
			keyMatches = true
		}
	}
	switch {
	case c.MinReaderVersion > wire.FormatVersion:
		p.State = ProjectTooNew
	case keyMatches && c.HasEscrow():
		p.State = ProjectReady
	case keyMatches:
		p.State = ProjectUnfinished
	case c.HasEscrow():
		p.State = ProjectLocked
	default:
		p.State = ProjectLegacy
	}
	return p
}

func (h *Host) List(ctx context.Context) ([]Project, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cands, err := h.discover(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(cands))
	for _, c := range cands {
		local, hasLocal, err := h.localFor(c.Peer.ChannelID)
		if err != nil {
			return nil, err
		}
		out = append(out, h.describe(c, local, hasLocal))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastOpened.Equal(out[j].LastOpened) {
			return out[i].LastOpened.After(out[j].LastOpened)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (h *Host) Current() (Project, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.project == nil {
		return Project{}, false
	}
	m := h.project.meta
	return Project{
		ID: projectID(m.channelID()), Name: m.file.Name, ChannelID: m.channelID(), State: ProjectReady,
		LastOpened: time.Unix(m.file.LastOpened, 0),
	}, true
}

func validName(name string) (string, error) {
	if _, err := buckets.ProjectTitle(name); err != nil || utf8.RuneCountInString(name) > MaxProjectNameChars {
		return "", ErrProjectName
	}
	return buckets.ProjectTitle(name)
}

func zero(b []byte) {
	clear(b)
}

func (h *Host) Create(ctx context.Context, name string, password []byte) (Project, error) {
	defer zero(password)
	title, err := validName(name)
	if err != nil {
		return Project{}, err
	}
	if utf8.RuneCount(password) < MinPasswordChars {
		return Project{}, ErrPasswordTooShort
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rec, err := h.primaryAccount(ctx)
	if err != nil {
		return Project{}, err
	}
	dir, err := h.newLocal(buckets.ProjectNameFromTitle(title), 0)
	if err != nil {
		return Project{}, err
	}
	pw := append([]byte(nil), password...)
	defer zero(pw)
	if err := h.openLocked(ctx, dir, rec, core.RegistryOptions{Create: true, Title: title, Password: pw}); err != nil {
		if stateChannelID(dir.path) == 0 {
			_ = os.RemoveAll(dir.path)
		}
		return Project{}, err
	}
	p, _ := h.currentLocked()
	return p, nil
}

func (h *Host) Unlock(ctx context.Context, id ProjectID, password []byte) error {
	defer zero(password)
	channelID, err := parseProjectID(id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c, err := h.candidate(ctx, channelID)
	if err != nil {
		return err
	}
	if !c.HasEscrow() || c.MinReaderVersion > wire.FormatVersion {
		return ErrProjectNotReady
	}
	escrow := core.EscrowOf(c.Escrow)
	if err := keyring.VerifyEscrow(escrow, password, c.DeploymentID); err != nil {
		if errors.Is(err, keyring.ErrEscrowPassword) {
			return ErrWrongPassword
		}
		return err
	}
	local, found, err := h.localFor(channelID)
	if err != nil {
		return err
	}
	if found && local.hasState() {
		if err := os.RemoveAll(local.path); err != nil {
			return err
		}
		found = false
	}
	if !found {
		if local, err = h.newLocal(buckets.ProjectNameFromTitle(c.Title), channelID); err != nil {
			return err
		}
	}
	_ = os.Remove(local.keyPath())
	return keyring.OpenEscrowToFile(escrow, password, c.DeploymentID, local.keyPath())
}

func (h *Host) Open(ctx context.Context, id ProjectID) (Project, error) {
	channelID, err := parseProjectID(id)
	if err != nil {
		return Project{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.project != nil && h.project.meta.channelID() == channelID {
		return h.currentLocked()
	}
	rec, err := h.primaryAccount(ctx)
	if err != nil {
		return Project{}, err
	}
	c, err := h.candidate(ctx, channelID)
	if err != nil {
		return Project{}, err
	}
	local, found, err := h.localFor(channelID)
	if err != nil {
		return Project{}, err
	}
	if !found {
		return Project{}, ErrProjectNotReady
	}
	state := h.describe(c, local, true).State
	if state != ProjectReady && state != ProjectUnfinished {
		return Project{}, ErrProjectNotReady
	}
	local.file.Name = buckets.ProjectNameFromTitle(c.Title)
	opts := core.RegistryOptions{}
	if !local.hasState() {
		opts.RegistryChannelID = channelID
	}
	if err := h.openLocked(ctx, local, rec, opts); err != nil {
		return Project{}, err
	}
	return h.currentLocked()
}

func (h *Host) currentLocked() (Project, error) {
	if h.project == nil {
		return Project{}, ErrProjectNotReady
	}
	m := h.project.meta
	return Project{
		ID: projectID(m.channelID()), Name: m.file.Name, ChannelID: m.channelID(), State: ProjectReady,
		LastOpened: time.Unix(m.file.LastOpened, 0),
	}, nil
}

func (h *Host) openLocked(ctx context.Context, dir projectDir, rec core.AccountRecord, opts core.RegistryOptions) error {
	if err := h.closeProjectLocked(ctx); err != nil {
		return err
	}
	if err := h.pauseAccountServing(ctx); err != nil {
		return err
	}
	opened, err := h.startProject(ctx, dir, rec, opts)
	if err != nil {
		_ = h.resumeAccountServing(context.WithoutCancel(ctx))
		return err
	}
	opened.meta.file.ChannelID = opened.meta.channelID()
	opened.meta.file.LastOpened = time.Now().Unix()
	_ = opened.meta.save()
	h.project = opened
	return nil
}

func (h *Host) startProject(ctx context.Context, dir projectDir, rec core.AccountRecord, opts core.RegistryOptions) (*openProject, error) {
	app, err := h.newApp(h.projectConfig(dir.path), &core.EmbeddedOptions{SessionKeys: h.ring, Registry: opts})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*openProject, error) {
		_ = app.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	if err := app.SeedPrimaryAccount(ctx, rec); err != nil {
		return fail(err)
	}
	serving, err := app.StartServing(context.WithoutCancel(ctx))
	if err != nil {
		return fail(err)
	}
	if err := waitForRuntime(ctx, serving); err != nil {
		_ = serving.Close(context.WithoutCancel(ctx))
		return fail(err)
	}
	return &openProject{meta: dir, app: app, serving: serving}, nil
}

func waitForRuntime(ctx context.Context, serving *core.Serving) error {
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if serving.Runtime() != nil {
			return nil
		}
		report := serving.RegistryReport()
		if report.Status == core.RegistryRefused || report.Status == core.RegistryRediscovery {
			return fmt.Errorf("%w: %s", ErrProjectNotReady, report.Detail)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("embedded: project did not start: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (h *Host) CloseProject(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.closeProjectLocked(ctx); err != nil {
		return err
	}
	return h.resumeAccountServing(ctx)
}

func (h *Host) closeProjectLocked(ctx context.Context) error {
	if h.project == nil {
		return nil
	}
	p := h.project
	h.project = nil
	return errors.Join(p.serving.Close(ctx), p.app.Close(ctx))
}

func (h *Host) requireOpen(id ProjectID) error {
	if h.project == nil || projectID(h.project.meta.channelID()) != id {
		return ErrProjectNotReady
	}
	return nil
}

func (h *Host) Rename(ctx context.Context, id ProjectID, name string) error {
	title, err := validName(name)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.requireOpen(id); err != nil {
		return err
	}
	if err := h.project.serving.RenameProject(ctx, h.project.app, buckets.ProjectNameFromTitle(title)); err != nil {
		return err
	}
	h.project.meta.file.Name = buckets.ProjectNameFromTitle(title)
	return h.project.meta.save()
}

func (h *Host) ChangePassword(ctx context.Context, id ProjectID, oldPassword, newPassword []byte) error {
	defer zero(oldPassword)
	defer zero(newPassword)
	if utf8.RuneCount(newPassword) < MinPasswordChars {
		return ErrPasswordTooShort
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.requireOpen(id); err != nil {
		return err
	}
	err := h.project.serving.ChangeProjectPassword(ctx, h.project.app, oldPassword, newPassword)
	if errors.Is(err, keyring.ErrEscrowPassword) {
		return ErrWrongPassword
	}
	return err
}

func (h *Host) ResetKey(ctx context.Context, id ProjectID, newPassword []byte, confirmation string) (ResetReport, error) {
	defer zero(newPassword)
	if confirmation != ResetConfirmation {
		return ResetReport{}, ErrConfirmation
	}
	if utf8.RuneCount(newPassword) < MinPasswordChars {
		return ResetReport{}, ErrPasswordTooShort
	}
	return ResetReport{}, ErrResetUnavailable
}
