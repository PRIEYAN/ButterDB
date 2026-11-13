package wire

import (
	"errors"
	"testing"
)

func TestMalformedUnless(t *testing.T) {
	if err := malformedUnless(true); err != nil {
		t.Fatalf("malformedUnless(true) = %v, want nil", err)
	}
	if err := malformedUnless(false); err != ErrWireMalformed {
		t.Fatalf("malformedUnless(false) = %v, want ErrWireMalformed", err)
	}
}

func TestSentinelErrorsAreDistinct(t *testing.T) {
	sentinels := []error{ErrWireMissingHeader, ErrWireBadVersion, ErrWireMalformed, ErrWireUnknownOp, ErrWireTooLong}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if (i == j) != errors.Is(a, b) {
				t.Fatalf("errors.Is(%v, %v) = %v", a, b, errors.Is(a, b))
			}
		}
	}
}

func TestEveryParseFailureReturnsASentinel(t *testing.T) {
	cases := map[string]error{
		"":                                      ErrWireMissingHeader,
		"   \n":                                 ErrWireMissingHeader,
		"TDX1":                                  ErrWireMalformed,
		"TDX1|":                                 ErrWireMalformed,
		"TDX1|x":                                ErrWireMalformed,
		"TDX1|t":                                ErrWireMalformed,
		"TDX1|t=":                               ErrWireMalformed,
		"TDX1|j=1":                              ErrWireMalformed,
		"TDX2|t=put":                            ErrWireBadVersion,
		"tdx1|t=put":                            ErrWireBadVersion,
		"TDX1|t=nope":                           ErrWireUnknownOp,
		"TDX1|t=put|k=" + longRun(MaxTextChars): ErrWireTooLong,
	}
	for raw, want := range cases {
		if _, err := Parse(raw); err != want {
			t.Fatalf("Parse(%.40q) error = %v, want %v", raw, err, want)
		}
	}
}

func longRun(n int) string {
	run := make([]byte, n)
	for i := range run {
		run[i] = 'a'
	}
	return string(run)
}
