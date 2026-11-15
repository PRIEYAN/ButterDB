package policy

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

type liveBucketLister interface {
	ListLive(ctx context.Context) ([]bucket.Info, error)
}

type Evaluator struct {
	buckets BucketSource
}

func NewEvaluator(buckets BucketSource) *Evaluator {
	return &Evaluator{buckets: buckets}
}

func (e *Evaluator) Authorize(ctx context.Context, p Principal, b bucket.BucketName, a access.Action) (bucket.Info, error) {
	if p.Anonymous {
		return bucket.Info{}, fmt.Errorf("policy: authorize %q: %w", string(b), errs.ErrAccessDenied)
	}
	scope := p.Key.Scope
	if !scope.CoversBucket(string(b)) {
		return bucket.Info{}, fmt.Errorf("policy: authorize %q: %w", string(b), errs.ErrAccessDenied)
	}
	info, found, err := e.lookup(ctx, b)
	if err != nil {
		return bucket.Info{}, err
	}
	if !found {
		return bucket.Info{}, fmt.Errorf("policy: authorize %q: %w", string(b), errs.ErrNoSuchBucket)
	}
	if !scope.HasAction(a) {
		return bucket.Info{}, fmt.Errorf("policy: authorize %q: %w", string(b), errs.ErrAccessDenied)
	}
	return info, nil
}

func (e *Evaluator) AuthorizeAnonymousRead(ctx context.Context, b bucket.BucketName) (bucket.Info, error) {
	info, found, err := e.lookup(ctx, b)
	if err != nil {
		return bucket.Info{}, err
	}
	if !found || info.Policy != bucket.PolicyPublicRead {
		return bucket.Info{}, fmt.Errorf("policy: anonymous read %q: %w", string(b), errs.ErrAccessDenied)
	}
	return info, nil
}

func (e *Evaluator) VisibleBuckets(ctx context.Context, p Principal) ([]bucket.Info, error) {
	if p.Anonymous {
		return nil, fmt.Errorf("policy: list buckets: %w", errs.ErrAccessDenied)
	}
	scope := p.Key.Scope
	if lister, ok := e.buckets.(liveBucketLister); ok {
		return listInScope(ctx, lister, scope)
	}
	if scope.AllBuckets() {
		return nil, fmt.Errorf("policy: list buckets: %w: bucket lister", errs.ErrMissingDependency)
	}
	return e.lookupNamed(ctx, scope)
}

func listInScope(ctx context.Context, lister liveBucketLister, scope access.Scope) ([]bucket.Info, error) {
	all, err := lister.ListLive(ctx)
	if err != nil {
		return nil, fmt.Errorf("policy: list buckets: %w", err)
	}
	visible := make([]bucket.Info, 0, len(all))
	for _, info := range all {
		if scope.CoversBucket(string(info.Name)) {
			visible = append(visible, info)
		}
	}
	return visible, nil
}

func (e *Evaluator) lookupNamed(ctx context.Context, scope access.Scope) ([]bucket.Info, error) {
	names := scope.Buckets()
	slices.Sort(names)
	visible := make([]bucket.Info, 0, len(names))
	for _, name := range names {
		info, found, err := e.lookup(ctx, bucket.BucketName(name))
		if err != nil {
			return nil, err
		}
		if found {
			visible = append(visible, info)
		}
	}
	slices.SortFunc(visible, func(a, b bucket.Info) int { return strings.Compare(string(a.Name), string(b.Name)) })
	return visible, nil
}

func (e *Evaluator) lookup(ctx context.Context, name bucket.BucketName) (bucket.Info, bool, error) {
	if e.buckets == nil {
		return bucket.Info{}, false, fmt.Errorf("policy: lookup: %w: buckets", errs.ErrMissingDependency)
	}
	info, found, err := e.buckets.Lookup(ctx, name)
	if err != nil {
		return bucket.Info{}, false, fmt.Errorf("policy: lookup %q: %w", string(name), err)
	}
	return info, found, nil
}
