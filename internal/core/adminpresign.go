package core

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

var _ handlers.Presigner = (*adminPresigner)(nil)

type presignSecrets interface {
	Secret(ctx context.Context, id access.AccessKeyID) ([]byte, access.KeyInfo, error)
}

type adminPresigner struct {
	secrets   presignSecrets
	clock     clock.Clock
	publicURL string
	region    string
}

func (p *adminPresigner) PresignGet(ctx context.Context, id access.AccessKeyID, name bucket.BucketName, key object.Key, ttl time.Duration) (string, time.Time, error) {
	if p.publicURL == "" {
		return "", time.Time{}, fmt.Errorf("core: admin: presign: %w", handlers.ErrS3PublicURLUnset)
	}
	target, err := p.target(name, key)
	if err != nil {
		return "", time.Time{}, err
	}
	secret, _, err := p.secrets.Secret(ctx, id)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("core: admin: presign: %w", err)
	}
	defer clear(secret)
	now := p.clock.Now()
	signed, err := sigv4.Presign(sigv4.PresignInput{
		Method: http.MethodGet, URL: target, AccessKeyID: id, Secret: secret, Region: p.region, Time: now, Expires: ttl,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("core: admin: presign: %w", err)
	}
	return signed, now.Add(ttl), nil
}

func (p *adminPresigner) target(name bucket.BucketName, key object.Key) (*url.URL, error) {
	base, err := url.Parse(p.publicURL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("core: admin: presign: s3 public url is not usable: %w", handlers.ErrS3PublicURLUnset)
	}
	target := *base
	target.Path = strings.TrimRight(base.Path, "/") + "/" + string(name) + "/" + string(key)
	target.RawPath = ""
	target.RawQuery = ""
	return &target, nil
}
