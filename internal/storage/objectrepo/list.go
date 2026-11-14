package objectrepo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

const listSelect = `SELECT object_key, put_msg_id, stored_size, plaintext_size, etag, msg_date, encrypted, content_type
FROM objects
WHERE channel_id = ?1 AND object_key >= ?2`

const (
	listBounded   = listSelect + ` AND object_key < ?3 ORDER BY object_key LIMIT ?4`
	listUnbounded = listSelect + ` ORDER BY object_key LIMIT ?3`
)

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type entry struct {
	object   object.ObjectMeta
	resume   resumePoint
	isPrefix bool
}

func (r *Repository) List(ctx context.Context, channelID int64, query object.ListQuery) (object.ListPage, error) {
	var page object.ListPage
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		page, err = listPage(ctx, tx, channelID, query)
		return err
	})
	if err != nil {
		return object.ListPage{}, fmt.Errorf("objectrepo: list: %w", err)
	}
	return page, nil
}

type listing struct {
	q         querier
	channelID int64
	prefix    string
	delimiter string
	upper     string
	hasUpper  bool
	want      int
	entries   []entry
}

func listPage(ctx context.Context, q querier, channelID int64, query object.ListQuery) (object.ListPage, error) {
	if query.Limit <= 0 {
		return object.ListPage{}, nil
	}
	lower, ok, err := startBound(query)
	if err != nil || !ok {
		return object.ListPage{}, err
	}
	l := &listing{q: q, channelID: channelID, prefix: query.Prefix, delimiter: query.Delimiter, want: query.Limit + 1}
	l.upper, l.hasUpper = PrefixEnd(query.Prefix)
	if err := l.collect(ctx, lower); err != nil {
		return object.ListPage{}, err
	}
	return l.page(query.Limit), nil
}

func startBound(query object.ListQuery) (string, bool, error) {
	lower := query.Prefix
	var floor string
	switch {
	case query.ContinuationToken != "":
		point, err := parseToken(query.ContinuationToken)
		if err != nil {
			return "", false, err
		}
		bound, ok := point.lowerBound()
		if !ok {
			return "", false, nil
		}
		floor = bound
	case query.StartAfter != "":
		floor = successor(query.StartAfter)
	}
	return max(lower, floor), true, nil
}

func (l *listing) collect(ctx context.Context, lower string) error {
	for len(l.entries) < l.want {
		if l.hasUpper && lower >= l.upper {
			return nil
		}
		next, skipped, err := l.scan(ctx, lower)
		if err != nil {
			return err
		}
		if !skipped {
			return nil
		}
		lower = next
	}
	return nil
}

func (l *listing) scan(ctx context.Context, lower string) (string, bool, error) {
	remaining := l.want - len(l.entries)
	rows, err := l.queryFrom(ctx, lower, remaining)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		meta, err := scanListed(rows, l.channelID)
		if err != nil {
			return "", false, err
		}
		common, rolled := l.rollUp(meta.Key)
		if !rolled {
			l.entries = append(l.entries, entry{object: meta, resume: resumePoint{value: meta.Key}})
			continue
		}
		l.entries = append(l.entries, entry{isPrefix: true, resume: resumePoint{value: common, isPrefix: true}})
		next, ok := PrefixEnd(common)
		return next, ok, nil
	}
	return "", false, rows.Err()
}

func (l *listing) queryFrom(ctx context.Context, lower string, limit int) (*sql.Rows, error) {
	if l.hasUpper {
		return l.q.QueryContext(ctx, listBounded, l.channelID, lower, l.upper, limit)
	}
	return l.q.QueryContext(ctx, listUnbounded, l.channelID, lower, limit)
}

func (l *listing) rollUp(key string) (string, bool) {
	if l.delimiter == "" {
		return "", false
	}
	remainder := strings.TrimPrefix(key, l.prefix)
	at := strings.Index(remainder, l.delimiter)
	if at < 0 {
		return "", false
	}
	return l.prefix + remainder[:at+len(l.delimiter)], true
}

func (l *listing) page(limit int) object.ListPage {
	kept := l.entries
	var page object.ListPage
	if len(kept) > limit {
		kept = kept[:limit]
		page.NextToken = kept[limit-1].resume.token()
	}
	for _, e := range kept {
		if e.isPrefix {
			page.CommonPrefixes = append(page.CommonPrefixes, e.resume.value)
			continue
		}
		page.Objects = append(page.Objects, e.object)
	}
	return page
}

func scanListed(rows *sql.Rows, channelID int64) (object.ObjectMeta, error) {
	meta := object.ObjectMeta{ChannelID: channelID}
	var encrypted int
	err := rows.Scan(&meta.Key, &meta.PutMsgID, &meta.StoredSize, &meta.PlaintextSize, &meta.ETag, &meta.MsgDate, &encrypted, &meta.ContentType)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	meta.Encrypted = encrypted == 1
	return meta, nil
}
