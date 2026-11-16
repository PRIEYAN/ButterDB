package vm

import (
	"context"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type reaction struct {
	resync   bool
	fallback Step
	override Step
	focus    Field
	flood    bool
	attempts bool
}

func reactionFor(code adminv1.Code) reaction {
	switch code {
	case adminv1.CodeTelegramCodeInvalid, adminv1.CodeTelegramPasswordInvalid:
		return reaction{focus: focusFor(code), attempts: true}
	case adminv1.CodeTelegramCodeExpired, adminv1.CodeTelegramAttemptsExhausted,
		adminv1.CodeTelegramLoginFailed, adminv1.CodeTelegramAccountMismatch:
		return reaction{resync: true, fallback: StepPhone, focus: FieldPhone}
	case adminv1.CodeTelegramAPIIDInvalid:
		return reaction{resync: true, override: StepApp, focus: FieldAPIID}
	case adminv1.CodeTelegramLoginInProgress, adminv1.CodeTelegramLoginNotActive:
		return reaction{resync: true}
	case adminv1.CodeTelegramFloodWait:
		return reaction{flood: true}
	}
	return reaction{}
}

func focusFor(code adminv1.Code) Field {
	if code == adminv1.CodeTelegramPasswordInvalid {
		return FieldPassword
	}
	return FieldCode
}

func (v *ViewModel) fail(ctx context.Context, err error) {
	p := model.Describe(err, v.now())
	if p.Presentation == "" {
		v.update(func(s *State) { s.Loading = false })
		return
	}
	r := reactionFor(p.Code)
	var fresh *adminv1.TelegramStatus
	if r.resync {
		if st, statusErr := v.flow.Status(ctx); statusErr == nil {
			fresh = &st
		}
	}
	v.update(func(s *State) {
		s.Loading = false
		v.applyReaction(s, r, p, fresh)
	})
}

func (v *ViewModel) applyReaction(s *State, r reaction, p model.Problem, fresh *adminv1.TelegramStatus) {
	switch {
	case fresh != nil:
		v.project(s, *fresh)
	case r.fallback != "":
		v.moveTo(s, r.fallback)
	}
	if r.override != "" {
		v.override = r.override
		v.overrideFrom = v.lastNext
		s.Step = r.override
	}
	if r.attempts {
		v.recordAttemptsLeft(s, p)
	}
	if r.flood && p.RetryAt.After(v.now()) {
		s.SubmitDisabled = true
		s.FloodUntil = p.RetryAt
	}
	v.raise(s, p)
	v.focus(s, r.focus)
}

func (v *ViewModel) recordAttemptsLeft(s *State, p model.Problem) {
	left, ok := attemptsLeftFrom(p.Details)
	if !ok || s.Login == nil {
		return
	}
	login := *s.Login
	if p.Code == adminv1.CodeTelegramPasswordInvalid {
		login.PasswordAttemptsLeft = left
	} else {
		login.CodeAttemptsLeft = left
	}
	s.Login = &login
}

func attemptsLeftFrom(details map[string]any) (int, bool) {
	switch n := details["attempts_left"].(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}
