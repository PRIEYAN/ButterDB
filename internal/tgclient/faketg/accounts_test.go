package faketg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func TestFakeTwoAccounts(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	bob, err := e.net.AddAccount(AccountConfig{UserID: 22, FirstName: "Bob", Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("accounts see their own identity", func(t *testing.T) {
		a, _ := e.alice.Self(ctx)
		b, _ := bob.Self(ctx)
		if a.UserID != 11 || b.UserID != 22 || b.Username != "bob" {
			t.Fatalf("self = %+v, %+v", a, b)
		}
	})

	t.Run("a second account cannot reach an unshared channel", func(t *testing.T) {
		if _, err := bob.ResolveChannel(ctx, e.peer.ChannelID); !errors.Is(err, tgclient.ErrChannelUnavailable) {
			t.Fatalf("error = %v", err)
		}
		if _, err := bob.GetHistory(ctx, e.peer, 0, 10); !errors.Is(err, tgclient.ErrChannelUnavailable) {
			t.Fatalf("error = %v", err)
		}
		if owned, _ := bob.ListOwnedChannels(ctx); len(owned) != 0 {
			t.Fatalf("bob owns %+v", owned)
		}
	})

	t.Run("access hashes differ per account", func(t *testing.T) {
		e.sendControl(t, e.alice, e.peer, "shared", 1)
		if err := e.net.Grant(e.peer.ChannelID, bob, false); err != nil {
			t.Fatal(err)
		}
		bobPeer, err := bob.ResolveChannel(ctx, e.peer.ChannelID)
		if err != nil {
			t.Fatal(err)
		}
		if bobPeer.AccessHash == e.peer.AccessHash || bobPeer.AccessHash == 0 {
			t.Fatalf("bob hash %d, alice hash %d", bobPeer.AccessHash, e.peer.AccessHash)
		}
		if _, err := bob.GetHistory(ctx, e.peer, 0, 10); !errors.Is(err, tgclient.ErrChannelUnavailable) {
			t.Fatalf("alice's hash worked for bob: %v", err)
		}
		hist, err := bob.GetHistory(ctx, bobPeer, 0, 10)
		if err != nil || len(hist) != 1 || hist[0].Text != "shared" {
			t.Fatalf("bob history = %+v, %v", hist, err)
		}
	})

	t.Run("a read only member cannot write or delete", func(t *testing.T) {
		bobPeer, _ := bob.PeerOf(e.peer.ChannelID)
		_, err := bob.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: bobPeer, Text: "x", RandomID: 1})
		if !errors.Is(err, ErrWriteForbidden) {
			t.Fatalf("send error = %v", err)
		}
		if err := bob.DeleteMessages(ctx, bobPeer, []int64{1}); !errors.Is(err, ErrWriteForbidden) {
			t.Fatalf("delete error = %v", err)
		}
	})

	t.Run("random id dedup is per account", func(t *testing.T) {
		if err := e.net.Grant(e.peer.ChannelID, bob, true); err != nil {
			t.Fatal(err)
		}
		bobPeer, _ := bob.PeerOf(e.peer.ChannelID)
		a := e.sendControl(t, e.alice, e.peer, "from alice", 777)
		b := e.sendControl(t, bob, bobPeer, "from bob", 777)
		if a == b {
			t.Fatalf("two accounts with one random id collided on message %d", a)
		}
	})

	t.Run("faults and saved messages are per account", func(t *testing.T) {
		if err := e.alice.QueueFaults(MethodSelf, Fault{Kind: FaultTransportDrop}); err != nil {
			t.Fatal(err)
		}
		if _, err := bob.Self(ctx); err != nil {
			t.Fatalf("bob inherited alice's fault: %v", err)
		}
		if _, err := e.alice.Self(ctx); !errors.Is(err, ErrInjectedTransport) {
			t.Fatalf("alice error = %v", err)
		}
		a1, _ := e.alice.SendToSavedMessages(ctx, "link one")
		a2, _ := e.alice.SendToSavedMessages(ctx, "link two")
		b1, _ := bob.SendToSavedMessages(ctx, "bob link")
		if a1 != 1 || a2 != 2 || b1 != 1 {
			t.Fatalf("saved ids = %d %d %d, want own sequence per account", a1, a2, b1)
		}
		if got := bob.SavedMessages(); len(got) != 1 || got[0].Text != "bob link" {
			t.Fatalf("bob saved = %+v", got)
		}
	})

	t.Run("duplicate and invalid accounts are refused", func(t *testing.T) {
		if _, err := e.net.AddAccount(AccountConfig{UserID: 11}); !errors.Is(err, ErrDuplicateAccount) {
			t.Fatalf("error = %v", err)
		}
		if _, err := e.net.AddAccount(AccountConfig{UserID: 0}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestFakeOwnedChannelsAndResolve(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	second, _ := e.alice.CreateChannel(ctx, "bucket-b", "about b")
	owned, err := e.alice.ListOwnedChannels(ctx)
	if err != nil || len(owned) != 2 || owned[0].Peer != e.peer || owned[1].Peer != second || owned[1].Title != "bucket-b" {
		t.Fatalf("owned = %+v, %v", owned, err)
	}
	resolved, err := e.alice.ResolveChannel(ctx, second.ChannelID)
	if err != nil || resolved != second {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
	if _, err := e.alice.ResolveChannel(ctx, 424242); !errors.Is(err, tgclient.ErrChannelUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestFakeLoginFlow(t *testing.T) {
	ctx := context.Background()
	newLogin := func(t *testing.T, script LoginScript) (*clock.Fake, *Client) {
		t.Helper()
		clk := clock.NewFake(time.Unix(1763000000, 0).UTC())
		n, _ := NewNetwork(clk)
		c, err := n.AddAccount(AccountConfig{UserID: 5, FirstName: "Eve", Login: &script})
		if err != nil {
			t.Fatal(err)
		}
		return clk, c
	}
	script := LoginScript{Phone: "+15550100", Code: "12345", Password: "hunter2", PasswordHint: "pet"}

	t.Run("unauthorized account refuses rpcs", func(t *testing.T) {
		_, c := newLogin(t, script)
		if _, err := c.Self(ctx); !errors.Is(err, tgclient.ErrNotAuthorized) {
			t.Fatalf("error = %v", err)
		}
		if _, err := c.CreateChannel(ctx, "x", ""); !errors.Is(err, tgclient.ErrNotAuthorized) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("code then password", func(t *testing.T) {
		_, c := newLogin(t, script)
		sent, err := c.SendCode(ctx, "+15550100")
		if err != nil || sent.PhoneCodeHash == "" || sent.Timeout <= 0 {
			t.Fatalf("SendCode = %+v, %v", sent, err)
		}
		if _, err := c.SignIn(ctx, "+15550100", sent.PhoneCodeHash, "00000"); !errors.Is(err, tgclient.ErrPhoneCodeInvalid) {
			t.Fatalf("wrong code error = %v", err)
		}
		if _, err := c.SignIn(ctx, "+15550100", sent.PhoneCodeHash, "12345"); !errors.Is(err, tgclient.ErrPasswordNeeded) {
			t.Fatalf("code accepted error = %v", err)
		}
		hint, err := c.GetPasswordHint(ctx)
		if err != nil || hint != "pet" {
			t.Fatalf("hint = %q, %v", hint, err)
		}
		if _, err := c.CheckPassword(ctx, "wrong"); !errors.Is(err, tgclient.ErrPasswordInvalid) {
			t.Fatalf("wrong password error = %v", err)
		}
		acct, err := c.CheckPassword(ctx, "hunter2")
		if err != nil || acct.UserID != 5 || !c.Authorized() {
			t.Fatalf("CheckPassword = %+v, %v", acct, err)
		}
		if self, err := c.Self(ctx); err != nil || self.FirstName != "Eve" {
			t.Fatalf("Self = %+v, %v", self, err)
		}
		if err := c.LogOut(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Self(ctx); !errors.Is(err, tgclient.ErrNotAuthorized) {
			t.Fatalf("after logout error = %v", err)
		}
	})

	t.Run("invalid code retries in place with the same hash", func(t *testing.T) {
		_, c := newLogin(t, LoginScript{Phone: "+15550100", Code: "12345"})
		sent, _ := c.SendCode(ctx, "+15550100")
		for range 3 {
			if _, err := c.SignIn(ctx, "+15550100", sent.PhoneCodeHash, "nope"); !errors.Is(err, tgclient.ErrPhoneCodeInvalid) {
				t.Fatalf("error = %v", err)
			}
		}
		acct, err := c.SignIn(ctx, "+15550100", sent.PhoneCodeHash, "12345")
		if err != nil || acct.UserID != 5 || !c.Authorized() {
			t.Fatalf("SignIn = %+v, %v", acct, err)
		}
	})

	t.Run("expired and replaced codes", func(t *testing.T) {
		clk, c := newLogin(t, LoginScript{Phone: "+15550100", Code: "12345", CodeLifetime: time.Minute})
		first, _ := c.SendCode(ctx, "+15550100")
		clk.Advance(2 * time.Minute)
		if _, err := c.SignIn(ctx, "+15550100", first.PhoneCodeHash, "12345"); !errors.Is(err, tgclient.ErrPhoneCodeExpired) {
			t.Fatalf("expired error = %v", err)
		}
		second, _ := c.SendCode(ctx, "+15550100")
		if second.PhoneCodeHash == first.PhoneCodeHash {
			t.Fatal("resent code reused the hash")
		}
		if _, err := c.SignIn(ctx, "+15550100", first.PhoneCodeHash, "12345"); !errors.Is(err, tgclient.ErrPhoneCodeExpired) {
			t.Fatalf("stale hash error = %v", err)
		}
		if _, err := c.SignIn(ctx, "+15550100", second.PhoneCodeHash, "12345"); err != nil {
			t.Fatalf("fresh hash error = %v", err)
		}
	})

	t.Run("wrong phone and password without a code", func(t *testing.T) {
		_, c := newLogin(t, script)
		if _, err := c.SendCode(ctx, "+1999"); !errors.Is(err, ErrPhoneNumberInvalid) {
			t.Fatalf("error = %v", err)
		}
		if _, err := c.CheckPassword(ctx, "hunter2"); !errors.Is(err, tgclient.ErrNotAuthorized) {
			t.Fatalf("error = %v", err)
		}
		if _, err := c.GetPasswordHint(ctx); !errors.Is(err, tgclient.ErrNotAuthorized) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestFakeConstructorsRejectNilDependencies(t *testing.T) {
	if _, err := NewNetwork(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil clock error = %v", err)
	}
	if _, err := NewNetwork(clock.NewFake(time.Time{}), WithDelay(nil)); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("nil delay error = %v", err)
	}
}

func TestFakeCustomDelay(t *testing.T) {
	var delays []time.Duration
	n, err := NewNetwork(clock.NewFake(time.Unix(0, 0)), WithDelay(func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := n.AddAccount(AccountConfig{UserID: 1})
	_ = c.SetLatency(MethodSelf, 2*time.Second)
	_, _ = c.Self(context.Background())
	if len(delays) != 1 || delays[0] != 2*time.Second {
		t.Fatalf("delays = %v", delays)
	}
}
