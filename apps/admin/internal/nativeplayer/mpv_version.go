package nativeplayer

import (
	"regexp"
	"strconv"
)

type mpvVersion struct {
	major, minor int
}

var mpvBannerPattern = regexp.MustCompile(`\bmpv v?(\d+)\.(\d+)`)

func parseMPVVersion(banner string) mpvVersion {
	match := mpvBannerPattern.FindStringSubmatch(banner)
	if match == nil {
		return mpvVersion{}
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	return mpvVersion{major: major, minor: minor}
}

func (v mpvVersion) known() bool {
	return v.major > 0 || v.minor > 0
}

func (v mpvVersion) atLeast(major, minor int) bool {
	return v.major > major || (v.major == major && v.minor >= minor)
}

func (v mpvVersion) String() string {
	if !v.known() {
		return "unknown"
	}
	return strconv.Itoa(v.major) + "." + strconv.Itoa(v.minor)
}
