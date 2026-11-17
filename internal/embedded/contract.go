package embedded

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

const BaseURL = "http://127.0.0.1"

type ProjectID string

type ProjectState string

const (
	ProjectReady      ProjectState = "ready"
	ProjectLocked     ProjectState = "locked"
	ProjectLegacy     ProjectState = "legacy"
	ProjectTooNew     ProjectState = "too_new"
	ProjectUnfinished ProjectState = "unfinished"
)

type Project struct {
	ID          ProjectID
	Name        string
	ChannelID   int64
	State       ProjectState
	CreatedAt   time.Time
	LiveBuckets int
	LastOpened  time.Time
}

type ResetReport struct {
	UnreadableObjects map[string]int64
	RevokedAccessKeys int
}

type Projects interface {
	List(ctx context.Context) ([]Project, error)
	Create(ctx context.Context, name string, password []byte) (Project, error)
	Unlock(ctx context.Context, id ProjectID, password []byte) error
	Open(ctx context.Context, id ProjectID) (Project, error)
	Current() (Project, bool)
	CloseProject(ctx context.Context) error
	Rename(ctx context.Context, id ProjectID, name string) error
	ChangePassword(ctx context.Context, id ProjectID, oldPassword, newPassword []byte) error
	ResetKey(ctx context.Context, id ProjectID, newPassword []byte, confirmation string) (ResetReport, error)
	Logout(ctx context.Context) error
	RemoveDevice(ctx context.Context) error
}

type Options struct {
	DataHome  string
	LogOutput io.Writer
	Version   string
}

const (
	MinPasswordChars    = 10
	ResetConfirmation   = "reset project key"
	MaxProjectNameChars = 64
)

var (
	ErrAlreadyRunning   = errors.New("butterDB is already open")
	ErrWrongPassword    = errors.New("wrong project password")
	ErrPasswordTooShort = errors.New("project password too short")
	ErrProjectNotFound  = errors.New("project not found")
	ErrProjectNotReady  = errors.New("project cannot be opened")
	ErrProjectName      = errors.New("project name is invalid")
	ErrConfirmation     = errors.New("confirmation text does not match")
	ErrNotLoggedIn      = errors.New("not logged in to telegram")
	ErrResetUnavailable = errors.New("resetting a lost project key is not available yet")
)

var _ Projects = (*Host)(nil)
var _ http.RoundTripper = (*Host)(nil)
