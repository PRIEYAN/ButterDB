package authrpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type handlerInvoker func(ctx context.Context, input bin.Encoder, output bin.Decoder) error

func (f handlerInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return f(ctx, input, output)
}

type fakeScope struct{ raw handlerInvoker }

func (s fakeScope) Invoke(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error {
	return fn(ctx, tg.NewClient(s.raw))
}

func (s fakeScope) InvokeMedia(ctx context.Context, _ int, fn func(ctx context.Context, api *tg.Client) error) error {
	return fn(ctx, tg.NewClient(s.raw))
}

func reply(output bin.Decoder, response bin.Encoder) error {
	var buffer bin.Buffer
	if err := response.Encode(&buffer); err != nil {
		return err
	}
	return output.Decode(&buffer)
}

func newTestClient(t *testing.T, raw handlerInvoker) *Client {
	t.Helper()
	client, err := New(Dependencies{
		Invoker: fakeScope{raw: raw},
		Random:  bytes.NewReader(bytes.Repeat([]byte{3}, 512)),
		APIID:   12345,
		APIHash: "hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func failWith(code int, text string) handlerInvoker {
	return func(context.Context, bin.Encoder, bin.Decoder) error { return tgerr.New(code, text) }
}

func decodeHex(t *testing.T, text string) []byte {
	t.Helper()
	out, err := hex.DecodeString(strings.ReplaceAll(text, "\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func passwordParameters(t *testing.T, hint string) *tg.AccountPassword {
	t.Helper()
	prime := decodeHex(t, "C71CAEB9C6B1C9048E6C522F70F13F73980D40238E3E21C14934D037563D930F"+
		"48198A0AA7C14058229493D22530F4DBFA336F6E0AC925139543AED44CCE7C37"+
		"20FD51F69458705AC68CD4FE6B6B13ABDC9746512969328454F18FAF8C595F64"+
		"2477FE96BB2A941D5BCD1D4AC8CC49880708FA9B378E3C4F3A9060BEE67CF9A4"+
		"A4A695811051907E162753B56B0F6B410DBA74D8A84B2A14B3144E0EF1284754"+
		"FD17ED950D5965B4B9DD46582DB1178D169C6BC465B0D6FF9CA3928FEF5B9AE4"+
		"E418FC15E83EBEA0F87FA9FF5EED70050DED2849F47BF959D956850CE929851F"+
		"0D8115F635B105EE2E4E15D04B2454BF6F4FADF034B10403119CD8E3B92FCC5B")
	srpB := decodeHex(t, "9C52401A6A8084EC82F01C3725D3FB448BD2F0C909F9D97726EAC4B7A74172D9"+
		"52F02466BE6734FA274D2B7429E27397F10372D66B400B80A5C5AE3F28B17BF3"+
		"105D7A2D2A885998CDC2DEFC208AEC217AB58859A9ABC2374AD93DC285F4B3FB"+
		"CAFF4143D7888F2425BD2FB711B25609CEB21757D935B1EF2F042173AD0CE2FE"+
		"0E474DAC53914BD25A8A9AED4AEA8953D55CB88621DB37B871EA0D04393AC098"+
		"7F68094CCC9DE8239251375D8FFFD263316CD528C097B7BC9FB919FBEDB76C52"+
		"5DF3413C374EE076D97A1E6D352BB7CC80FD13651B04B32E2E48C5268150842C"+
		"FD07CF855958B1B5EA9C36FDAD697FE3AEC8DCC6B1EFEC36874AF226204676CF")
	parameters := &tg.AccountPassword{
		HasPassword: true,
		CurrentAlgo: &tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{
			Salt1: decodeHex(t, "4D11FB6BEC38F9D2546BB0F61E4F1C99A1BC0DB8F0D5F35B1291B37B213123D7ED48F3C6794D495B"),
			Salt2: decodeHex(t, "A1B181AAFE88188680AE32860D60BB01"),
			G:     3,
			P:     prime,
		},
		SRPB:          srpB,
		SRPID:         99,
		SecureRandom:  bytes.Repeat([]byte{1}, 256),
		NewAlgo:       &tg.PasswordKdfAlgoUnknown{},
		NewSecureAlgo: &tg.SecurePasswordKdfAlgoUnknown{},
	}
	if hint != "" {
		parameters.SetHint(hint)
	}
	return parameters
}

func authorization() *tg.AuthAuthorization {
	return &tg.AuthAuthorization{User: &tg.User{ID: 42, FirstName: "Ada", Username: "ada"}}
}

func TestSendCodeReturnsHashAndTimeout(t *testing.T) {
	var request *tg.AuthSendCodeRequest
	client := newTestClient(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request = input.(*tg.AuthSendCodeRequest)
		sent := &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{Length: 5}, PhoneCodeHash: "hash-1"}
		sent.SetTimeout(60)
		return reply(output, sent)
	})
	got, err := client.SendCode(t.Context(), "+15550100")
	if err != nil || got != (tgclient.SentCode{PhoneCodeHash: "hash-1", Timeout: 60 * time.Second}) {
		t.Fatalf("SendCode = %+v, %v", got, err)
	}
	if request.PhoneNumber != "+15550100" || request.APIID != 12345 || request.APIHash != "hash" {
		t.Fatalf("request = %+v", request)
	}
}

func TestSendCodeRejectsUnexpectedAnswer(t *testing.T) {
	client := newTestClient(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		return reply(output, &tg.AuthSentCodeSuccess{Authorization: authorization()})
	})
	if _, err := client.SendCode(t.Context(), "+15550100"); err == nil {
		t.Fatal("unexpected sent code type accepted")
	}
}

func TestSignInReturnsAccountAndSendsHash(t *testing.T) {
	var request *tg.AuthSignInRequest
	client := newTestClient(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request = input.(*tg.AuthSignInRequest)
		return reply(output, authorization())
	})
	account, err := client.SignIn(t.Context(), "+15550100", "hash-1", "12345")
	if err != nil || account != (tgclient.Account{UserID: 42, FirstName: "Ada", Username: "ada"}) {
		t.Fatalf("SignIn = %+v, %v", account, err)
	}
	code, _ := request.GetPhoneCode()
	if request.PhoneCodeHash != "hash-1" || code != "12345" || request.PhoneNumber != "+15550100" {
		t.Fatalf("request = %+v", request)
	}
}

func TestAuthErrorsTyped(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(c *Client) error
		code int
		text string
		want error
	}{
		{"code invalid", func(c *Client) error { _, err := c.SignIn(t.Context(), "p", "h", "c"); return err }, 400, "PHONE_CODE_INVALID", tgclient.ErrPhoneCodeInvalid},
		{"code empty is invalid", func(c *Client) error { _, err := c.SignIn(t.Context(), "p", "h", "c"); return err }, 400, "PHONE_CODE_EMPTY", tgclient.ErrPhoneCodeInvalid},
		{"code expired", func(c *Client) error { _, err := c.SignIn(t.Context(), "p", "h", "c"); return err }, 400, "PHONE_CODE_EXPIRED", tgclient.ErrPhoneCodeExpired},
		{"password needed", func(c *Client) error { _, err := c.SignIn(t.Context(), "p", "h", "c"); return err }, 401, "SESSION_PASSWORD_NEEDED", tgclient.ErrPasswordNeeded},
		{"unregistered number", func(c *Client) error { _, err := c.SignIn(t.Context(), "p", "h", "c"); return err }, 400, "PHONE_NUMBER_UNOCCUPIED", ErrSignUpRequired},
		{"send code unauthorized", func(c *Client) error { _, err := c.SendCode(t.Context(), "p"); return err }, 401, "AUTH_KEY_UNREGISTERED", tgclient.ErrNotAuthorized},
		{"log out unauthorized", func(c *Client) error { return c.LogOut(t.Context()) }, 401, "AUTH_KEY_UNREGISTERED", tgclient.ErrNotAuthorized},
		{"hint unauthorized", func(c *Client) error { _, err := c.GetPasswordHint(t.Context()); return err }, 401, "AUTH_KEY_UNREGISTERED", tgclient.ErrNotAuthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(newTestClient(t, failWith(tc.code, tc.text))); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAuthFloodWaitIsTyped(t *testing.T) {
	client := newTestClient(t, failWith(420, "FLOOD_WAIT_17"))
	_, err := client.SendCode(t.Context(), "+15550100")
	var flood *tgclient.FloodWaitError
	if !errors.As(err, &flood) || flood.Duration != 17*time.Second {
		t.Fatalf("err = %v, want 17s FloodWaitError", err)
	}
}

func TestCheckPasswordInvalidMapsToSentinel(t *testing.T) {
	client := newTestClient(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		switch input.(type) {
		case *tg.AccountGetPasswordRequest:
			return reply(output, passwordParameters(t, "pet"))
		case *tg.AuthCheckPasswordRequest:
			return tgerr.New(400, "PASSWORD_HASH_INVALID")
		}
		return errors.New("unexpected rpc")
	})
	if _, err := client.CheckPassword(t.Context(), "wrong"); !errors.Is(err, tgclient.ErrPasswordInvalid) {
		t.Fatalf("err = %v, want ErrPasswordInvalid", err)
	}
}

func TestCheckPasswordSendsProofAndReturnsAccount(t *testing.T) {
	var check *tg.AuthCheckPasswordRequest
	client := newTestClient(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		switch request := input.(type) {
		case *tg.AccountGetPasswordRequest:
			return reply(output, passwordParameters(t, ""))
		case *tg.AuthCheckPasswordRequest:
			check = request
			return reply(output, authorization())
		}
		return errors.New("unexpected rpc")
	})
	account, err := client.CheckPassword(t.Context(), "123123")
	if err != nil || account.UserID != 42 {
		t.Fatalf("CheckPassword = %+v, %v", account, err)
	}
	proof, ok := check.Password.(*tg.InputCheckPasswordSRP)
	if !ok || proof.SRPID != 99 || len(proof.A) == 0 || len(proof.M1) == 0 {
		t.Fatalf("proof = %#v", check.Password)
	}
}

func TestCheckPasswordWithoutPasswordSetFails(t *testing.T) {
	client := newTestClient(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		return reply(output, &tg.AccountPassword{NewAlgo: &tg.PasswordKdfAlgoUnknown{}, NewSecureAlgo: &tg.SecurePasswordKdfAlgoUnknown{}})
	})
	if _, err := client.CheckPassword(t.Context(), "x"); !errors.Is(err, errNoPasswordSet) {
		t.Fatalf("err = %v, want errNoPasswordSet", err)
	}
}

