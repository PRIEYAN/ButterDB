package buckets

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	projectTitlePrefix  = "butterDB · "
	maxProjectNameRunes = 64
)

var ErrProjectName = errors.New("buckets: project name must be 1 to 64 characters without control characters")

func ProjectTitle(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n == 0 || n > maxProjectNameRunes || !utf8.ValidString(name) {
		return "", ErrProjectName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrProjectName
		}
	}
	return projectTitlePrefix + name, nil
}

func ProjectNameFromTitle(title string) string {
	if name, ok := strings.CutPrefix(title, projectTitlePrefix); ok && name != "" {
		return name
	}
	if title == registryChannelTitle {
		return "registry"
	}
	return title
}

func titleOrDefault(title string) string {
	if title == "" {
		return registryChannelTitle
	}
	return title
}
