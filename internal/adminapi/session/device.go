package session

import (
	"context"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

type deviceContextKey struct{}

type clientAddrContextKey struct{}

func DeviceFromContext(ctx context.Context) (adminrepo.Device, bool) {
	d, ok := ctx.Value(deviceContextKey{}).(adminrepo.Device)
	return d, ok
}

func ContextWithDevice(ctx context.Context, d adminrepo.Device) context.Context {
	return context.WithValue(ctx, deviceContextKey{}, d)
}

func ContextWithClientAddr(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, clientAddrContextKey{}, addr)
}

func ClientAddrFromContext(ctx context.Context) (string, bool) {
	addr, ok := ctx.Value(clientAddrContextKey{}).(string)
	return addr, ok && addr != ""
}

func DeviceDTO(d adminrepo.Device, current bool) adminv1.Device {
	dto := adminv1.Device{
		DeviceID:      d.DeviceID,
		Name:          d.Name,
		Platform:      d.Platform,
		CreatedAt:     d.CreatedAt,
		CreatedVia:    string(d.CreatedVia),
		LastSeenAt:    d.LastSeenAt,
		LastSeenIP:    d.LastSeenIP,
		TokenIssuedAt: d.TokenIssuedAt,
		Current:       current,
	}
	if !d.RevokedAt.IsZero() {
		revokedAt := d.RevokedAt
		dto.RevokedAt = &revokedAt
	}
	return dto
}

func formatMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
