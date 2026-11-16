package vm

import "testing"

func TestPairingCodeFormatting(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		want     string
		complete bool
	}{
		{"empty", "", "", false},
		{"five characters get no hyphen", "abcde", "ABCDE", false},
		{"sixth character inserts the hyphen", "abcdef", "ABCDE-F", false},
		{"uppercases as typed", "ab3de-fg", "AB3DE-FG", false},
		{"ten characters complete", "abcdefghjk", "ABCDE-FGHJK", true},
		{"existing hyphen is not doubled", "abcde-fghjk", "ABCDE-FGHJK", true},
		{"spaces and hyphens are stripped", " abc de - fgh jk ", "ABCDE-FGHJK", true},
		{"extra characters are dropped", "abcdefghjkmnp", "ABCDE-FGHJK", true},
		{"punctuation is dropped", "ab.cd!e", "ABCDE", false},
		{"non ascii letters are dropped", "abcdéfgh", "ABCDF-GH", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatPairingCode(tc.input)
			if got != tc.want {
				t.Fatalf("FormatPairingCode(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if PairingCodeComplete(got) != tc.complete {
				t.Fatalf("PairingCodeComplete(%q) = %v, want %v", got, !tc.complete, tc.complete)
			}
		})
	}
}

func TestSetPairingCodeUpdatesState(t *testing.T) {
	f := newFixture(t)
	if got := f.vm.SetPairingCode("abcde"); got != "ABCDE" {
		t.Fatalf("SetPairingCode = %q", got)
	}
	if f.state().PairingCodeComplete {
		t.Fatalf("five characters reported complete")
	}
	f.vm.SetPairingCode("abcdefghjk")
	if s := f.state(); s.PairingCode != "ABCDE-FGHJK" || !s.PairingCodeComplete {
		t.Fatalf("state = %q complete=%v", s.PairingCode, s.PairingCodeComplete)
	}
}

func TestFingerprintLines(t *testing.T) {
	canonical := "SHA256:4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e"
	want := "4f1c 9a0e 4f1c 9a0e\n4f1c 9a0e 4f1c 9a0e\n4f1c 9a0e 4f1c 9a0e\n4f1c 9a0e 4f1c 9a0e"
	display := "4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e"
	for name, input := range map[string]string{"canonical": canonical, "display form": display, "upper case": "sha256:4F1C9A0E4F1C9A0E4F1C9A0E4F1C9A0E4F1C9A0E4F1C9A0E4F1C9A0E4F1C9A0E"} {
		t.Run(name, func(t *testing.T) {
			if got := FingerprintLines(input); got != want {
				t.Fatalf("FingerprintLines = %q, want %q", got, want)
			}
		})
	}
	if got := FingerprintLines(""); got != "" {
		t.Fatalf("FingerprintLines(empty) = %q", got)
	}
}
