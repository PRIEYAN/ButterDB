package faketg

import (
	"context"
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func (c *Client) SendCode(ctx context.Context, phone string) (tgclient.SentCode, error) {
	if _, err := c.enter(ctx, MethodSendCode, 0); err != nil {
		return tgclient.SentCode{}, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if c.login == nil {
		return tgclient.SentCode{}, ErrNoLoginScript
	}
	if phone != c.login.Phone {
		return tgclient.SentCode{}, ErrPhoneNumberInvalid
	}
	c.codeSeq++
	c.codeHash = fmt.Sprintf("faketg-code-%d-%d", c.userID, c.codeSeq)
	c.codeIssuedAt = c.n.clock.Now()
	c.pendingPassword = false
	timeout := c.login.CodeTimeout
	if timeout <= 0 {
		timeout = defaultCodeTimeout
	}
	return tgclient.SentCode{PhoneCodeHash: c.codeHash, Timeout: timeout}, nil
}

func (c *Client) codeLifetime() time.Duration {
	if c.login.CodeLifetime > 0 {
		return c.login.CodeLifetime
	}
	return defaultCodeLife
}

func (c *Client) SignIn(ctx context.Context, phone, phoneCodeHash, code string) (tgclient.Account, error) {
	if _, err := c.enter(ctx, MethodSignIn, 0); err != nil {
		return tgclient.Account{}, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if c.login == nil {
		return tgclient.Account{}, ErrNoLoginScript
	}
	if phone != c.login.Phone {
		return tgclient.Account{}, ErrPhoneNumberInvalid
	}
	if c.codeHash == "" || phoneCodeHash != c.codeHash || c.n.clock.Now().Sub(c.codeIssuedAt) > c.codeLifetime() {
		return tgclient.Account{}, fmt.Errorf("faketg: %w", tgclient.ErrPhoneCodeExpired)
	}
	if code != c.login.Code {
		return tgclient.Account{}, fmt.Errorf("faketg: %w", tgclient.ErrPhoneCodeInvalid)
	}
	c.codeHash = ""
	if c.login.Password != "" {
		c.pendingPassword = true
		return tgclient.Account{}, fmt.Errorf("faketg: %w", tgclient.ErrPasswordNeeded)
	}
	c.authorized = true
	return c.profile, nil
}

func (c *Client) CheckPassword(ctx context.Context, password string) (tgclient.Account, error) {
	if _, err := c.enter(ctx, MethodCheckPassword, 0); err != nil {
		return tgclient.Account{}, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if c.login == nil || !c.pendingPassword {
		return tgclient.Account{}, fmt.Errorf("faketg: %w", tgclient.ErrNotAuthorized)
	}
	if password != c.login.Password {
		return tgclient.Account{}, fmt.Errorf("faketg: %w", tgclient.ErrPasswordInvalid)
	}
	c.pendingPassword = false
	c.authorized = true
	return c.profile, nil
}

func (c *Client) GetPasswordHint(ctx context.Context) (string, error) {
	if _, err := c.enter(ctx, MethodGetPasswordHint, 0); err != nil {
		return "", err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if c.login == nil || (!c.pendingPassword && !c.authorized) {
		return "", fmt.Errorf("faketg: %w", tgclient.ErrNotAuthorized)
	}
	return c.login.PasswordHint, nil
}

func (c *Client) LogOut(ctx context.Context) error {
	if _, err := c.enter(ctx, MethodLogOut, 0); err != nil {
		return err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if err := c.requireAuthLocked(); err != nil {
		return err
	}
	c.authorized = false
	c.pendingPassword = false
	return nil
}
