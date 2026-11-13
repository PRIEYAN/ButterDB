package wire

import (
	"encoding/base64"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type headerBuilder struct {
	out strings.Builder
}

func newHeader(t OpType) *headerBuilder {
	h := &headerBuilder{}
	h.out.WriteString(Prefix)
	h.raw("t", string(t))
	return h
}

func (h *headerBuilder) raw(key, value string) {
	h.out.WriteString(pairSeparator)
	h.out.WriteString(key)
	h.out.WriteByte('=')
	h.out.WriteString(value)
}

func (h *headerBuilder) escaped(key, value string) {
	h.raw(key, url.QueryEscape(value))
}

func (h *headerBuilder) number(key string, value int64) {
	h.raw(key, strconv.FormatInt(value, 10))
}

func (h *headerBuilder) binary(key string, value []byte) {
	h.raw(key, base64.RawURLEncoding.EncodeToString(value))
}

func (h *headerBuilder) entries(prefix string, values map[string]string) {
	for _, name := range slices.Sorted(maps.Keys(values)) {
		h.raw(prefix+url.QueryEscape(name), url.QueryEscape(values[name]))
	}
}

func (h *headerBuilder) optionalWriter(writer string) {
	if writer != "" {
		h.raw("w", writer)
	}
}

func (h *headerBuilder) encryption(op Op) {
	if !op.Encrypted {
		return
	}
	h.raw("enc", "1")
	if op.EncryptionVersion != 1 {
		h.number("ev", int64(op.EncryptionVersion))
	}
}

func (h *headerBuilder) String() string {
	return h.out.String()
}

func Format(op Op) (string, error) {
	format := formatterFor(op.Type)
	if format == nil {
		return "", ErrWireUnknownOp
	}
	header, err := format(op)
	if err != nil {
		return "", err
	}
	if len(header) > carrierLimit(op.Type) {
		return "", ErrWireTooLong
	}
	return header, nil
}

func carrierLimit(t OpType) int {
	if OnDocument(t) {
		return MaxCaptionChars
	}
	return MaxTextChars
}