func TestGetPasswordHint(t *testing.T) {
	client := newTestClient(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		return reply(output, passwordParameters(t, "first pet"))
	})
	hint, err := client.GetPasswordHint(t.Context())
	if err != nil || hint != "first pet" {
		t.Fatalf("hint = %q, %v", hint, err)
	}
}

func TestSignUpRequiredAuthorization(t *testing.T) {
	client := newTestClient(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		return reply(output, &tg.AuthAuthorizationSignUpRequired{})
	})
	if _, err := client.SignIn(t.Context(), "p", "h", "c"); !errors.Is(err, ErrSignUpRequired) {
		t.Fatalf("err = %v, want ErrSignUpRequired", err)
	}
}

func TestLogOutCallsAuthLogOut(t *testing.T) {
	called := false
	client := newTestClient(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		_, called = input.(*tg.AuthLogOutRequest)
		return reply(output, &tg.AuthLoggedOut{})
	})
	if err := client.LogOut(t.Context()); err != nil || !called {
		t.Fatalf("LogOut err %v called %v", err, called)
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	random := bytes.NewReader(nil)
	for name, deps := range map[string]Dependencies{
		"invoker":  {Random: random, APIID: 1, APIHash: "h"},
		"random":   {Invoker: fakeScope{}, APIID: 1, APIHash: "h"},
		"api id":   {Invoker: fakeScope{}, Random: random, APIHash: "h"},
		"api hash": {Invoker: fakeScope{}, Random: random, APIID: 1},
	} {
		if _, err := New(deps); err == nil {
			t.Errorf("%s: New accepted missing dependency", name)
		}
	}
}
