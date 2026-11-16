package vm

import (
	"context"
	"reflect"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func TestListFoldersFirst(t *testing.T) {
	c := newFakeClient()
	c.pages[""] = adminv1.ObjectList{
		Objects:        []adminv1.ObjectSummary{{Key: "a/cat.jpg"}, {Key: "a/dog.jpg"}},
		CommonPrefixes: []string{"a/raw/", "a/2026/"},
	}
	v, _ := newTestVM(t, c)
	v.Open(context.Background(), "photos", "a/")
	rows := v.State().Get().Rows
	var got []string
	for _, row := range rows {
		got = append(got, row.Name)
	}
	want := []string{"2026/", "raw/", "cat.jpg", "dog.jpg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if rows[0].Kind != RowFolder || rows[2].Kind != RowObject {
		t.Fatalf("row kinds = %v %v", rows[0].Kind, rows[2].Kind)
	}
}

func TestLoadMoreAppendsPage(t *testing.T) {
	c := newFakeClient()
	c.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "one"}}, NextCursor: "c1"}
	c.pages["c1"] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "two"}}, CommonPrefixes: []string{"dir/"}}
	v, _ := newTestVM(t, c)
	v.Open(context.Background(), "photos", "")
	first := v.State().Get()
	if !first.HasMore || first.Rows[len(first.Rows)-1].Kind != RowLoadMore {
		t.Fatalf("first page lacks the load more row: %+v", first.Rows)
	}
	v.LoadMore(context.Background())
	got := v.State().Get()
	if got.HasMore {
		t.Fatal("HasMore after the last page")
	}
	var names []string
	for _, row := range got.Rows {
		names = append(names, row.Name)
	}
	if want := []string{"dir/", "one", "two"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("rows = %v, want %v", names, want)
	}
	if last := c.listCalls[len(c.listCalls)-1]; last.Cursor != "c1" || last.Delimiter != "/" || last.Limit != ListPageSize {
		t.Fatalf("load more options = %+v", last)
	}
}

func TestBreadcrumbSegments(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		want   []Crumb
	}{
		{"root", "", []Crumb{{Label: "photos", Prefix: ""}}},
		{"nested", "2026/11/", []Crumb{{Label: "photos", Prefix: ""}, {Label: "2026", Prefix: "2026/"}, {Label: "11", Prefix: "2026/11/"}}},
		{"no trailing slash", "a", []Crumb{{Label: "photos", Prefix: ""}, {Label: "a", Prefix: "a/"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Breadcrumbs("photos", tc.prefix); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Breadcrumbs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParentPrefix(t *testing.T) {
	cases := map[string]string{"": "", "a/": "", "a/b/": "a/", "a/b/c/": "a/b/"}
	for in, want := range cases {
		if got := ParentPrefix(in); got != want {
			t.Errorf("ParentPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpGoesToParentAndFilterAddsToPrefix(t *testing.T) {
	c := newFakeClient()
	v, _ := newTestVM(t, c)
	ctx := context.Background()
	v.Open(ctx, "photos", "a/b/")
	v.Up(ctx)
	if _, prefix := v.Location(); prefix != "a/" {
		t.Fatalf("prefix after Up = %q", prefix)
	}
	v.ApplyFilter(ctx, "ca")
	last := c.listCalls[len(c.listCalls)-1]
	if last.Prefix != "a/ca" {
		t.Fatalf("filter listing prefix = %q", last.Prefix)
	}
	v.OpenFolder(ctx, "a/cats/")
	if got := v.State().Get(); got.Filter != "" {
		t.Fatalf("filter kept after navigating: %q", got.Filter)
	}
}

func TestSelectionToggleAndSelectAll(t *testing.T) {
	c := newFakeClient()
	c.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "x"}, {Key: "y"}}, CommonPrefixes: []string{"d/"}}
	v, _ := newTestVM(t, c)
	v.Open(context.Background(), "b", "")
	v.ToggleObject("x")
	if got := v.State().Get().SelectedObjects; !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("selected = %v", got)
	}
	v.SelectAll()
	st := v.State().Get()
	if len(st.SelectedObjects) != 2 || len(st.SelectedFolders) != 1 {
		t.Fatalf("select all = %v %v", st.SelectedObjects, st.SelectedFolders)
	}
	v.ClearSelection()
	if st := v.State().Get(); len(st.SelectedObjects)+len(st.SelectedFolders) != 0 {
		t.Fatal("selection survived ClearSelection")
	}
}

func TestListErrorIsDescribed(t *testing.T) {
	c := newFakeClient()
	c.listErr = apiErr(404, adminv1.CodeBucketNotFound)
	v, _ := newTestVM(t, c)
	v.Open(context.Background(), "gone", "")
	st := v.State().Get()
	if st.Problem == nil || st.Problem.Code != adminv1.CodeBucketNotFound || st.Loading {
		t.Fatalf("state = %+v", st)
	}
}
