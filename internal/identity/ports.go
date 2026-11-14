package identity

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type AuthRPC interface {
	SendCode(ctx context.Context, phone string) (tgclient.SentCode, error)
	SignIn(ctx context.Context, phone, phoneCodeHash, code string) (tgclient.Account, error)
	CheckPassword(ctx context.Context, password string) (tgclient.Account, error)
	GetPasswordHint(ctx context.Context) (string, error)
	LogOut(ctx context.Context) error
}

type SelfReader interface {
	Self(ctx context.Context) (tgclient.Account, error)
}

type SecretWriter interface {
	Write(path string, data []byte) error
}

type FileReader interface {
	ReadFile(path string) ([]byte, error)
}

type FileRemover interface {
	Remove(path string) error
}

type SessionDiscarder interface {
	Delete(ctx context.Context) error
}

type StoredAccount struct {
	Role        string
	UserID      int64
	Username    string
	PhoneMasked string
	DCID        int
	Linked      bool
	BoundAt     int64
	LinkedAt    int64
	UnlinkedAt  int64
}

type AccountStore interface {
	Get(ctx context.Context, role string) (StoredAccount, bool, error)
	FindByUser(ctx context.Context, userID int64) (string, bool, error)
	Link(ctx context.Context, account StoredAccount) error
	Unlink(ctx context.Context, role string, at int64) error
}
