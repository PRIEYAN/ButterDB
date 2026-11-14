package log

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func HashHeader(rawHeader string) string {
	sum := sha256.Sum256([]byte(rawHeader))
	return hex.EncodeToString(sum[:])
}

func headerOf(rawText string) string {
	return strings.TrimSpace(wire.ExtractHeaderLine(rawText))
}
