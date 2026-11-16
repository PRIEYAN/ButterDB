package vm

import (
	"context"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const UnlinkWord = "unlink"

type TelegramState struct {
	Loaded bool
	Status adminv1.TelegramStatus
}

func (t TelegramState) Linked() bool {
	return t.Loaded && t.Status.State == adminv1.TelegramLinked && t.Status.Account != nil
}

type UnlinkOutcome int

const (
	UnlinkFailed UnlinkOutcome = iota
	UnlinkNotConfirmed
	UnlinkDone
)

func (o UnlinkOutcome) Route() model.Route {
	if o == UnlinkDone {
		return model.RouteTelegram
	}
	return ""
}

func (v *ViewModel) RefreshTelegram(ctx context.Context) {
	v.begin()
	status, err := v.client.TelegramStatus(ctx)
	if err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return
	}
	v.finish(func(s *State) { s.Telegram = TelegramState{Loaded: true, Status: status} }, nil)
}

func (v *ViewModel) Unlink(ctx context.Context, typed string) UnlinkOutcome {
	if typed != UnlinkWord {
		return UnlinkNotConfirmed
	}
	v.begin()
	status, err := v.client.UnlinkTelegram(ctx)
	if err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return UnlinkFailed
	}
	v.finish(func(s *State) { s.Telegram = TelegramState{Loaded: true, Status: status} }, nil)
	return UnlinkDone
}
