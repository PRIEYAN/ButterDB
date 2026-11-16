package dashboard

import (
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
)

const (
	statusOK       = "ok"
	statusDegraded = "degraded"
	statusDown     = "down"
)

func serverStatusWord(state string) string {
	switch state {
	case "ready":
		return statusOK
	case "":
		return statusDown
	}
	return statusDegraded
}

func statusIcon(word string) fyne.Resource {
	switch word {
	case statusOK:
		return theme.NewSuccessThemedResource(theme.ConfirmIcon())
	case statusDown:
		return theme.NewErrorThemedResource(theme.ErrorIcon())
	}
	return theme.NewWarningThemedResource(theme.WarningIcon())
}

func statusGlyph(word string) fyne.Resource {
	switch word {
	case statusOK:
		return theme.ConfirmIcon()
	case statusDown:
		return theme.ErrorIcon()
	}
	return theme.WarningIcon()
}

func componentWord(status string) string {
	switch status {
	case statusOK, statusDegraded, statusDown:
		return status
	}
	return statusDegraded
}

func uptimeText(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	d := time.Duration(seconds) * time.Second
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	minutes := int(d % time.Hour / time.Minute)
	switch {
	case days > 0:
		return strconv.Itoa(days) + " d " + strconv.Itoa(hours) + " h"
	case hours > 0:
		return strconv.Itoa(hours) + " h " + strconv.Itoa(minutes) + " min"
	case minutes > 0:
		return strconv.Itoa(minutes) + " min"
	}
	return strconv.Itoa(int(d/time.Second)) + " s"
}

func percentText(ratio float64) string {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return strconv.Itoa(int(ratio*100+0.5)) + "%"
}

func usedOfLimitText(used, limit int64) string {
	if limit <= 0 {
		return shell.FormatBytes(used) + " used, no limit set"
	}
	return shell.FormatBytes(used) + " of " + shell.FormatBytes(limit)
}

func floodWaitText(until, now time.Time) string {
	return "Flood wait until " + until.In(now.Location()).Format("15:04:05")
}

func versionText(version, commit string) string {
	if commit == "" {
		return version
	}
	return version + " (" + commit + ")"
}
