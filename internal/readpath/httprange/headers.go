package httprange

import (
	"net/http"
	"strconv"
	"time"
)

type CacheControlSources struct {
	Override            string
	Stored              string
	PublicRead          bool
	PublicMaxAgeSeconds int
}

func CacheControl(sources CacheControlSources) string {
	switch {
	case sources.Override != "":
		return sources.Override
	case sources.Stored != "":
		return sources.Stored
	case sources.PublicRead && sources.PublicMaxAgeSeconds > 0:
		return "public, max-age=" + strconv.Itoa(sources.PublicMaxAgeSeconds)
	case sources.PublicRead:
		return "no-cache"
	default:
		return ""
	}
}

func SetValidators(header http.Header, entity Entity) {
	header.Set("ETag", `"`+currentOpaque(entity.ETag)+`"`)
	if !entity.LastModified.IsZero() {
		header.Set("Last-Modified", entity.LastModified.UTC().Truncate(time.Second).Format(http.TimeFormat))
	}
}

func WriteNotModified(w http.ResponseWriter, entity Entity, cacheControl, expires string) {
	header := w.Header()
	SetValidators(header, entity)
	if cacheControl != "" {
		header.Set("Cache-Control", cacheControl)
	}
	if expires != "" {
		header.Set("Expires", expires)
	}
	w.WriteHeader(http.StatusNotModified)
}
