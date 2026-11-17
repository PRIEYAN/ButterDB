package settings

import (
	"time"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
)

const (
	safetyNotice = "Use a dedicated Telegram account that is not new, with two-step verification on. Never share your api_id. butterDB keeps a send-rate limit on; if Telegram restricts the account, objects that are not mirrored to a replica account are lost."

	removeDeviceTitle = "Remove this account from this device"
	removeDeviceBody  = "butterDB logs out of Telegram and deletes this account, its projects' local data and their keys from this computer. Files stay in your Telegram. To open a project again on this computer you need its password."

	resetKeyBody = "A new key is made for this project. Encrypted files stored with the old key can no longer be read, and every access key of this project is revoked."

	timeLayout = "2006-01-02 15:04"
)

func timeWithAge(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return shell.FormatAge(t, now) + " (" + t.In(now.Location()).Format(timeLayout) + ")"
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
