package s3err

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	HeaderRequestID = "X-Amz-Request-Id"
	HeaderHostID    = "X-Amz-Id-2"

	requestIDBytes = 8
	hostIDBytes    = 32
	unsetRequestID = "0000000000000000"
)

func NewIDs(random io.Reader) (requestID, hostID string, err error) {
	request := make([]byte, requestIDBytes)
	if _, err := io.ReadFull(random, request); err != nil {
		return "", "", fmt.Errorf("s3err: request id: %w", err)
	}
	host := make([]byte, hostIDBytes)
	if _, err := io.ReadFull(random, host); err != nil {
		return "", "", fmt.Errorf("s3err: host id: %w", err)
	}
	return strings.ToUpper(hex.EncodeToString(request)), base64.StdEncoding.EncodeToString(host), nil
}

func RequestIDs(h http.Header) (requestID, hostID string) {
	requestID = h.Get(HeaderRequestID)
	if requestID == "" {
		requestID = unsetRequestID
	}
	hostID = h.Get(HeaderHostID)
	if hostID == "" {
		hostID = base64.StdEncoding.EncodeToString(make([]byte, hostIDBytes))
	}
	return requestID, hostID
}
