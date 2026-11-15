package xmlcodec

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
)

type deleteRequest struct {
	XMLName xml.Name `xml:"Delete"`
	Quiet   bool     `xml:"Quiet"`
	Objects []struct {
		Key string `xml:"Key"`
	} `xml:"Object"`
}

type countingReader struct {
	r    io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

func TestDecodeWithoutNamespace(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no namespace", `<Delete><Quiet>true</Quiet><Object><Key>a</Key></Object></Delete>`},
		{"s3 namespace", `<Delete xmlns="` + Namespace + `"><Quiet>true</Quiet><Object><Key>a</Key></Object></Delete>`},
		{"declaration and whitespace", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n <Delete><Quiet>true</Quiet><Object><Key>a</Key></Object></Delete>\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got deleteRequest
			if err := Decode(strings.NewReader(tc.body), 1<<20, &got); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !got.Quiet || len(got.Objects) != 1 || got.Objects[0].Key != "a" {
				t.Fatalf("decoded %+v", got)
			}
		})
	}
}

func TestDecodeRejectsDoctype(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"entity bomb", `<!DOCTYPE d [<!ENTITY a "aaaa">]><Delete><Quiet>&a;</Quiet></Delete>`},
		{"bare doctype", `<!DOCTYPE Delete><Delete/>`},
		{"doctype after declaration", `<?xml version="1.0"?><!DOCTYPE Delete SYSTEM "http://x/y.dtd"><Delete/>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got deleteRequest
			err := Decode(strings.NewReader(tc.body), 1<<20, &got)
			if !errors.Is(err, ErrDoctype) {
				t.Fatalf("Decode error = %v, want ErrDoctype", err)
			}
		})
	}
}

func TestDecodeLimit(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		limit   int64
		wantErr error
	}{
		{"exactly at the limit", `<Delete/>`, 9, nil},
		{"one byte over", `<Delete/>`, 8, ErrBodyTooLarge},
		{"empty body", ``, 10, ErrEmptyBody},
		{"whitespace only body", " \n\t", 10, ErrEmptyBody},
		{"not well formed", `<Delete><Quiet></Delete>`, 100, ErrMalformed},
		{"plain text", `hello`, 100, ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got deleteRequest
			err := Decode(strings.NewReader(tc.body), tc.limit, &got)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Decode error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestXMLBodyLimit(t *testing.T) {
	const limit = 2 << 20
	body := &countingReader{r: io.MultiReader(
		strings.NewReader("<Delete>"),
		io.LimitReader(zeroFill{}, 3<<20),
	)}
	var got deleteRequest
	err := Decode(body, limit, &got)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("Decode error = %v, want ErrBodyTooLarge", err)
	}
	if body.read > limit+1 {
		t.Fatalf("read %d bytes, want at most %d", body.read, limit+1)
	}
}

type zeroFill struct{}

func (zeroFill) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}
