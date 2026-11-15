package xmlcodec

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

func Decode(r io.Reader, limit int64, v any) error {
	body, err := readBounded(r, limit)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Errorf("xmlcodec: decode: %w", ErrEmptyBody)
	}
	if err := rejectDirectives(body); err != nil {
		return err
	}
	if err := xml.NewDecoder(bytes.NewReader(body)).Decode(v); err != nil {
		return fmt.Errorf("xmlcodec: decode: %w: %w", ErrMalformed, err)
	}
	return nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		limit = 0
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("xmlcodec: read body: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("xmlcodec: read body: %w", ErrBodyTooLarge)
	}
	return body, nil
}

func rejectDirectives(body []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.RawToken()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("xmlcodec: decode: %w: %w", ErrMalformed, err)
		}
		if _, isDirective := token.(xml.Directive); isDirective {
			return fmt.Errorf("xmlcodec: decode: %w", ErrDoctype)
		}
	}
}
