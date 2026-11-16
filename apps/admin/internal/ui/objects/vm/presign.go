package vm

import (
	"context"
	"slices"
	"sort"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	MinPresignSeconds     = 1
	MaxPresignSeconds     = 604800
	DefaultPresignSeconds = 3600
	CustomExpiryLabel     = "Custom"

	PresignWarning     = "Anyone with this URL can download the object until it expires or the key is revoked."
	NoSigningKeysText  = "Create an access key with Read on this bucket first."
	actionRead         = "read"
	scopeAllBuckets    = "*"
	accessKeyActiveTag = "active"
)

type SigningKey struct {
	ID   string
	Name string
}

type ExpiryOption struct {
	Label   string
	Seconds int
}

type PresignOutcome struct {
	URL     adminv1.PresignedURL
	Problem *model.Problem
}

func ExpiryOptions() []ExpiryOption {
	return []ExpiryOption{
		{Label: "15 minutes", Seconds: 900},
		{Label: "1 hour", Seconds: DefaultPresignSeconds},
		{Label: "24 hours", Seconds: 86400},
		{Label: "7 days", Seconds: MaxPresignSeconds},
	}
}

func FilterSigningKeys(keys []adminv1.AccessKey, bucket string) []SigningKey {
	out := make([]SigningKey, 0, len(keys))
	for _, key := range keys {
		if key.Status != accessKeyActiveTag || key.RevokedAt != nil {
			continue
		}
		if !slices.Contains(key.Scope.Actions, actionRead) {
			continue
		}
		if !slices.Contains(key.Scope.Buckets, scopeAllBuckets) && !slices.Contains(key.Scope.Buckets, bucket) {
			continue
		}
		out = append(out, SigningKey{ID: key.AccessKeyID, Name: key.Name})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (v *ViewModel) SigningKeys(ctx context.Context, bucket string) ([]SigningKey, *model.Problem) {
	list, err := v.client.ListAccessKeys(ctx, false)
	if err != nil {
		return nil, v.problemFor(err)
	}
	return FilterSigningKeys(list.AccessKeys, bucket), nil
}

func (v *ViewModel) Presign(ctx context.Context, bucket, key, accessKeyID string, expiresSeconds int) PresignOutcome {
	if accessKeyID == "" {
		return PresignOutcome{Problem: validationProblem("signing_key", "choose a signing key")}
	}
	if expiresSeconds < MinPresignSeconds || expiresSeconds > MaxPresignSeconds {
		return PresignOutcome{Problem: validationProblem("expires", "use 1 second to 7 days")}
	}
	url, err := v.client.Presign(ctx, bucket, adminv1.PresignRequest{Key: key, AccessKeyID: accessKeyID, ExpiresInSeconds: expiresSeconds})
	if err != nil {
		return PresignOutcome{Problem: v.problemFor(err)}
	}
	return PresignOutcome{URL: url}
}
