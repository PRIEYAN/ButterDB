package httprange

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const rangeUnit = "bytes"

type ByteRange struct {
	Start int64
	End   int64
}

func (r ByteRange) Length() int64 {
	if r.End < r.Start {
		return 0
	}
	return r.End - r.Start + 1
}

type Selection struct {
	Partial bool
	Range   ByteRange
}

type UnsatisfiableRangeError struct {
	Requested string
	Size      int64
}

func (e *UnsatisfiableRangeError) Error() string {
	return fmt.Sprintf("httprange: range not satisfiable for size %d", e.Size)
}

func (e *UnsatisfiableRangeError) Unwrap() error {
	return errs.ErrInvalidRange
}

func (e *UnsatisfiableRangeError) ContentRange() string {
	return UnsatisfiedContentRange(e.Size)
}

func UnsatisfiedContentRange(size int64) string {
	return fmt.Sprintf("bytes */%d", size)
}

func ParseRange(header string, size int64) (Selection, error) {
	spec, ok := singleRangeSpec(header)
	if !ok {
		return Selection{}, nil
	}
	first, last, ok := splitRangeSpec(spec)
	if !ok {
		return Selection{}, nil
	}
	selection, satisfiable := resolveRange(first, last, size)
	if !satisfiable {
		return Selection{}, &UnsatisfiableRangeError{Requested: header, Size: size}
	}
	return selection, nil
}

func singleRangeSpec(header string) (string, bool) {
	header = strings.TrimSpace(header)
	unit, spec, found := strings.Cut(header, "=")
	if !found || !strings.EqualFold(strings.TrimSpace(unit), rangeUnit) {
		return "", false
	}
	if strings.Contains(spec, ",") {
		return "", false
	}
	return strings.TrimSpace(spec), true
}

type rangeBound struct {
	present bool
	value   int64
}

func splitRangeSpec(spec string) (first, last rangeBound, ok bool) {
	firstText, lastText, found := strings.Cut(spec, "-")
	if !found || (firstText == "" && lastText == "") {
		return rangeBound{}, rangeBound{}, false
	}
	first, ok = parseBound(firstText)
	if !ok {
		return rangeBound{}, rangeBound{}, false
	}
	last, ok = parseBound(lastText)
	if !ok {
		return rangeBound{}, rangeBound{}, false
	}
	if first.present && last.present && last.value < first.value {
		return rangeBound{}, rangeBound{}, false
	}
	return first, last, true
}

func parseBound(text string) (rangeBound, bool) {
	if text == "" {
		return rangeBound{}, true
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return rangeBound{}, false
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		value = 1<<63 - 1
	}
	return rangeBound{present: true, value: value}, true
}

func resolveRange(first, last rangeBound, size int64) (Selection, bool) {
	if size <= 0 {
		return Selection{}, false
	}
	switch {
	case !first.present:
		if last.value == 0 {
			return Selection{}, false
		}
		length := last.value
		if length > size {
			length = size
		}
		return partial(size-length, size-1), true
	case !last.present:
		if first.value >= size {
			return Selection{}, false
		}
		return partial(first.value, size-1), true
	default:
		if first.value >= size {
			return Selection{}, false
		}
		end := last.value
		if end >= size {
			end = size - 1
		}
		return partial(first.value, end), true
	}
}

func partial(start, end int64) Selection {
	return Selection{Partial: true, Range: ByteRange{Start: start, End: end}}
}

type Plan struct {
	Status       int
	Range        ByteRange
	Length       int64
	ContentRange string
	Size         int64
}

func NewPlan(rangeHeader string, size int64, ignoreRange bool) (Plan, error) {
	if ignoreRange {
		return wholeObjectPlan(size), nil
	}
	selection, err := ParseRange(rangeHeader, size)
	if err != nil {
		return Plan{}, err
	}
	if !selection.Partial {
		return wholeObjectPlan(size), nil
	}
	return partialPlan(selection.Range, size), nil
}

func PartNumberOnePlan(size int64) Plan {
	if size <= 0 {
		return wholeObjectPlan(size)
	}
	return partialPlan(ByteRange{Start: 0, End: size - 1}, size)
}

func wholeObjectPlan(size int64) Plan {
	return Plan{
		Status: http.StatusOK,
		Range:  ByteRange{Start: 0, End: size - 1},
		Length: size,
		Size:   size,
	}
}

func partialPlan(selected ByteRange, size int64) Plan {
	return Plan{
		Status:       http.StatusPartialContent,
		Range:        selected,
		Length:       selected.Length(),
		ContentRange: fmt.Sprintf("bytes %d-%d/%d", selected.Start, selected.End, size),
		Size:         size,
	}
}

func (p Plan) SetHeaders(header http.Header) {
	header.Set("Accept-Ranges", rangeUnit)
	header.Set("Content-Length", strconv.FormatInt(p.Length, 10))
	if p.ContentRange != "" {
		header.Set("Content-Range", p.ContentRange)
	}
}

func SetUnsatisfiedHeaders(header http.Header, size int64) {
	header.Set("Accept-Ranges", rangeUnit)
	header.Set("Content-Range", UnsatisfiedContentRange(size))
}
