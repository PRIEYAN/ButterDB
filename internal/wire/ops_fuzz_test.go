package wire

import (
	"errors"
	"reflect"
	"testing"
)

func FuzzRoundTrip(f *testing.F) {
	for _, header := range workedExampleHeaders(f) {
		f.Add(header)
	}
	for _, entry := range roundTripTable(f) {
		header, err := Format(entry.op)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(header)
	}
	f.Add(headerWith(putHeader, "k", "%2A+%7e"))
	f.Add(putHeader + "|m.%61=x|s.expires=y")
	f.Add(encPutHeader + "|ev=7")
	f.Fuzz(func(t *testing.T, raw string) {
		op, err := Parse(raw)
		if err != nil {
			return
		}
		header, err := Format(op)
		if err != nil {
			encryptedWithBadVersion := op.Encrypted && op.EncryptionVersion < 1
			if !errors.Is(err, ErrWireTooLong) && !encryptedWithBadVersion {
				t.Fatalf("Format failed for a parsed op: %v\nraw: %q\nop: %#v", err, raw, op)
			}
			return
		}
		again, err := Parse(header)
		if err != nil {
			t.Fatalf("Parse rejected Format output %q: %v", header, err)
		}
		if !reflect.DeepEqual(again, op) {
			t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v\nheader: %s", again, op, header)
		}
	})
}
