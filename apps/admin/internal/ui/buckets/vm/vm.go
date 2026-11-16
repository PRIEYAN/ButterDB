package vm

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	PolicyPrivate    = "private"
	PolicyPublicRead = "public-read"

	EncryptionNone = "none"
	EncryptionTDE1 = "tde1"

	MaxCacheMaxAgeSeconds     = 31536000
	DefaultCacheMaxAgeSeconds = 3600

	FieldName        = "name"
	FieldCacheMaxAge = "cache_max_age_seconds"

	detailObjectCount = "object_count"
	unknownObjects    = -1
)

type Client interface {
	ListBuckets(ctx context.Context) (adminv1.BucketList, error)
	CreateBucket(ctx context.Context, req adminv1.CreateBucketRequest) (adminv1.Bucket, error)
	GetBucket(ctx context.Context, name string) (adminv1.Bucket, error)
	UpdateBucket(ctx context.Context, name string, req adminv1.UpdateBucketRequest) (adminv1.Bucket, error)
	DeleteBucket(ctx context.Context, name string) error
}

type NotEmpty struct {
	Bucket      string
	ObjectCount int64
	Problem     model.Problem
}

type State struct {
	Loading        bool
	Working        bool
	Problem        *model.Problem
	FormProblem    *model.Problem
	NotEmpty       *NotEmpty
	Buckets        []adminv1.Bucket
	SortColumn     SortColumn
	SortDescending bool
}

type NewBucket struct {
	Name               string
	PublicRead         bool
	Encrypted          bool
	CacheMaxAgeSeconds int
}

type Settings struct {
	PublicRead         bool
	Encrypted          bool
	CacheMaxAgeSeconds int
}

type ViewModel struct {
	client Client
	now    func() time.Time
	mu     sync.Mutex
	state  *model.Observable[State]
}

func New(c Client, now func() time.Time) *ViewModel {
	return &ViewModel{
		client: c,
		now:    now,
		state:  model.NewObservable(State{SortColumn: ColumnName}),
	}
}

func DefaultNewBucket() NewBucket {
	return NewBucket{CacheMaxAgeSeconds: DefaultCacheMaxAgeSeconds}
}

func SettingsOf(b adminv1.Bucket) Settings {
	return Settings{
		PublicRead:         b.Policy == PolicyPublicRead,
		Encrypted:          b.EncryptionDefault == EncryptionTDE1,
		CacheMaxAgeSeconds: b.CacheMaxAgeSeconds,
	}
}

func (v *ViewModel) State() *model.Observable[State] {
	return v.state
}

func (v *ViewModel) Refresh(ctx context.Context) {
	v.update(func(s *State) {
		s.Loading = true
		s.Problem = nil
	})
	list, err := v.client.ListBuckets(ctx)
	problem := v.describe(err)
	v.update(func(s *State) {
		s.Loading = false
		if err != nil {
			s.Problem = problem
			return
		}
		s.Buckets = sortedBuckets(list.Buckets, s.SortColumn, s.SortDescending)
	})
}

func (v *ViewModel) Sort(column SortColumn) {
	v.update(func(s *State) {
		if s.SortColumn == column {
			s.SortDescending = !s.SortDescending
		} else {
			s.SortColumn, s.SortDescending = column, false
		}
		s.Buckets = sortedBuckets(s.Buckets, s.SortColumn, s.SortDescending)
	})
}

func (v *ViewModel) Create(ctx context.Context, in NewBucket) bool {
	v.beginWork()
	if problem := precheckNew(in, v.now()); problem != nil {
		v.endWork(func(s *State) { s.FormProblem = problem })
		return false
	}
	cache := in.CacheMaxAgeSeconds
	_, err := v.client.CreateBucket(ctx, adminv1.CreateBucketRequest{
		Name:               in.Name,
		Policy:             policyOf(in.PublicRead),
		EncryptionDefault:  encryptionOf(in.Encrypted),
		CacheMaxAgeSeconds: &cache,
	})
	if err != nil {
		v.failWork(ctx, in.Name, err)
		return false
	}
	v.endWork(nil)
	v.Refresh(ctx)
	return true
}

