package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/identity"
)

const maxSetupSteps = 32

var ErrSetupAborted = errors.New("core: setup aborted")

type Terminal interface {
	Prompt(label string) (string, error)
	PromptSecret(label string) (string, error)
	Say(text string)
}

type LoginFlow interface {
	Status(ctx context.Context) (identity.Snapshot, error)
	SaveCredentials(ctx context.Context, apiID int, apiHash string) error
	Start(ctx context.Context, phone string) (identity.Snapshot, error)
	SubmitCode(ctx context.Context, loginID, code string) (identity.Snapshot, error)
	SubmitPassword(ctx context.Context, loginID, password string) (identity.Snapshot, error)
}

type SetupDependencies struct {
	Flow     LoginFlow
	Terminal Terminal
}

type Setup struct {
	flow     LoginFlow
	terminal Terminal
}

func NewSetup(deps SetupDependencies) (*Setup, error) {
	if deps.Flow == nil {
		return nil, fmt.Errorf("core: setup: %w: login flow", errs.ErrMissingDependency)
	}
	if deps.Terminal == nil {
		return nil, fmt.Errorf("core: setup: %w: terminal", errs.ErrMissingDependency)
	}
	return &Setup{flow: deps.Flow, terminal: deps.Terminal}, nil
}

func (s *Setup) Run(ctx context.Context) (identity.Snapshot, error) {
	snap, err := s.flow.Status(ctx)
	if err != nil {
		return snap, fmt.Errorf("core: setup: %w", err)
	}
	for step := 0; step < maxSetupSteps; step++ {
		if snap.State == identity.StateLinked {
			s.announceLinked(snap)
			return snap, nil
		}
		snap, err = s.advance(ctx, snap)
		if err != nil {
			return snap, err
		}
	}
	return snap, fmt.Errorf("core: setup: %w: too many steps", ErrSetupAborted)
}

func (s *Setup) advance(ctx context.Context, snap identity.Snapshot) (identity.Snapshot, error) {
	switch snap.Next() {
	case identity.NextApp:
		return s.collectCredentials(ctx)
	case identity.NextPhone:
		return s.collectPhone(ctx)
	case identity.NextCode:
		return s.collectCode(ctx, snap)
	case identity.NextPassword:
		return s.collectPassword(ctx, snap)
	}
	return snap, fmt.Errorf("core: setup: %w: state %q cannot continue", ErrSetupAborted, snap.State)
}

func (s *Setup) collectCredentials(ctx context.Context) (identity.Snapshot, error) {
	s.terminal.Say("Create an api_id and api_hash at https://my.telegram.org/apps, then enter them here.")
	rawID, err := s.terminal.Prompt("api_id")
	if err != nil {
		return identity.Snapshot{}, fmt.Errorf("core: setup: %w", err)
	}
	apiID, err := strconv.Atoi(strings.TrimSpace(rawID))
	if err != nil {
		s.terminal.Say("api_id must be a number.")
		return s.flow.Status(ctx)
	}
	apiHash, err := s.terminal.PromptSecret("api_hash")
	if err != nil {
		return identity.Snapshot{}, fmt.Errorf("core: setup: %w", err)
	}
	if err := s.flow.SaveCredentials(ctx, apiID, apiHash); err != nil {
		if errors.Is(err, identity.ErrCredentialsInvalid) {
			s.terminal.Say("api_id or api_hash is not valid.")
			return s.flow.Status(ctx)
		}
		return identity.Snapshot{}, fmt.Errorf("core: setup: %w", err)
	}
	return s.flow.Status(ctx)
}

func (s *Setup) collectPhone(ctx context.Context) (identity.Snapshot, error) {
	phone, err := s.terminal.Prompt("phone number with country code")
	if err != nil {
		return identity.Snapshot{}, fmt.Errorf("core: setup: %w", err)
	}
	snap, err := s.flow.Start(ctx, phone)
	return s.settle(snap, err)
}

func (s *Setup) collectCode(ctx context.Context, snap identity.Snapshot) (identity.Snapshot, error) {
	if snap.Login == nil {
		return snap, fmt.Errorf("core: setup: %w: no login in progress", ErrSetupAborted)
	}
	s.terminal.Say("A login code was sent to " + snap.Login.PhoneMasked + " through Telegram.")
	code, err := s.terminal.Prompt("login code")
	if err != nil {
		return snap, fmt.Errorf("core: setup: %w", err)
	}
	next, err := s.flow.SubmitCode(ctx, snap.Login.LoginID, code)
	return s.settle(next, err)
}

func (s *Setup) collectPassword(ctx context.Context, snap identity.Snapshot) (identity.Snapshot, error) {
	if snap.Login == nil {
		return snap, fmt.Errorf("core: setup: %w: no login in progress", ErrSetupAborted)
	}
	if hint := snap.Login.PasswordHint; hint != "" {
		s.terminal.Say("Two-step password hint: " + hint)
	}
	password, err := s.terminal.PromptSecret("two-step password")
	if err != nil {
		return snap, fmt.Errorf("core: setup: %w", err)
	}
	next, err := s.flow.SubmitPassword(ctx, snap.Login.LoginID, password)
	return s.settle(next, err)
}

func (s *Setup) settle(snap identity.Snapshot, err error) (identity.Snapshot, error) {
	if err == nil {
		return snap, nil
	}
	if errors.Is(err, identity.ErrPhoneInvalid) {
		s.terminal.Say("That phone number is not valid.")
		return snap, nil
	}
	var step *identity.StepError
	retryable := errors.Is(err, identity.ErrCodeInvalid) || errors.Is(err, identity.ErrPasswordInvalid)
	if retryable && errors.As(err, &step) {
		s.terminal.Say(fmt.Sprintf("That was not accepted. %d attempt(s) left.", step.AttemptsLeft))
		return snap, nil
	}
	return snap, fmt.Errorf("core: setup: %w", err)
}

func (s *Setup) announceLinked(snap identity.Snapshot) {
	if snap.Account == nil {
		return
	}
	s.terminal.Say(fmt.Sprintf("Linked to Telegram user %d (%s).", snap.Account.UserID, snap.Account.PhoneMasked))
}
