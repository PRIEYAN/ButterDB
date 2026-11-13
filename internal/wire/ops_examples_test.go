package wire

import (
	"reflect"
	"testing"
)

func TestWorkedExamplesByteExact(t *testing.T) {
	headers := workedExampleHeaders(t)
	ops := workedExampleOps(t)
	if len(headers) != len(ops) {
		t.Fatalf("examples file has %d headers, fixtures have %d ops", len(headers), len(ops))
	}
	for i, header := range headers {
		t.Run(string(ops[i].Type)+"_"+header[len(header)-10:], func(t *testing.T) {
			got, err := Format(ops[i])
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			if got != header {
				t.Fatalf("Format mismatch\n got: %s\nwant: %s", got, header)
			}
			parsed, err := Parse(header)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(parsed, ops[i]) {
				t.Fatalf("Parse mismatch\n got: %#v\nwant: %#v", parsed, ops[i])
			}
		})
	}
}

func TestFormatCanonicalOrder(t *testing.T) {
	headers := workedExampleHeaders(t)
	for i, op := range workedExampleOps(t) {
		got, err := Format(op)
		if err != nil {
			t.Fatalf("Format(%s): %v", op.Type, err)
		}
		if got != headers[i] {
			t.Fatalf("Format(%s) = %q, want %q", op.Type, got, headers[i])
		}
	}
}
