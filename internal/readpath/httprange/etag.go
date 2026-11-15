package httprange

import "strings"

const maxConditionalHeaderBytes = 16 << 10

type EntityTag struct {
	Weak   bool
	Opaque string
}

type ETagConditions struct {
	Present bool
	Any     bool
	Tags    []EntityTag
}

func SerializeEntityTag(tag EntityTag) string {
	prefix := ""
	if tag.Weak {
		prefix = "W/"
	}
	return prefix + `"` + tag.Opaque + `"`
}

func ParseETagConditions(values []string) (ETagConditions, bool) {
	if len(values) == 0 {
		return ETagConditions{}, true
	}
	input := strings.TrimSpace(strings.Join(values, ","))
	if input == "" || len(input) > maxConditionalHeaderBytes {
		return ETagConditions{}, false
	}
	if input == "*" {
		return ETagConditions{Present: true, Any: true}, true
	}
	tags := make([]EntityTag, 0, 2)
	for len(input) > 0 {
		tag, remaining, ok := consumeEntityTag(strings.TrimSpace(input))
		if !ok {
			return ETagConditions{}, false
		}
		tags = append(tags, tag)
		remaining = strings.TrimSpace(remaining)
		if remaining == "" {
			break
		}
		if remaining[0] != ',' {
			return ETagConditions{}, false
		}
		input = remaining[1:]
		if strings.TrimSpace(input) == "" {
			return ETagConditions{}, false
		}
	}
	return ETagConditions{Present: true, Tags: tags}, len(tags) > 0
}

func consumeEntityTag(input string) (EntityTag, string, bool) {
	if strings.HasPrefix(input, `W/"`) {
		tag, remaining, ok := consumeQuotedTag(input[2:])
		tag.Weak = true
		return tag, remaining, ok
	}
	if strings.HasPrefix(input, `"`) {
		return consumeQuotedTag(input)
	}
	return consumeBareTag(input)
}

func consumeQuotedTag(input string) (EntityTag, string, bool) {
	end := strings.IndexByte(input[1:], '"')
	if end < 0 {
		return EntityTag{}, "", false
	}
	end++
	opaque := input[1:end]
	if !validOpaque(opaque) {
		return EntityTag{}, "", false
	}
	return EntityTag{Opaque: opaque}, input[end+1:], true
}

// S3 clients send entity tags with or without quotes; AWS accepts both.
func consumeBareTag(input string) (EntityTag, string, bool) {
	end := strings.IndexAny(input, ", \t")
	if end < 0 {
		end = len(input)
	}
	opaque := input[:end]
	if opaque == "" || opaque == "*" || !validOpaque(opaque) {
		return EntityTag{}, "", false
	}
	return EntityTag{Opaque: opaque}, input[end:], true
}

func validOpaque(opaque string) bool {
	for _, character := range opaque {
		if character == 0x7f || character < 0x21 || character == '"' {
			return false
		}
	}
	return true
}

func validStrongETag(value string) bool {
	tag, remaining, ok := consumeEntityTag(strings.TrimSpace(value))
	return ok && !tag.Weak && strings.TrimSpace(remaining) == ""
}

func currentOpaque(etag string) string {
	etag = strings.TrimSpace(etag)
	etag = strings.TrimPrefix(etag, "W/")
	if len(etag) >= 2 && etag[0] == '"' && etag[len(etag)-1] == '"' {
		return etag[1 : len(etag)-1]
	}
	return etag
}

func matchesStrong(conditions ETagConditions, etag string) bool {
	if conditions.Any {
		return true
	}
	current := currentOpaque(etag)
	for _, tag := range conditions.Tags {
		if !tag.Weak && tag.Opaque == current {
			return true
		}
	}
	return false
}

func matchesWeak(conditions ETagConditions, etag string) bool {
	if conditions.Any {
		return true
	}
	current := currentOpaque(etag)
	for _, tag := range conditions.Tags {
		if tag.Opaque == current {
			return true
		}
	}
	return false
}
