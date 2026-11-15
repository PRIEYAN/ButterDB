package httprange

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseETagConditions(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   ETagConditions
		ok     bool
	}{
		{name: "absent", ok: true},
		{name: "wildcard", values: []string{" * "}, want: ETagConditions{Present: true, Any: true}, ok: true},
		{
			name:   "strong and weak list",
			values: []string{`"one,with-comma"`, ` W/"two"`},
			want: ETagConditions{Present: true, Tags: []EntityTag{
				{Opaque: "one,with-comma"},
				{Weak: true, Opaque: "two"},
			}},
			ok: true,
		},
		{name: "unquoted tag", values: []string{"abc"}, want: ETagConditions{Present: true, Tags: []EntityTag{{Opaque: "abc"}}}, ok: true},
		{
			name:   "unquoted list",
			values: []string{"abc, def"},
			want:   ETagConditions{Present: true, Tags: []EntityTag{{Opaque: "abc"}, {Opaque: "def"}}},
			ok:     true,
		},
		{
			name:   "mixed quoted and unquoted",
			values: []string{`"abc", def`},
			want:   ETagConditions{Present: true, Tags: []EntityTag{{Opaque: "abc"}, {Opaque: "def"}}},
			ok:     true,
		},
		{name: "empty", values: []string{""}},
		{name: "unterminated", values: []string{`"abc`}},
		{name: "trailing comma", values: []string{`"abc",`}},
		{name: "missing comma", values: []string{`"abc" "def"`}},
		{name: "unquoted missing comma", values: []string{"abc def"}},
		{name: "control", values: []string{"\"a\tb\""}},
		{name: "wildcard mixed", values: []string{`*, "abc"`}},
		{name: "wildcard after tag", values: []string{`"abc", *`}},
		{name: "stray quote in bare tag", values: []string{`ab"c`}},
		{name: "over header cap", values: []string{`"` + strings.Repeat("a", maxConditionalHeaderBytes) + `"`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseETagConditions(test.values)
			if ok != test.ok || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ParseETagConditions(%q) = (%+v, %t), want (%+v, %t)", test.values, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestEntityTagSerializationAndValidation(t *testing.T) {
	if got := SerializeEntityTag(EntityTag{Weak: true, Opaque: "revision"}); got != `W/"revision"` {
		t.Fatalf("serialized weak ETag = %q", got)
	}
	if got := SerializeEntityTag(EntityTag{Opaque: "revision"}); got != `"revision"` {
		t.Fatalf("serialized strong ETag = %q", got)
	}
	for _, value := range []string{`"ok"`, `"comma,ok"`, `"back\\slash"`, ` "padded" `, "bare"} {
		if !validStrongETag(value) {
			t.Errorf("validStrongETag(%q) = false", value)
		}
	}
	for _, value := range []string{`W/"weak"`, "", `"has space"`, `"unterminated`, `"a" "b"`, "*"} {
		if validStrongETag(value) {
			t.Errorf("validStrongETag(%q) = true", value)
		}
	}
}
