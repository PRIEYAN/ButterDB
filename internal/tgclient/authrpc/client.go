package authrpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/gotd/td/crypto/srp"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/scope"
)

var ErrSignUpRequired = errors.New("authrpc: phone number is not registered with telegram")

var errNoPasswordSet = errors.New("authrpc: account has no two-step password")

type Dependencies struct {
	Invoker scope.Invoker
	Random  io.Reader
	APIID   int
	APIHash string
}

type Client struct {
	invoker scope.Invoker
	random  io.Reader
	apiID   int
	apiHash string
}

var _ tgclient.AuthRPC = (*Client)(nil)

func New(deps Dependencies) (*Client, error) {
	if deps.Invoker == nil {
		return nil, fmt.Errorf("authrpc: %w: invoker", errs.ErrMissingDependency)
	}
	if deps.Random == nil {
		return nil, fmt.Errorf("authrpc: %w: random", errs.ErrMissingDependency)
	}
	if deps.APIID <= 0 {
		return nil, fmt.Errorf("authrpc: %w: api id", errs.ErrMissingDependency)
	}
	if deps.APIHash == "" {
		return nil, fmt.Errorf("authrpc: %w: api hash", errs.ErrMissingDependency)
	}
	return &Client{invoker: deps.Invoker, random: deps.Random, apiID: deps.APIID, apiHash: deps.APIHash}, nil
}

func (c *Client) call(ctx context.Context, operation string, fn func(ctx context.Context, api *tg.Client) error) error {
	return mapError(operation, c.invoker.Invoke(ctx, fn))
}

func (c *Client) SendCode(ctx context.Context, phone string) (tgclient.SentCode, error) {
	if phone == "" {
		return tgclient.SentCode{}, errors.New("authrpc: send code: empty phone")
	}
	var sent tgclient.SentCode
	err := c.call(ctx, "send code", func(ctx context.Context, api *tg.Client) error {
		result, err := api.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: c.apiID, APIHash: c.apiHash})
		if err != nil {
			return err
		}
		sent, err = sentCodeFromTG(result)
		return err
	})
	if err != nil {
		return tgclient.SentCode{}, err
	}
	return sent, nil
}

func sentCodeFromTG(result tg.AuthSentCodeClass) (tgclient.SentCode, error) {
	code, ok := result.(*tg.AuthSentCode)
	if !ok {
		return tgclient.SentCode{}, fmt.Errorf("unexpected sent code type %T", result)
	}
	out := tgclient.SentCode{PhoneCodeHash: code.PhoneCodeHash}
	if seconds, has := code.GetTimeout(); has {
		out.Timeout = time.Duration(seconds) * time.Second
	}
	return out, nil
}

func (c *Client) SignIn(ctx context.Context, phone, phoneCodeHash, code string) (tgclient.Account, error) {
	var account tgclient.Account
	err := c.call(ctx, "sign in", func(ctx context.Context, api *tg.Client) error {
		request := &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: phoneCodeHash}
		request.SetPhoneCode(code)
		result, err := api.AuthSignIn(ctx, request)
		if err != nil {
			return err
		}
		account, err = accountFromAuthorization(result)
		return err
	})
	if err != nil {
		return tgclient.Account{}, err
	}
	return account, nil
}

func (c *Client) CheckPassword(ctx context.Context, password string) (tgclient.Account, error) {
	var account tgclient.Account
	err := c.call(ctx, "check password", func(ctx context.Context, api *tg.Client) error {
		parameters, err := api.AccountGetPassword(ctx)
		if err != nil {
			return err
		}
		check, err := c.passwordProof(password, parameters)
		if err != nil {
			return err
		}
		result, err := api.AuthCheckPassword(ctx, check)
		if err != nil {
			return err
		}
		account, err = accountFromAuthorization(result)
		return err
	})
	if err != nil {
		return tgclient.Account{}, err
	}
	return account, nil
}

func (c *Client) passwordProof(password string, parameters *tg.AccountPassword) (*tg.InputCheckPasswordSRP, error) {
	algo, ok := parameters.CurrentAlgo.(*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow)
	if !ok {
		if parameters.CurrentAlgo == nil {
			return nil, errNoPasswordSet
		}
		return nil, fmt.Errorf("unsupported password algorithm %T", parameters.CurrentAlgo)
	}
	answer, err := srp.NewSRP(c.random).Hash([]byte(password), parameters.SRPB, parameters.SecureRandom, srp.Input(*algo))
	if err != nil {
		return nil, fmt.Errorf("compute password proof: %w", err)
	}
	return &tg.InputCheckPasswordSRP{SRPID: parameters.SRPID, A: answer.A, M1: answer.M1}, nil
}

func (c *Client) GetPasswordHint(ctx context.Context) (string, error) {
	var hint string
	err := c.call(ctx, "get password hint", func(ctx context.Context, api *tg.Client) error {
		parameters, err := api.AccountGetPassword(ctx)
		if err != nil {
			return err
		}
		hint = parameters.Hint
		return nil
	})
	if err != nil {
		return "", err
	}
	return hint, nil
}

func (c *Client) LogOut(ctx context.Context) error {
	return c.call(ctx, "log out", func(ctx context.Context, api *tg.Client) error {
		_, err := api.AuthLogOut(ctx)
		return err
	})
}

func accountFromAuthorization(result tg.AuthAuthorizationClass) (tgclient.Account, error) {
	switch value := result.(type) {
	case *tg.AuthAuthorization:
		user, ok := value.User.(*tg.User)
		if !ok {
			return tgclient.Account{}, fmt.Errorf("unexpected user type %T", value.User)
		}
		return tgclient.Account{UserID: user.ID, FirstName: user.FirstName, Username: user.Username}, nil
	case *tg.AuthAuthorizationSignUpRequired:
		return tgclient.Account{}, ErrSignUpRequired
	default:
		return tgclient.Account{}, fmt.Errorf("unexpected authorization type %T", result)
	}
}

func mapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var flood *tgclient.FloodWaitError
	if errors.As(err, &flood) {
		return fmt.Errorf("authrpc: %s: %w", operation, err)
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return fmt.Errorf("authrpc: %s: %w", operation, &tgclient.FloodWaitError{Duration: wait})
	}
	if sentinel := sentinelFor(err); sentinel != nil {
		return fmt.Errorf("authrpc: %s: %w", operation, sentinel)
	}
	if tgerr.Is(err, "PHONE_NUMBER_UNOCCUPIED") {
		return fmt.Errorf("authrpc: %s: %w", operation, ErrSignUpRequired)
	}
	return fmt.Errorf("authrpc: %s: %w", operation, err)
}

func sentinelFor(err error) error {
	switch {
	case tgerr.Is(err, "PHONE_CODE_INVALID", "PHONE_CODE_EMPTY"):
		return tgclient.ErrPhoneCodeInvalid
	case tgerr.Is(err, "PHONE_CODE_EXPIRED"):
		return tgclient.ErrPhoneCodeExpired
	case tgerr.Is(err, "SESSION_PASSWORD_NEEDED"):
		return tgclient.ErrPasswordNeeded
	case tgerr.Is(err, "PASSWORD_HASH_INVALID"):
		return tgclient.ErrPasswordInvalid
	case tgerr.Is(err, "AUTH_KEY_UNREGISTERED"):
		return tgclient.ErrNotAuthorized
	}
	return nil
}
