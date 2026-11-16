package vm

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type Client interface {
	ListAccessKeys(ctx context.Context, includeRevoked bool) (adminv1.AccessKeyList, error)
	CreateAccessKey(ctx context.Context, req adminv1.CreateAccessKeyRequest) (adminv1.AccessKeyCreated, error)
	UpdateAccessKey(ctx context.Context, id string, req adminv1.UpdateAccessKeyRequest) (adminv1.AccessKey, error)
	RevokeAccessKey(ctx context.Context, id string) (adminv1.AccessKey, error)
	ListBuckets(ctx context.Context) (adminv1.BucketList, error)
	Status(ctx context.Context) (adminv1.Status, error)
}

type State struct {
	Loading        bool
	Working        bool
	Problem        *model.Problem
	FormProblem    *model.Problem
	Keys           []adminv1.AccessKey
	IncludeRevoked bool
	BucketNames    []string
	Secret         *Secret
}

type ViewModel struct {
	client Client
	now    func() time.Time
	mu     sync.Mutex
	state  *model.Observable[State]
}

func New(c Client, now func() time.Time) *ViewModel {
	return &ViewModel{client: c, now: now, state: model.NewObservable(State{})}
}

func (v *ViewModel) State() *model.Observable[State] {
	return v.state
}

func (v *ViewModel) Now() time.Time {
	return v.now()
}

func (v *ViewModel) Refresh(ctx context.Context) {
	include := v.state.Get().IncludeRevoked
	v.update(func(s *State) {
		s.Loading = true
		s.Problem = nil
	})
	list, err := v.client.ListAccessKeys(ctx, include)
	problem := v.describe(err)
	v.update(func(s *State) {
		s.Loading = false
		if err != nil {
			s.Problem = problem
			return
		}
		s.Keys = list.AccessKeys
	})
}

func (v *ViewModel) SetIncludeRevoked(ctx context.Context, include bool) {
	v.update(func(s *State) { s.IncludeRevoked = include })
	v.Refresh(ctx)
}

func (v *ViewModel) LoadBuckets(ctx context.Context) {
	list, err := v.client.ListBuckets(ctx)
	problem := v.describe(err)
	v.update(func(s *State) {
		if err != nil && problem != nil && problem.Presentation == model.PresentRoute {
			s.Problem = problem
			return
		}
		if err != nil {
			s.FormProblem = problem
			return
		}
		s.BucketNames = bucketNames(list.Buckets)
	})
}

func (v *ViewModel) Create(ctx context.Context, in NewKey) bool {
	if v.state.Get().Secret != nil {
		return false
	}
	v.beginWork()
	if problem := precheck(in.Name, in.Scope, v.now()); problem != nil {
		v.endWork(func(s *State) { s.FormProblem = problem })
		return false
	}
	created, err := v.client.CreateAccessKey(ctx, adminv1.CreateAccessKeyRequest{
		Name:  trimmed(in.Name),
		Scope: in.Scope.Wire(),
	})
	if err != nil {
		v.failWork(ctx, err)
		return false
	}
	secret := v.secretOf(ctx, created)
	v.endWork(func(s *State) { s.Secret = &secret })
	v.Refresh(ctx)
	return true
}

func (v *ViewModel) Update(ctx context.Context, id string, in KeyEdit) bool {
	v.beginWork()
	if problem := precheck(in.Name, in.Scope, v.now()); problem != nil {
		v.endWork(func(s *State) { s.FormProblem = problem })
		return false
	}
	req, changed := v.changes(id, in)
	if !changed {
		v.endWork(nil)
		return true
	}
	if _, err := v.client.UpdateAccessKey(ctx, id, req); err != nil {
		v.failWork(ctx, err)
		return false
	}
	v.endWork(nil)
	v.Refresh(ctx)
	return true
}

func (v *ViewModel) Revoke(ctx context.Context, id string) bool {
	v.beginWork()
	if _, err := v.client.RevokeAccessKey(ctx, id); err != nil {
		v.failWork(ctx, err)
		return false
	}
	v.endWork(nil)
	v.Refresh(ctx)
	return true
}

func (v *ViewModel) Done() {
	v.update(func(s *State) { s.Secret = nil })
}

func (v *ViewModel) DismissProblem() {
	v.update(func(s *State) { s.Problem = nil })
}

func (v *ViewModel) ClearFormProblem() {
	v.update(func(s *State) { s.FormProblem = nil })
}

func (v *ViewModel) secretOf(ctx context.Context, created adminv1.AccessKeyCreated) Secret {
	secret := Secret{
		AccessKeyID:     created.AccessKey.AccessKeyID,
		SecretAccessKey: created.SecretAccessKey,
		S3Endpoint:      created.S3Endpoint,
		Region:          created.S3Region,
	}
	if secret.S3Endpoint != "" && secret.Region != "" {
		return secret
	}
	status, err := v.client.Status(ctx)
	if err != nil {
		return secret
	}
	if secret.S3Endpoint == "" {
		secret.S3Endpoint = status.Endpoints.S3PublicURL
	}
	if secret.Region == "" {
		secret.Region = status.Endpoints.S3Region
	}
	return secret
}

func (v *ViewModel) changes(id string, in KeyEdit) (adminv1.UpdateAccessKeyRequest, bool) {
	var req adminv1.UpdateAccessKeyRequest
	current, known := v.find(id)
	name, scope := trimmed(in.Name), in.Scope.Wire()
	changed := false
	if !known || current.Name != name {
		req.Name, changed = &name, true
	}
	if !known || !sameScope(current.Scope, scope) {
		req.Scope, changed = &scope, true
	}
	return req, changed
}

func (v *ViewModel) find(id string) (adminv1.AccessKey, bool) {
	for _, k := range v.state.Get().Keys {
		if k.AccessKeyID == id {
			return k, true
		}
	}
	return adminv1.AccessKey{}, false
}

func (v *ViewModel) beginWork() {
	v.update(func(s *State) {
		s.Working = true
		s.Problem = nil
		s.FormProblem = nil
	})
}

func (v *ViewModel) endWork(mutate func(*State)) {
	v.update(func(s *State) {
		s.Working = false
		if mutate != nil {
			mutate(s)
		}
	})
}

func (v *ViewModel) failWork(ctx context.Context, err error) {
	problem := v.describe(err)
	if problem == nil {
		v.endWork(nil)
		return
	}
	v.endWork(func(s *State) {
		if problem.Presentation == model.PresentInline {
			s.FormProblem = problem
			return
		}
		s.Problem = problem
	})
	if problem.Code == adminv1.CodeAccessKeyNotFound {
		v.Refresh(ctx)
		v.update(func(s *State) {
			if s.Problem == nil {
				s.Problem = problem
			}
		})
	}
}

func (v *ViewModel) describe(err error) *model.Problem {
	if err == nil {
		return nil
	}
	p := model.Describe(err, v.now())
	if p.Title == "" && p.Code == "" {
		return nil
	}
	return &p
}

func (v *ViewModel) update(mutate func(*State)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := v.state.Get()
	mutate(&s)
	v.state.Set(s)
}

func bucketNames(buckets []adminv1.Bucket) []string {
	names := make([]string, len(buckets))
	for i, b := range buckets {
		names[i] = b.Name
	}
	slices.Sort(names)
	return names
}
