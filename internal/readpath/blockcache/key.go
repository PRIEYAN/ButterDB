package blockcache

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	blockFileSuffix = ".blk"
	tempFilePrefix  = ".tmp-"
	dirMode         = 0o700
	fileMode        = 0o600

	defaultMaxEntries = 64 << 10

	BlockAlignment int64 = 4 * 1024
	BlockMaxBytes        = 1024 * 1024
)

type blockKey struct {
	channelID int64
	messageID int64
	offset    int64
	length    int
}

func (k blockKey) relativeName() string {
	var b strings.Builder
	b.WriteString("bdblock1|")
	b.WriteString(strconv.FormatInt(k.channelID, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(k.messageID, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(k.offset, 10))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(k.length))
	digest := sha256.Sum256([]byte(b.String()))
	encoded := hex.EncodeToString(digest[:])
	return filepath.Join(encoded[:2], encoded[2:4], encoded+blockFileSuffix)
}

func cacheableShape(offset int64, length int) bool {
	if length <= 0 || length > BlockMaxBytes || offset < 0 || offset%BlockAlignment != 0 {
		return false
	}
	first := offset / int64(BlockMaxBytes)
	last := (offset + int64(length) - 1) / int64(BlockMaxBytes)
	return first == last
}
