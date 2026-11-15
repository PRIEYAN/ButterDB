package httprange

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

const (
	conditionIfMatch           = "If-Match"
	conditionIfUnmodifiedSince = "If-Unmodified-Since"
	conditionIfNoneMatch       = "If-None-Match"
	conditionIfModifiedSince   = "If-Modified-Since"
	copySourcePrefix           = "x-amz-copy-source-"
)

type Entity struct {
	ETag         string
	LastModified time.Time
}

func EntityFromMeta(meta object.ObjectMeta) Entity {
	return Entity{ETag: meta.ETag, LastModified: time.Unix(meta.MsgDate, 0).UTC()}
}

type Conditions struct {
	IfMatch           []string
	IfNoneMatch       []string
	IfModifiedSince   string
	IfUnmodifiedSince string
	IfRange           string
}

func ConditionsFromHeader(header http.Header) Conditions {
	return Conditions{
		IfMatch:           header.Values(conditionIfMatch),
		IfNoneMatch:       header.Values(conditionIfNoneMatch),
		IfModifiedSince:   header.Get(conditionIfModifiedSince),
		IfUnmodifiedSince: header.Get(conditionIfUnmodifiedSince),
		IfRange:           header.Get("If-Range"),
	}
}

func ConditionsFromCopySourceHeader(header http.Header) Conditions {
	return Conditions{
		IfMatch:           header.Values(copySourcePrefix + "if-match"),
		IfNoneMatch:       header.Values(copySourcePrefix + "if-none-match"),
		IfModifiedSince:   header.Get(copySourcePrefix + "if-modified-since"),
		IfUnmodifiedSince: header.Get(copySourcePrefix + "if-unmodified-since"),
	}
}

type Result struct {
	NotModified bool
	Condition   string
	IgnoreRange bool
}

type PreconditionError struct {
	Condition string
}

func (e *PreconditionError) Error() string {
	return fmt.Sprintf("httprange: condition %s not met", e.Condition)
}

func (e *PreconditionError) Unwrap() error {
	return errs.ErrPreconditionFailed
}

func Evaluate(conditions Conditions, entity Entity, now time.Time) (Result, error) {
	ifMatch, ifMatchUsable := usableETagCondition(conditions.IfMatch)
	if ifMatchUsable && !matchesStrong(ifMatch, entity.ETag) {
		return Result{}, &PreconditionError{Condition: conditionIfMatch}
	}
	if !ifMatchUsable && unmodifiedSinceFails(conditions.IfUnmodifiedSince, entity, now) {
		return Result{}, &PreconditionError{Condition: conditionIfUnmodifiedSince}
	}
	ifNoneMatch, ifNoneMatchUsable := usableETagCondition(conditions.IfNoneMatch)
	if ifNoneMatchUsable && matchesWeak(ifNoneMatch, entity.ETag) {
		return Result{NotModified: true, Condition: conditionIfNoneMatch}, nil
	}
	if !ifNoneMatchUsable && notModifiedSince(conditions.IfModifiedSince, entity, now) {
		return Result{NotModified: true, Condition: conditionIfModifiedSince}, nil
	}
	return Result{IgnoreRange: ifRangeFails(conditions.IfRange, entity)}, nil
}

func EvaluateCopySource(conditions Conditions, entity Entity, now time.Time) error {
	result, err := Evaluate(conditions, entity, now)
	if err != nil {
		return copySourceError(err)
	}
	if result.NotModified {
		return &PreconditionError{Condition: copySourcePrefix + strings.ToLower(result.Condition)}
	}
	return nil
}

func copySourceError(err error) error {
	var precondition *PreconditionError
	if !errors.As(err, &precondition) {
		return err
	}
	return &PreconditionError{Condition: copySourcePrefix + strings.ToLower(precondition.Condition)}
}

func usableETagCondition(values []string) (ETagConditions, bool) {
	conditions, ok := ParseETagConditions(values)
	if !ok || !conditions.Present {
		return ETagConditions{}, false
	}
	return conditions, true
}

func parseConditionDate(value string, now time.Time) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := http.ParseTime(strings.TrimSpace(value))
	if err != nil || parsed.After(now) {
		return time.Time{}, false
	}
	return parsed, true
}

func unmodifiedSinceFails(value string, entity Entity, now time.Time) bool {
	date, ok := parseConditionDate(value, now)
	if !ok || entity.LastModified.IsZero() {
		return false
	}
	return entity.LastModified.Truncate(time.Second).After(date)
}

func notModifiedSince(value string, entity Entity, now time.Time) bool {
	date, ok := parseConditionDate(value, now)
	if !ok || entity.LastModified.IsZero() {
		return false
	}
	return !entity.LastModified.Truncate(time.Second).After(date)
}

func ifRangeFails(value string, entity Entity) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if date, err := http.ParseTime(value); err == nil {
		return entity.LastModified.IsZero() || !entity.LastModified.Truncate(time.Second).Equal(date)
	}
	tag, remaining, ok := consumeEntityTag(value)
	if !ok || tag.Weak || strings.TrimSpace(remaining) != "" {
		return true
	}
	return tag.Opaque != currentOpaque(entity.ETag)
}
