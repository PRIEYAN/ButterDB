package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/ingest/remove"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

const (
	deviceScopePrefix = "device:"
	keyScopePrefix    = "key:"
	deviceWriterTag   = "dev."
	keyWriterTag      = "key."
)

type principalKey struct{}

func WithPrincipal(ctx context.Context, principal remove.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func PrincipalForCaller(accessKeyID, requestID string) (remove.Principal, error) {
	if device, ok := strings.CutPrefix(accessKeyID, deviceScopePrefix); ok && device != "" {
		return remove.Principal{Origin: jobrepo.OriginAdmin, Scope: accessKeyID, Writer: deviceWriterTag + device, RequestID: requestID}, nil
	}
	if accessKeyID == "" || strings.HasPrefix(accessKeyID, deviceScopePrefix) {
		return remove.Principal{}, fmt.Errorf("core: caller identity is required: %w", errs.ErrAccessDenied)
	}
	return remove.Principal{Origin: jobrepo.OriginS3, Scope: keyScopePrefix + accessKeyID, Writer: keyWriterTag + accessKeyID, RequestID: requestID}, nil
}

type principalContext struct{}

func (principalContext) Principal(ctx context.Context) (remove.Principal, error) {
	principal, ok := ctx.Value(principalKey{}).(remove.Principal)
	if !ok || principal.Scope == "" {
		return remove.Principal{}, fmt.Errorf("core: caller identity is required: %w", errs.ErrAccessDenied)
	}
	return principal, nil
}
