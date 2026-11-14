package rangeio

import (
	"io"
)

const (
	RangeReadAlignment int64 = 4 * 1024
	RangeReadMaxBytes        = 1024 * 1024
)

func copyRequestedRange(dst, src []byte) (int, error) {
	if len(src) < len(dst) {
		return 0, io.ErrUnexpectedEOF
	}
	copy(dst, src[:len(dst)])
	return len(dst), nil
}

func roundedTelegramLimit(want int) int {
	if want <= 0 {
		return 0
	}
	alignment := int(RangeReadAlignment)
	rounded := ((want + alignment - 1) / alignment) * alignment
	if rounded > RangeReadMaxBytes {
		return RangeReadMaxBytes
	}
	return rounded
}

func crossesRangeBoundary(offset int64, length int) bool {
	if length <= 0 {
		return false
	}
	start := offset / int64(RangeReadMaxBytes)
	end := (offset + int64(length) - 1) / int64(RangeReadMaxBytes)
	return start != end
}
