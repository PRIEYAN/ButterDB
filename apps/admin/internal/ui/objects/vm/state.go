package vm

import (
	"sort"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type RowKind int

const (
	RowFolder RowKind = iota
	RowObject
	RowLoadMore
)

type Row struct {
	Kind     RowKind
	Name     string
	Key      string
	Object   adminv1.ObjectSummary
	Selected bool
}

type Crumb struct {
	Label  string
	Prefix string
}

type Details struct {
	Key     string
	Loading bool
	Meta    *adminv1.ObjectMeta
	Problem *model.Problem
}

type State struct {
	Loading         bool
	LoadingMore     bool
	Problem         *model.Problem
	Buckets         []string
	Bucket          string
	Prefix          string
	Filter          string
	Breadcrumbs     []Crumb
	Rows            []Row
	HasMore         bool
	SelectedObjects []string
	SelectedFolders []string
	Limits          adminv1.Limits
	Details         Details
}

func Breadcrumbs(bucket, prefix string) []Crumb {
	crumbs := []Crumb{{Label: bucket, Prefix: ""}}
	trimmed := strings.TrimSuffix(prefix, Delimiter)
	if trimmed == "" {
		return crumbs
	}
	acc := ""
	for _, segment := range strings.Split(trimmed, Delimiter) {
		acc += segment + Delimiter
		crumbs = append(crumbs, Crumb{Label: segment, Prefix: acc})
	}
	return crumbs
}

func ParentPrefix(prefix string) string {
	trimmed := strings.TrimSuffix(prefix, Delimiter)
	cut := strings.LastIndex(trimmed, Delimiter)
	if cut < 0 {
		return ""
	}
	return trimmed[:cut+1]
}

func (v *ViewModel) mutate(fn func()) {
	v.mu.Lock()
	defer v.mu.Unlock()
	fn()
	v.state.Set(v.snapshotLocked())
}

func (v *ViewModel) mutateIf(generation int, fn func()) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.generation != generation {
		return false
	}
	fn()
	v.state.Set(v.snapshotLocked())
	return true
}

func (v *ViewModel) snapshotLocked() State {
	return State{
		Loading:         v.loading,
		LoadingMore:     v.loadingMore,
		Problem:         v.problem,
		Buckets:         append([]string(nil), v.buckets...),
		Bucket:          v.bucket,
		Prefix:          v.prefix,
		Filter:          v.filter,
		Breadcrumbs:     Breadcrumbs(v.bucket, v.prefix),
		Rows:            v.rowsLocked(),
		HasMore:         v.cursor != "",
		SelectedObjects: sortedKeys(v.selObjects),
		SelectedFolders: sortedKeys(v.selFolders),
		Limits:          v.limits,
		Details:         v.details,
	}
}

func (v *ViewModel) rowsLocked() []Row {
	rows := make([]Row, 0, len(v.folders)+len(v.objects)+1)
	for _, folder := range v.folders {
		_, selected := v.selFolders[folder]
		rows = append(rows, Row{Kind: RowFolder, Name: relativeName(folder, v.prefix), Key: folder, Selected: selected})
	}
	for _, object := range v.objects {
		_, selected := v.selObjects[object.Key]
		rows = append(rows, Row{Kind: RowObject, Name: relativeName(object.Key, v.prefix), Key: object.Key, Object: object, Selected: selected})
	}
	if v.cursor != "" {
		rows = append(rows, Row{Kind: RowLoadMore, Name: "Load more"})
	}
	return rows
}

func relativeName(key, prefix string) string {
	name := strings.TrimPrefix(key, prefix)
	if name == "" {
		return key
	}
	return name
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
