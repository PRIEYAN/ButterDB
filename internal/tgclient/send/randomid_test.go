package send

import (
	"errors"
	"strings"
	"testing"
)

func TestStableRandomIDVectors(t *testing.T) {
	tests := []struct {
		name    string
		jobID   string
		purpose string
		want    int64
	}{
		{name: "body", jobID: "7f136fd2-dfbd-4e91-9421-181298cc1e20", purpose: "body", want: -3105839358345735791},
		{name: "put", jobID: "7f136fd2-dfbd-4e91-9421-181298cc1e20", purpose: "put", want: -3988114258281980785},
		{name: "registry", jobID: "deployment-1", purpose: "reg:1", want: -8939519607386301655},
		{name: "maximum length", jobID: strings.Repeat("a", 256), purpose: "del", want: -5355332429258622005},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := StableRandomID(test.jobID, test.purpose)
			if err != nil {
				t.Fatalf("StableRandomID: %v", err)
			}
			if got != test.want {
				t.Fatalf("StableRandomID = %d, want %d", got, test.want)
			}
			if got == 0 || got&1 != 1 {
				t.Fatalf("StableRandomID = %d, want odd", got)
			}
		})
	}
}

func TestStableRandomIDIsDeterministicAndNamespaced(t *testing.T) {
	const job = "7f136fd2-dfbd-4e91-9421-181298cc1e20"
	first, err := StableRandomID(job, "body")
	if err != nil {
		t.Fatalf("StableRandomID: %v", err)
	}
	again, err := StableRandomID(job, "body")
	if err != nil {
		t.Fatalf("StableRandomID again: %v", err)
	}
	put, err := StableRandomID(job, "put")
	if err != nil {
		t.Fatalf("StableRandomID put: %v", err)
	}
	if first != again {
		t.Fatalf("same input produced %d and %d", first, again)
	}
	if first == put {
		t.Fatalf("different steps produced the same id %d", first)
	}
}

func TestStableRandomIDRejectsAmbiguousKeys(t *testing.T) {
	tests := []struct {
		name    string
		jobID   string
		purpose string
	}{
		{name: "empty job", purpose: "body"},
		{name: "trimmed job", jobID: " op-1 ", purpose: "body"},
		{name: "empty purpose", jobID: "op-1"},
		{name: "trimmed purpose", jobID: "op-1", purpose: " body "},
		{name: "job NUL", jobID: "op\x00one", purpose: "body"},
		{name: "invalid UTF-8", jobID: string([]byte{0xff}), purpose: "body"},
		{name: "oversized job", jobID: strings.Repeat("a", 257), purpose: "body"},
		{name: "oversized purpose", jobID: "op-1", purpose: strings.Repeat("a", 257)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := StableRandomID(test.jobID, test.purpose); !errors.Is(err, ErrInvalidIdempotencyKey) {
				t.Fatalf("StableRandomID error = %v, want ErrInvalidIdempotencyKey", err)
			}
		})
	}
}
