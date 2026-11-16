package settings

import (
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
)

const (
	safetyNotice = "Use a dedicated Telegram account that is not new, with two-step verification on. Never share your api_id. butterDB keeps a send-rate limit on; if Telegram restricts the account, objects that are not mirrored to a replica account are lost."

	unlinkBody = "The server stops syncing and uploading until an account is linked again. Only the same Telegram account can be relinked."

	lastDeviceTitle = "This is the last active device."
	lastDeviceBody  = "This is the last active device. After revoking it, only butterdbd admin-token on the host can add a device."

	linkTelegramFirst = "Link Telegram first"

	timeLayout = "2006-01-02 15:04"
)

func trustText(t adminv1.Trust) string {
	switch t.Mode {
	case adminv1.TrustPinned:
		return "Pinned certificate key"
	case adminv1.TrustSystem:
		return "System certificate store"
	}
	return string(t.Mode)
}

func storageText(kind model.TokenStorage) string {
	switch kind {
	case model.TokenKeychain:
		return "OS keychain"
	case model.TokenFile:
		return "Protected file"
	case model.TokenMemory:
		return "Memory only (this session)"
	}
	return string(kind)
}

func timeWithAge(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return shell.FormatAge(t, now) + " (" + t.In(now.Location()).Format(timeLayout) + ")"
}

func deviceStatusText(d adminv1.Device, now time.Time) string {
	if d.RevokedAt != nil {
		return "revoked at " + d.RevokedAt.In(now.Location()).Format(timeLayout)
	}
	return "active"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func filterLabel(prefix string) string {
	if prefix == "" {
		return "All"
	}
	return prefix
}

func auditTimeText(t time.Time, now time.Time) string {
	return t.In(now.Location()).Format("2006-01-02 15:04:05")
}
