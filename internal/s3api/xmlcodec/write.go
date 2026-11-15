package xmlcodec

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const xmlDeclaration = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeFormat)
}

func WriteResult(w http.ResponseWriter, status int, v any) error {
	document, err := marshalDocument(v)
	if err != nil {
		return err
	}
	body := append([]byte(xmlDeclaration), document...)
	header := w.Header()
	header.Set("Content-Type", "application/xml")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("xmlcodec: write result: %w", err)
	}
	return nil
}

func marshalDocument(v any) ([]byte, error) {
	document, err := xml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("xmlcodec: marshal: %w", err)
	}
	return withNamespace(document), nil
}

func withNamespace(document []byte) []byte {
	end := bytes.IndexByte(document, '>')
	if end < 0 || bytes.Contains(document[:end], []byte("xmlns=")) {
		return document
	}
	nameEnd := bytes.IndexAny(document, " />")
	if nameEnd < 0 || nameEnd > end {
		return document
	}
	var out bytes.Buffer
	out.Write(document[:nameEnd])
	out.WriteString(` xmlns="` + Namespace + `"`)
	out.Write(document[nameEnd:])
	return out.Bytes()
}
