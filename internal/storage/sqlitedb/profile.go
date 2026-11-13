package sqlitedb

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBusyTimeout     = 5 * time.Second
	defaultSlowTxThreshold = 250 * time.Millisecond
	defaultReadConnsFloor  = 4
	readConnMaxIdleTime    = 5 * time.Minute

	projectionApplicationID = 1650745936
	controlApplicationID    = 1650745923
)

type profile struct {
	applicationID    int
	synchronous      string
	cacheSize        int
	mmapSize         int64
	journalSizeLimit int64
}

func profileFor(name Name) (profile, error) {
	switch name {
	case Projection:
		return profile{
			applicationID:    projectionApplicationID,
			synchronous:      "NORMAL",
			cacheSize:        -32768,
			mmapSize:         134217728,
			journalSizeLimit: 67108864,
		}, nil
	case Control:
		return profile{
			applicationID:    controlApplicationID,
			synchronous:      "FULL",
			cacheSize:        -8192,
			mmapSize:         0,
			journalSizeLimit: 16777216,
		}, nil
	}
	return profile{}, fmt.Errorf("sqlitedb: unknown database name %q", string(name))
}

func ApplicationID(name Name) (int, error) {
	p, err := profileFor(name)
	if err != nil {
		return 0, err
	}
	return p.applicationID, nil
}

func fileURI(path string) string {
	escaped := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
	return "file:" + escaped
}

func sharedPragmas(p profile, busyTimeout time.Duration) []string {
	return []string{
		fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
		"synchronous(" + p.synchronous + ")",
		"foreign_keys(1)",
		"temp_store(MEMORY)",
		fmt.Sprintf("cache_size(%d)", p.cacheSize),
	}
}

func buildDSN(path string, query []string) string {
	return fileURI(path) + "?" + strings.Join(query, "&")
}

func pragmaQuery(pragmas []string) []string {
	query := make([]string, 0, len(pragmas)+1)
	for _, pragma := range pragmas {
		query = append(query, "_pragma="+url.QueryEscape(pragma))
	}
	return query
}

func writerDSN(path string, p profile, busyTimeout time.Duration) string {
	pragmas := append(sharedPragmas(p, busyTimeout), fmt.Sprintf("journal_size_limit(%d)", p.journalSizeLimit))
	return buildDSN(path, append([]string{"_txlock=immediate"}, pragmaQuery(pragmas)...))
}

func readerDSN(path string, p profile, busyTimeout time.Duration) string {
	pragmas := append(sharedPragmas(p, busyTimeout), "query_only(1)", fmt.Sprintf("mmap_size(%d)", p.mmapSize))
	return buildDSN(path, pragmaQuery(pragmas))
}
