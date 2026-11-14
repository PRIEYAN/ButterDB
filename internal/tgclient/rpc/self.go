package rpc

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func (c *Client) Self(ctx context.Context) (tgclient.Account, error) {
	var account tgclient.Account
	err := c.read(ctx, "self", func(ctx context.Context, api *tg.Client) error {
		users, err := api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		if err != nil {
			return err
		}
		for _, raw := range users {
			if user, ok := raw.(*tg.User); ok && user.Self {
				account = tgclient.Account{UserID: user.ID, FirstName: user.FirstName, Username: user.Username}
				return nil
			}
		}
		return fmt.Errorf("self user not found")
	})
	if err != nil {
		return tgclient.Account{}, err
	}
	return account, nil
}
