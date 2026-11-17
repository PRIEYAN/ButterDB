package vm

import (
	"context"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type TelegramState struct {
	Loaded bool
	Status adminv1.TelegramStatus
}

func (t TelegramState) Linked() bool {
	return t.Loaded && t.Status.State == adminv1.TelegramLinked && t.Status.Account != nil
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
