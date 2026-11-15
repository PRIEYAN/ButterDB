package sigv4

import (
	"context"
	"net/url"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
)

type AuthKind int

const (
	AuthAnonymous AuthKind = iota
	AuthHeader
	AuthPresigned
)

type PayloadMode int

const (
	PayloadNone PayloadMode = iota
	PayloadSignedHex
	PayloadUnsigned
	PayloadStreamingSigned
	PayloadStreamingSignedTrailer
	PayloadStreamingUnsignedTrailer
)

type Reason int

const (
	ReasonSignature Reason = iota + 1
	ReasonUnknownKey
	ReasonHeaderMalformed
	ReasonQueryMalformed
	ReasonWrongRegion
	ReasonSkew
	ReasonExpired
	ReasonNotYetValid
	ReasonMissingDate
	ReasonMissingContentSHA256
	ReasonBadContentSHA256
	ReasonContentSHA256Mismatch
	ReasonSecurityToken
	ReasonSigV2
	ReasonSigV4A
	ReasonBothMechanisms
	ReasonUnsupportedScheme
	ReasonChunkFraming
	ReasonIncompleteBody
	ReasonChecksumMismatch
	ReasonChecksumMalformed
)

type Error struct {
	Reason           Reason
	Err              error
	AccessKeyID      string
	StringToSign     string
	CanonicalRequest string
	Region           string
	RequestTime      time.Time
	ServerTime       time.Time
	Expires          time.Time
	Algorithm        string
	ClientSHA256     string
	ServerSHA256     string
}

type Config struct {
	Region     string
	MaxSkew    time.Duration
	MaxPresign time.Duration
	Now        func() time.Time
}

type SecretSource interface {
	Secret(ctx context.Context, id access.AccessKeyID) ([]byte, access.KeyInfo, error)
}

type Auth struct {
	Kind          AuthKind
	Key           access.KeyInfo
	Mode          PayloadMode
	ContentSHA256 string
	DecodedLength int64
	Trailer       string
	RequestTime   time.Time
	Region        string
	seed          string
	signingKey    []byte
}

type PresignInput struct {
	Method      string
	URL         *url.URL
	AccessKeyID access.AccessKeyID
	Secret      []byte
	Region      string
	Time        time.Time
	Expires     time.Duration
}
