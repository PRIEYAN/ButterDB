package vm

import (
	"slices"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	ActionRead   = "read"
	ActionList   = "list"
	ActionWrite  = "write"
	ActionDelete = "delete"

	AllBucketsMarker = "*"

	MaxNameCharacters = 64

	FieldName  = "name"
	FieldScope = "scope"

	reasonNameLength = "Use 1 to 64 characters."
)

func AllActions() []string {
	return []string{ActionRead, ActionList, ActionWrite, ActionDelete}
}

func PresetReadOnly() []string {
	return []string{ActionRead, ActionList}
}

func PresetReadWrite() []string {
	return AllActions()
}

func ActionWord(action string) string {
	switch action {
	case ActionRead:
		return "Read"
	case ActionList:
		return "List"
	case ActionWrite:
		return "Write"
	case ActionDelete:
		return "Delete"
	}
	return action
}

type KeyScope struct {
	AllBuckets bool
	Buckets    []string
	Actions    []string
}

type NewKey struct {
	Name  string
	Scope KeyScope
}

type KeyEdit struct {
	Name  string
	Scope KeyScope
}

func (s KeyScope) Wire() adminv1.Scope {
	buckets := []string{AllBucketsMarker}
	if !s.AllBuckets {
		buckets = uniqueSorted(s.Buckets)
	}
	return adminv1.Scope{Buckets: buckets, Actions: canonicalActions(s.Actions)}
}

func ScopeOf(wire adminv1.Scope) KeyScope {
	if slices.Contains(wire.Buckets, AllBucketsMarker) {
		return KeyScope{AllBuckets: true, Actions: canonicalActions(wire.Actions)}
	}
	return KeyScope{Buckets: uniqueSorted(wire.Buckets), Actions: canonicalActions(wire.Actions)}
}

func BucketsText(scope adminv1.Scope) string {
	if slices.Contains(scope.Buckets, AllBucketsMarker) {
		return "All buckets"
	}
	return strings.Join(scope.Buckets, ", ")
}

func ActionsText(scope adminv1.Scope) string {
	words := make([]string, 0, len(scope.Actions))
	for _, action := range canonicalActions(scope.Actions) {
		words = append(words, ActionWord(action))
	}
	return strings.Join(words, ", ")
}

func canonicalActions(in []string) []string {
	out := make([]string, 0, len(in))
	for _, action := range AllActions() {
		if slices.Contains(in, action) {
			out = append(out, action)
		}
	}
	return out
}

func uniqueSorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func sameScope(a, b adminv1.Scope) bool {
	return slices.Equal(uniqueSorted(a.Buckets), uniqueSorted(b.Buckets)) &&
		slices.Equal(canonicalActions(a.Actions), canonicalActions(b.Actions))
}

func CheckName(name string) string {
	n := len([]rune(strings.TrimSpace(name)))
	if n < 1 || n > MaxNameCharacters {
		return reasonNameLength
	}
	return ""
}

func precheck(name string, scope KeyScope, now time.Time) *model.Problem {
	if reason := CheckName(name); reason != "" {
		return localProblem(adminv1.CodeValidationFailed, map[string]any{"field": FieldName, "reason": reason}, now)
	}
	wire := scope.Wire()
	if len(wire.Actions) == 0 || (!scope.AllBuckets && len(wire.Buckets) == 0) {
		return localProblem(adminv1.CodeScopeInvalid, nil, now)
	}
	return nil
}

func trimmed(name string) string {
	return strings.TrimSpace(name)
}

func FormatCreated(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(now.Location()).Format("2006-01-02 15:04")
}
