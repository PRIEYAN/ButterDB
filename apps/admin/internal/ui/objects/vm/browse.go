package vm

import (
	"context"
	"sort"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func (v *ViewModel) LoadBuckets(ctx context.Context) {
	list, err := v.client.ListBuckets(ctx)
	if err != nil {
		v.mutate(func() { v.problem = v.problemFor(err) })
		return
	}
	names := make([]string, 0, len(list.Buckets))
	for _, bucket := range list.Buckets {
		names = append(names, bucket.Name)
	}
	sort.Strings(names)
	v.mutate(func() { v.buckets = names })
}

func (v *ViewModel) Refresh(ctx context.Context) {
	v.LoadBuckets(ctx)
	v.refreshLimits(ctx)
	v.mu.Lock()
	bucket, prefix := v.bucket, v.prefix
	if bucket == "" && len(v.buckets) > 0 {
		bucket = v.buckets[0]
	}
	v.mu.Unlock()
	if bucket == "" {
		return
	}
	v.openKeepingFilter(ctx, bucket, prefix)
}

func (v *ViewModel) Open(ctx context.Context, bucket, prefix string) {
	v.mu.Lock()
	changed := bucket != v.bucket || prefix != v.prefix
	v.mu.Unlock()
	if changed {
		v.mutate(func() { v.filter = "" })
	}
	v.openKeepingFilter(ctx, bucket, prefix)
}

func (v *ViewModel) openKeepingFilter(ctx context.Context, bucket, prefix string) {
	v.mutate(func() {
		v.generation++
		if bucket != v.bucket || prefix != v.prefix {
			v.focusGen++
			v.selObjects = make(map[string]struct{})
			v.selFolders = make(map[string]struct{})
			v.details = Details{}
		}
		v.bucket = bucket
		v.prefix = prefix
		v.loading = true
		v.loadingMore = false
		v.problem = nil
	})
	v.listFirstPage(ctx)
}

func (v *ViewModel) SelectBucket(ctx context.Context, bucket string) {
	v.Open(ctx, bucket, "")
}

func (v *ViewModel) OpenFolder(ctx context.Context, folder string) {
	v.mu.Lock()
	bucket := v.bucket
	v.mu.Unlock()
	v.Open(ctx, bucket, folder)
}

func (v *ViewModel) Up(ctx context.Context) {
	v.mu.Lock()
	bucket, prefix := v.bucket, v.prefix
	v.mu.Unlock()
	if bucket == "" || prefix == "" {
		return
	}
	v.Open(ctx, bucket, ParentPrefix(prefix))
}

func (v *ViewModel) ApplyFilter(ctx context.Context, filter string) {
	v.mu.Lock()
	bucket, prefix := v.bucket, v.prefix
	v.mu.Unlock()
	if bucket == "" {
		return
	}
	v.mutate(func() { v.filter = filter })
	v.openKeepingFilter(ctx, bucket, prefix)
}

func (v *ViewModel) listFirstPage(ctx context.Context) {
	v.mu.Lock()
	generation := v.generation
	bucket, listPrefix := v.bucket, v.prefix+v.filter
	v.mu.Unlock()
	list, err := v.client.ListObjects(ctx, bucket, adminv1.ListObjectsOptions{
		Prefix:    listPrefix,
		Delimiter: Delimiter,
		Limit:     ListPageSize,
	})
	if err != nil {
		v.mutateIf(generation, func() {
			v.loading = false
			v.problem = v.problemFor(err)
			v.folders, v.objects, v.cursor = nil, nil, ""
		})
		return
	}
	v.mutateIf(generation, func() {
		v.loading = false
		v.folders = nil
		v.objects = nil
		v.cursor = ""
		v.appendPageLocked(list)
	})
}

func (v *ViewModel) LoadMore(ctx context.Context) {
	v.mu.Lock()
	generation := v.generation
	bucket, listPrefix, cursor := v.bucket, v.prefix+v.filter, v.cursor
	busy := v.loadingMore || v.loading
	v.mu.Unlock()
	if cursor == "" || bucket == "" || busy {
		return
	}
	v.mutateIf(generation, func() { v.loadingMore = true })
	list, err := v.client.ListObjects(ctx, bucket, adminv1.ListObjectsOptions{
		Prefix:    listPrefix,
		Delimiter: Delimiter,
		Cursor:    cursor,
		Limit:     ListPageSize,
	})
	if err != nil {
		v.mutateIf(generation, func() {
			v.loadingMore = false
			v.problem = v.problemFor(err)
		})
		return
	}
	v.mutateIf(generation, func() {
		v.loadingMore = false
		v.appendPageLocked(list)
	})
}

func (v *ViewModel) appendPageLocked(list adminv1.ObjectList) {
	known := make(map[string]struct{}, len(v.folders))
	for _, folder := range v.folders {
		known[folder] = struct{}{}
	}
	for _, folder := range list.CommonPrefixes {
		if _, dup := known[folder]; dup {
			continue
		}
		known[folder] = struct{}{}
		v.folders = append(v.folders, folder)
	}
	sort.Strings(v.folders)
	v.objects = append(v.objects, list.Objects...)
	v.cursor = list.NextCursor
}

func (v *ViewModel) ToggleObject(key string) {
	v.mutate(func() { toggle(v.selObjects, key) })
}

func (v *ViewModel) ToggleFolder(prefix string) {
	v.mutate(func() { toggle(v.selFolders, prefix) })
}

func (v *ViewModel) SelectAll() {
	v.mutate(func() {
		for _, folder := range v.folders {
			v.selFolders[folder] = struct{}{}
		}
		for _, object := range v.objects {
			v.selObjects[object.Key] = struct{}{}
		}
	})
}

func (v *ViewModel) ClearSelection() {
	v.mutate(func() {
		v.selObjects = make(map[string]struct{})
		v.selFolders = make(map[string]struct{})
	})
}

func toggle(set map[string]struct{}, key string) {
	if _, ok := set[key]; ok {
		delete(set, key)
		return
	}
	set[key] = struct{}{}
}

func (v *ViewModel) Location() (bucket, prefix string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.bucket, v.prefix
}
