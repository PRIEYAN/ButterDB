package shell

import (
	"strconv"
	"time"
)

const (
	bytesPerUnit    = 1024
	byteUnitLetters = "KMGTPE"
	justNowBelow    = time.Minute
	daysShownBelow  = 30 * 24 * time.Hour
)

func FormatBytes(n int64) string {
	if n < bytesPerUnit {
		if n < 0 {
			n = 0
		}
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(bytesPerUnit), 0
	for m := n / bytesPerUnit; m >= bytesPerUnit && exp < len(byteUnitLetters)-1; m /= bytesPerUnit {
		div *= bytesPerUnit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string(byteUnitLetters[exp]) + "iB"
}

func FormatAge(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	age := now.Sub(t)
	switch {
	case age < justNowBelow:
		return "just now"
	case age < time.Hour:
		return strconv.Itoa(int(age/time.Minute)) + " min ago"
	case age < 24*time.Hour:
		return strconv.Itoa(int(age/time.Hour)) + " h ago"
	case age < daysShownBelow:
		return strconv.Itoa(int(age/(24*time.Hour))) + " d ago"
	}
	return t.In(now.Location()).Format("2006-01-02")
}

func countdownText(remaining time.Duration) string {
	seconds := int((remaining + time.Second - 1) / time.Second)
	if seconds <= 0 {
		return "You can retry now."
	}
	if seconds < 60 {
		return "Try again in " + strconv.Itoa(seconds) + " s."
	}
	return "Try again in " + strconv.Itoa(seconds/60) + " min " + strconv.Itoa(seconds%60) + " s."
}
