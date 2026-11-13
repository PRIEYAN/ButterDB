package access

import (
	"fmt"
	"slices"
)

const (
	AllBucketsWildcard = "*"
	maxBucketName      = 63
	minBucketName      = 3
)

type Scope struct {
	buckets []string
	actions []Action
}

func NewScope(buckets []string, actions []string) (Scope, error) {
	checkedBuckets, err := checkScopeBuckets(buckets)
	if err != nil {
		return Scope{}, err
	}
	checkedActions, err := checkScopeActions(actions)
	if err != nil {
		return Scope{}, err
	}
	return Scope{buckets: checkedBuckets, actions: checkedActions}, nil
}

func (scope Scope) Buckets() []string {
	return slices.Clone(scope.buckets)
}

func (scope Scope) Actions() []Action {
	return slices.Clone(scope.actions)
}

func (scope Scope) AllBuckets() bool {
	return len(scope.buckets) == 1 && scope.buckets[0] == AllBucketsWildcard
}

func (scope Scope) CoversBucket(name string) bool {
	return scope.AllBuckets() || slices.Contains(scope.buckets, name)
}

func (scope Scope) HasAction(action Action) bool {
	return slices.Contains(scope.actions, action)
}

func (scope Scope) Allows(bucket string, action Action) bool {
	return scope.CoversBucket(bucket) && scope.HasAction(action)
}

func checkScopeBuckets(buckets []string) ([]string, error) {
	if len(buckets) == 0 {
		return nil, scopeError("buckets are empty")
	}
	if slices.Contains(buckets, AllBucketsWildcard) {
		if len(buckets) != 1 {
			return nil, scopeError("'*' must be the only bucket entry")
		}
		return []string{AllBucketsWildcard}, nil
	}
	seen := make(map[string]struct{}, len(buckets))
	for _, name := range buckets {
		if !plausibleBucketName(name) {
			return nil, scopeError("bucket entry is not a bucket name")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, scopeError("bucket entries repeat")
		}
		seen[name] = struct{}{}
	}
	return slices.Clone(buckets), nil
}

func checkScopeActions(actions []string) ([]Action, error) {
	if len(actions) == 0 {
		return nil, scopeError("actions are empty")
	}
	granted := make(map[Action]struct{}, len(actions))
	for _, raw := range actions {
		action, err := ParseAction(raw)
		if err != nil {
			return nil, fmt.Errorf("access: scope: %w: %w", ErrInvalidScope, err)
		}
		granted[action] = struct{}{}
	}
	canonical := make([]Action, 0, len(granted))
	for _, action := range AllActions() {
		if _, ok := granted[action]; ok {
			canonical = append(canonical, action)
		}
	}
	return canonical, nil
}

func scopeError(reason string) error {
	return fmt.Errorf("access: scope: %w: %s", ErrInvalidScope, reason)
}

func plausibleBucketName(name string) bool {
	if len(name) < minBucketName || len(name) > maxBucketName {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') && b != '.' && b != '-' {
			return false
		}
	}
	return true
}
