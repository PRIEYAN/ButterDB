package access

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestAccessKeyIDFormat(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		valid bool
	}{
		{name: "spec example", id: "BDBK7Q2M9X4T1W8Z3R6Y", valid: true},
		{name: "all letters", id: "BDBKABCDEFGHIJKLMNOP", valid: true},
		{name: "all digits", id: "BDBK0123456789012345", valid: true},
		{name: "empty", id: ""},
		{name: "too short", id: "BDBK7Q2M9X4T1W8Z3R6"},
		{name: "too long", id: "BDBK7Q2M9X4T1W8Z3R6YA"},
		{name: "wrong prefix", id: "AKIA7Q2M9X4T1W8Z3R6Y"},
		{name: "lowercase prefix", id: "bdbk7Q2M9X4T1W8Z3R6Y"},
		{name: "lowercase body", id: "BDBK7q2M9X4T1W8Z3R6Y"},
		{name: "punctuation", id: "BDBK7Q2M9X4T1W8Z3R-Y"},
		{name: "space", id: "BDBK7Q2M9X4T1W8Z3R Y"},
		{name: "non ascii", id: "BDBK7Q2M9X4T1W8Z3Ré"},
		{name: "prefix only repeated", id: "BDBKBDBKBDBKBDBKBDB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id, err := ParseAccessKeyID(test.id)
			if test.valid {
				if err != nil || id.String() != test.id {
					t.Fatalf("ParseAccessKeyID(%q) = %q, %v", test.id, id, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidAccessKeyID) {
				t.Fatalf("ParseAccessKeyID(%q) error = %v", test.id, err)
			}
		})
	}
}

func TestActionParsing(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  Action
		valid bool
	}{
		{name: "read", raw: "read", want: ActionRead, valid: true},
		{name: "list", raw: "list", want: ActionList, valid: true},
		{name: "write", raw: "write", want: ActionWrite, valid: true},
		{name: "delete", raw: "delete", want: ActionDelete, valid: true},
		{name: "empty", raw: ""},
		{name: "uppercase", raw: "READ"},
		{name: "padded", raw: " read"},
		{name: "unknown", raw: "admin"},
		{name: "s3 style", raw: "s3:GetObject"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseAction(test.raw)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("ParseAction(%q) = %q, %v", test.raw, got, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidAction) {
				t.Fatalf("ParseAction(%q) error = %v", test.raw, err)
			}
		})
	}
	if want := []Action{ActionRead, ActionList, ActionWrite, ActionDelete}; !slices.Equal(AllActions(), want) {
		t.Fatalf("AllActions() = %v, want %v", AllActions(), want)
	}
}

func TestScopeValidation(t *testing.T) {
	tests := []struct {
		name        string
		buckets     []string
		actions     []string
		wantErr     bool
		wantActions []Action
		wantAll     bool
	}{
		{name: "named buckets", buckets: []string{"photos", "avatars"}, actions: []string{"read", "list", "write"}, wantActions: []Action{ActionRead, ActionList, ActionWrite}},
		{name: "wildcard", buckets: []string{"*"}, actions: []string{"read"}, wantActions: []Action{ActionRead}, wantAll: true},
		{name: "all actions", buckets: []string{"photos"}, actions: []string{"delete", "write", "list", "read"}, wantActions: AllActions()},
		{name: "duplicate actions collapse", buckets: []string{"photos"}, actions: []string{"read", "read"}, wantActions: []Action{ActionRead}},
		{name: "nil buckets", buckets: nil, actions: []string{"read"}, wantErr: true},
		{name: "empty buckets", buckets: []string{}, actions: []string{"read"}, wantErr: true},
		{name: "nil actions", buckets: []string{"photos"}, actions: nil, wantErr: true},
		{name: "empty actions", buckets: []string{"photos"}, actions: []string{}, wantErr: true},
		{name: "unknown action", buckets: []string{"photos"}, actions: []string{"read", "admin"}, wantErr: true},
		{name: "wildcard with name", buckets: []string{"*", "photos"}, actions: []string{"read"}, wantErr: true},
		{name: "name with wildcard", buckets: []string{"photos", "*"}, actions: []string{"read"}, wantErr: true},
		{name: "duplicate wildcard", buckets: []string{"*", "*"}, actions: []string{"read"}, wantErr: true},
		{name: "glob is not wildcard", buckets: []string{"pho*"}, actions: []string{"read"}, wantErr: true},
		{name: "empty bucket name", buckets: []string{""}, actions: []string{"read"}, wantErr: true},
		{name: "uppercase bucket name", buckets: []string{"Photos"}, actions: []string{"read"}, wantErr: true},
		{name: "too long bucket name", buckets: []string{strings.Repeat("a", 64)}, actions: []string{"read"}, wantErr: true},
		{name: "duplicate bucket names", buckets: []string{"photos", "photos"}, actions: []string{"read"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope, err := NewScope(test.buckets, test.actions)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidScope) {
					t.Fatalf("NewScope() error = %v, want ErrInvalidScope", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewScope() error = %v", err)
			}
			if !slices.Equal(scope.Actions(), test.wantActions) {
				t.Fatalf("Actions() = %v, want %v", scope.Actions(), test.wantActions)
			}
			if scope.AllBuckets() != test.wantAll {
				t.Fatalf("AllBuckets() = %v, want %v", scope.AllBuckets(), test.wantAll)
			}
		})
	}
}

func TestScopeIsImmutable(t *testing.T) {
	buckets := []string{"photos"}
	actions := []string{"read"}
	scope, err := NewScope(buckets, actions)
	if err != nil {
		t.Fatal(err)
	}
	buckets[0] = "other"
	actions[0] = "delete"
	scope.Buckets()[0] = "tampered"
	scope.Actions()[0] = ActionDelete
	if !scope.Allows("photos", ActionRead) || scope.Allows("other", ActionDelete) || scope.Allows("tampered", ActionDelete) {
		t.Fatal("scope changed after construction")
	}
}

func TestScopeAllows(t *testing.T) {
	named, _ := NewScope([]string{"photos"}, []string{"read", "list"})
	wild, _ := NewScope([]string{"*"}, []string{"write"})
	tests := []struct {
		name   string
		scope  Scope
		bucket string
		action Action
		want   bool
	}{
		{name: "named bucket allowed action", scope: named, bucket: "photos", action: ActionRead, want: true},
		{name: "named bucket missing action", scope: named, bucket: "photos", action: ActionWrite},
		{name: "other bucket", scope: named, bucket: "avatars", action: ActionRead},
		{name: "wildcard any bucket", scope: wild, bucket: "anything", action: ActionWrite, want: true},
		{name: "wildcard missing action", scope: wild, bucket: "anything", action: ActionRead},
		{name: "zero scope", scope: Scope{}, bucket: "photos", action: ActionRead},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.scope.Allows(test.bucket, test.action); got != test.want {
				t.Fatalf("Allows() = %v, want %v", got, test.want)
			}
		})
	}
}
