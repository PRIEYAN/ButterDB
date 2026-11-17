package embedded

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/processlock"
)

const (
	accountDirName  = "account"
	projectsDirName = "projects"
	accountKeyName  = "account.key"
	openTimeout     = 10 * time.Minute
	pollInterval    = 200 * time.Millisecond
)

type Host struct {
	root    string
	logs    io.Writer
	version string

	mu      sync.Mutex
	account *core.App
	serving *core.Serving
	ring    *keyring.Ring
	project *openProject
}

type openProject struct {
	meta    projectDir
	app     *core.App
	serving *core.Serving
}

func DefaultDataHome() (string, error) {
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, "butterdb"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("embedded: find home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "butterdb"), nil
}

func Open(opts Options) (*Host, error) {
	root := opts.DataHome
	if root == "" {
		var err error
		if root, err = DefaultDataHome(); err != nil {
			return nil, err
		}
	}
	logs := opts.LogOutput
	if logs == nil {
		logs = io.Discard
	}
	h := &Host{root: root, logs: logs, version: opts.Version}
	if err := h.startAccount(context.Background()); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Host) accountDir() string {
	return filepath.Join(h.root, accountDirName)
}

func (h *Host) projectsDir() string {
	return filepath.Join(h.root, projectsDirName)
}

func (h *Host) accountConfig() config.Config {
	dir := h.accountDir()
	cfg := config.DefaultIn(dir)
	cfg.ServerKeyFile = filepath.Join(dir, accountKeyName)
	embeddedDefaults(&cfg)
	return cfg
}

func (h *Host) projectConfig(dir string) config.Config {
	cfg := config.DefaultIn(dir)
	account := h.accountConfig()
	cfg.ServerKeyFile = filepath.Join(dir, serverKeyName)
	cfg.Telegram.AppFile = account.Telegram.AppFile
	cfg.Telegram.SessionFile = account.Telegram.SessionFile
	cfg.Telegram.ReplicaSessionFile = account.Telegram.ReplicaSessionFile
	embeddedDefaults(&cfg)
	return cfg
}

func embeddedDefaults(cfg *config.Config) {
	cfg.S3.Listen = ""
	cfg.S3.PublicURL = ""
	cfg.Admin.Listen = "127.0.0.1:1"
	cfg.Admin.TLSMode = "proxy"
	cfg.Cache.BlocksMaxBytes = 0
	cfg.Cache.WarmOnPutMaxBytes = 0
	cfg.Telegram.DeviceModel = "butterDB desktop"
	cfg.Log.Format = "text"
	cfg.Log.AccessLog = false
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("embedded: create %s: %w", path, err)
	}
	return os.Chmod(path, 0o700)
}

func (h *Host) newApp(cfg config.Config, embed *core.EmbeddedOptions) (*core.App, error) {
	app, err := core.New(core.Dependencies{
		Config: cfg, Role: "app", LogOutput: h.logs, Clock: clock.System{}, FS: fsx.OS{},
		Build: core.BuildInfo{Version: h.version, Commit: "embedded"}, Embedded: embed,
	})
	if err != nil {
		return nil, err
	}
	if err := app.Start(context.Background()); err != nil {
		_ = app.Close(context.Background())
		if errors.Is(err, processlock.ErrAlreadyRunning) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return app, nil
}

func (h *Host) startAccount(ctx context.Context) error {
	for _, dir := range []string{h.root, h.accountDir(), filepath.Join(h.accountDir(), "secrets"), h.projectsDir()} {
		if err := ensureDir(dir); err != nil {
			return err
		}
	}
	app, err := h.newApp(h.accountConfig(), &core.EmbeddedOptions{NoRegistry: true})
	if err != nil {
		return err
	}
	serving, err := app.StartServing(context.WithoutCancel(ctx))
	if err != nil {
		_ = app.Close(ctx)
		return err
	}
	ring, err := keyring.Load(filepath.Join(h.accountDir(), accountKeyName))
	if err != nil {
		_ = serving.Close(ctx)
		_ = app.Close(ctx)
		return err
	}
	h.account, h.serving, h.ring = app, serving, ring
	return nil
}

func (h *Host) resumeAccountServing(ctx context.Context) error {
	if h.serving != nil || h.account == nil {
		return nil
	}
	serving, err := h.account.StartServing(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	h.serving = serving
	return nil
}

func (h *Host) pauseAccountServing(ctx context.Context) error {
	if h.serving == nil {
		return nil
	}
	err := h.serving.Close(ctx)
	h.serving = nil
	return err
}

func (h *Host) Close(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closeAll(ctx)
}

func (h *Host) closeAll(ctx context.Context) error {
	projectErr := h.closeProjectLocked(ctx)
	accountErr := h.pauseAccountServing(ctx)
	var appErr error
	if h.account != nil {
		appErr = h.account.Close(ctx)
		h.account = nil
	}
	if h.ring != nil {
		h.ring.Close()
		h.ring = nil
	}
	return errors.Join(projectErr, accountErr, appErr)
}

func (h *Host) RoundTrip(req *http.Request) (*http.Response, error) {
	h.mu.Lock()
	var handler http.Handler
	switch {
	case h.project != nil:
		handler = h.project.serving.AdminHandler()
	case h.serving != nil:
		handler = h.serving.AdminHandler()
	}
	h.mu.Unlock()
	return serveInProcess(handler, req)
}

func (h *Host) session() *core.TelegramSession {
	if h.project != nil {
		return h.project.serving.Session()
	}
	if h.serving != nil {
		return h.serving.Session()
	}
	return nil
}

func (h *Host) primaryAccount(ctx context.Context) (core.AccountRecord, error) {
	sess := h.session()
	if sess == nil {
		return core.AccountRecord{}, ErrNotLoggedIn
	}
	rec, found, err := sess.PrimaryAccount(ctx)
	if err != nil {
		return core.AccountRecord{}, err
	}
	if !found || !rec.Linked {
		return core.AccountRecord{}, ErrNotLoggedIn
	}
	return rec, nil
}

func (h *Host) Logout(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.closeProjectLocked(ctx); err != nil {
		return err
	}
	if err := h.resumeAccountServing(ctx); err != nil {
		return err
	}
	_, err := h.serving.Session().Identity().Unlink(ctx)
	return err
}

func (h *Host) RemoveDevice(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.closeAll(ctx); err != nil {
		return err
	}
	if err := os.RemoveAll(h.root); err != nil {
		return fmt.Errorf("embedded: remove %s: %w", h.root, err)
	}
	return h.startAccount(ctx)
}