func (v *ViewModel) Update(ctx context.Context, name string, in Settings) bool {
	v.beginWork()
	if problem := precheckCacheMaxAge(in.CacheMaxAgeSeconds, v.now()); problem != nil {
		v.endWork(func(s *State) { s.FormProblem = problem })
		return false
	}
	req, changed := v.changes(name, in)
	if !changed {
		v.endWork(nil)
		return true
	}
	if _, err := v.client.UpdateBucket(ctx, name, req); err != nil {
		v.failWork(ctx, name, err)
		return false
	}
	v.endWork(nil)
	v.Refresh(ctx)
	return true
}

func (v *ViewModel) Delete(ctx context.Context, name string) bool {
	v.beginWork()
	if err := v.client.DeleteBucket(ctx, name); err != nil {
		v.failWork(ctx, name, err)
		return false
	}
	v.endWork(nil)
	v.Refresh(ctx)
	return true
}

func (v *ViewModel) DismissNotEmpty() {
	v.update(func(s *State) { s.NotEmpty = nil })
}

func (v *ViewModel) DismissProblem() {
	v.update(func(s *State) { s.Problem = nil })
}

func (v *ViewModel) ClearFormProblem() {
	v.update(func(s *State) { s.FormProblem = nil })
}

func (v *ViewModel) changes(name string, in Settings) (adminv1.UpdateBucketRequest, bool) {
	current, known := v.find(name)
	var req adminv1.UpdateBucketRequest
	changed := false
	policy, encryption, cache := policyOf(in.PublicRead), encryptionOf(in.Encrypted), in.CacheMaxAgeSeconds
	if !known || current.Policy != policy {
		req.Policy, changed = &policy, true
	}
	if !known || current.EncryptionDefault != encryption {
		req.EncryptionDefault, changed = &encryption, true
	}
	if !known || current.CacheMaxAgeSeconds != cache {
		req.CacheMaxAgeSeconds, changed = &cache, true
	}
	return req, changed
}

func (v *ViewModel) find(name string) (adminv1.Bucket, bool) {
	for _, b := range v.state.Get().Buckets {
		if b.Name == name {
			return b, true
		}
	}
	return adminv1.Bucket{}, false
}

func (v *ViewModel) beginWork() {
	v.update(func(s *State) {
		s.Working = true
		s.Problem = nil
		s.FormProblem = nil
		s.NotEmpty = nil
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

func (v *ViewModel) failWork(ctx context.Context, name string, err error) {
	problem := v.describe(err)
	if problem == nil {
		v.endWork(nil)
		return
	}
	if problem.Code == adminv1.CodeBucketNotEmpty {
		notEmpty := v.notEmptyOf(name, *problem)
		v.endWork(func(s *State) { s.NotEmpty = &notEmpty })
		return
	}
	v.endWork(func(s *State) {
		if problem.Presentation == model.PresentInline {
			s.FormProblem = problem
			return
		}
		s.Problem = problem
	})
	if problem.Code == adminv1.CodeBucketNotFound {
		v.refreshKeepingProblem(ctx, problem)
	}
}

func (v *ViewModel) refreshKeepingProblem(ctx context.Context, problem *model.Problem) {
	v.Refresh(ctx)
	v.update(func(s *State) {
		if s.Problem == nil {
			s.Problem = problem
		}
	})
}

func (v *ViewModel) notEmptyOf(name string, problem model.Problem) NotEmpty {
	count, ok := objectCountOf(problem)
	if !ok {
		count = unknownObjects
		if known, found := v.find(name); found {
			count = known.ObjectCount
			problem.Title = strconv.FormatInt(count, 10) + " objects remain. Delete them first."
		}
	}
	return NotEmpty{Bucket: name, ObjectCount: count, Problem: problem}
}

func objectCountOf(problem model.Problem) (int64, bool) {
	switch n := problem.Details[detailObjectCount].(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
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

func policyOf(publicRead bool) string {
	if publicRead {
		return PolicyPublicRead
	}
	return PolicyPrivate
}

func encryptionOf(encrypted bool) string {
	if encrypted {
		return EncryptionTDE1
	}
	return EncryptionNone
}
