package tgclient

import (
	"context"
	"errors"
	"io"
	"time"
)

type InputPeer struct {
	ChannelID  int64
	AccessHash int64
}

type DocumentRef struct {
	Peer          InputPeer
	MsgID         int64
	Size          int64
	DocumentID    int64
	AccessHash    int64
	FileReference []byte
	DCID          int
}

type HistoryMessage struct {
	MsgID        int64
	Date         int64
	FromID       int64
	Text         string
	HasMedia     bool
	MediaSize    int64
	DocumentName string
	Placeholder  bool
}

type FloodWaitError struct {
	Duration time.Duration
}

func (e *FloodWaitError) Error() string {
	return "tgclient: flood wait " + e.Duration.String()
}

func (e *FloodWaitError) Is(target error) bool {
	return target == ErrFloodWait
}

var ErrFloodWait = errors.New("tgclient: flood wait")
var ErrMessageNotFound = errors.New("tgclient: message not found")
var ErrChannelUnavailable = errors.New("tgclient: peer unavailable")
var ErrSendOutcomeUnknown = errors.New("tgclient: send outcome unknown")
var ErrZeroAccessHash = errors.New("tgclient: zero access hash")
var ErrFileReferenceExpired = errors.New("tgclient: file reference expired")
var ErrPhoneCodeInvalid = errors.New("tgclient: phone code invalid")
var ErrPhoneCodeExpired = errors.New("tgclient: phone code expired")
var ErrPasswordNeeded = errors.New("tgclient: two-step password needed")
var ErrPasswordInvalid = errors.New("tgclient: two-step password invalid")
var ErrNotAuthorized = errors.New("tgclient: not authorized")

type SendFileRequest struct {
	Peer     InputPeer
	Caption  string
	FileName string
	Size     int64
	Body     io.ReadSeeker
	RandomID int64
}

type SendFileResult struct {
	MsgID    int64
	Document DocumentRef
}

type SendControlRequest struct {
	Peer     InputPeer
	Text     string
	RandomID int64
}

type Channel struct {
	Peer  InputPeer
	Title string
	About string
}

type ChannelDifference struct {
	Messages          []HistoryMessage
	DeletedMessageIDs []int64
	Pts               int
	Final             bool
	TooLong           bool
}

type SentCode struct {
	PhoneCodeHash string
	Timeout       time.Duration
}

type Account struct {
	UserID    int64
	FirstName string
	Username  string
}

type WriteClass int

const (
	WriteControl WriteClass = iota + 1
	WriteDocument
	WriteDelete
	WriteChannel
)

type WriteGate interface {
	Do(ctx context.Context, class WriteClass, action func() error) error
}

type GetFileGate interface {
	AcquireForeground(ctx context.Context, n int) (release func(), err error)
	TryAcquireBackground(n int) (release func(), ok bool)
}

type Sender interface {
	SendFileIdempotent(ctx context.Context, req SendFileRequest) (SendFileResult, error)
	SendControlIdempotent(ctx context.Context, req SendControlRequest) (int64, error)
}

type RangeReader interface {
	ResolveDocument(ctx context.Context, peer InputPeer, msgID int64) (DocumentRef, error)
	ResolveDocuments(ctx context.Context, peer InputPeer, msgIDs []int64) ([]DocumentRef, error)
	ReadDocumentRange(ctx context.Context, ref DocumentRef, offset int64, dst []byte) (int, error)
}

type ChannelRPC interface {
	CreateChannel(ctx context.Context, title, about string) (InputPeer, error)
	GetHistory(ctx context.Context, peer InputPeer, offsetID int64, limit int) ([]HistoryMessage, error)
	GetChannelDifference(ctx context.Context, peer InputPeer, pts int, limit int) (ChannelDifference, error)
	GetChannelPts(ctx context.Context, peer InputPeer) (int, error)
	GetMessages(ctx context.Context, peer InputPeer, msgIDs []int64) ([]HistoryMessage, error)
	DeleteMessages(ctx context.Context, peer InputPeer, msgIDs []int64) error
	ListOwnedChannels(ctx context.Context) ([]Channel, error)
	ResolveChannel(ctx context.Context, channelID int64) (InputPeer, error)
	SendToSavedMessages(ctx context.Context, text string) (int64, error)
	Self(ctx context.Context) (Account, error)
}

type AuthRPC interface {
	SendCode(ctx context.Context, phone string) (SentCode, error)
	SignIn(ctx context.Context, phone, phoneCodeHash, code string) (Account, error)
	CheckPassword(ctx context.Context, password string) (Account, error)
	GetPasswordHint(ctx context.Context) (string, error)
	LogOut(ctx context.Context) error
}
