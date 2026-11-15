package buckets

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strconv"
)

const purposeBucketCreate = "bcreate"

func newJobID(random io.Reader) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", fmt.Errorf("buckets: mint job id: %w", err)
	}
	return formatUUIDv4(raw), nil
}

func derivedCreateJobID(deploymentID string, channelID int64) string {
	digest := sha256.Sum256([]byte(deploymentID + "\x00" + purposeBucketCreate + "\x00" + strconv.FormatInt(channelID, 10)))
	var raw [16]byte
	copy(raw[:], digest[:16])
	return formatUUIDv4(raw)
}

func formatUUIDv4(raw [16]byte) string {
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
