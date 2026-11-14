package objectrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

func TestPrefixEnd(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
		ok     bool
	}{
		{"slash prefix", "2026/", "20260", true},
		{"empty prefix has no bound", "", "", false},
		{"single byte", "a", "b", true},
		{"trailing 0xff dropped", "a\xff", "b", true},
		{"several trailing 0xff dropped", "ab\xff\xff", "ac", true},
		{"only 0xff has no bound", "\xff\xff", "", false},
		{"ends in a continuation byte", "ÿ", "\xc3\xc0", true},
		{"two byte rune", "é", "ê", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := PrefixEnd(tt.prefix)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("PrefixEnd(%q) = %q, %v; want %q, %v", tt.prefix, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestListByteOrder(t *testing.T) {
	repo, db := newRepo(t)
	putKeys(t, db, "é", "z", "a0", "a/", "Z", "a")
	page, err := repo.List(context.Background(), testChannel, object.ListQuery{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"Z", "a", "a/", "a0", "z", "é"}
	if got := keysOf(page); !slices.Equal(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	if page.NextToken != "" {
		t.Fatalf("next token = %q, want none", page.NextToken)
	}
}

func TestListPrefixAndStartAfter(t *testing.T) {
	repo, db := newRepo(t)
	putKeys(t, db, "2025/a", "2026/a", "2026/b", "2026/c", "20260", "2027/a", "ÿ", "ÿa", "ÿĀ")
	tests := []struct {
		name  string
		query object.ListQuery
		want  []string
	}{
		{"prefix only", object.ListQuery{Prefix: "2026/", Limit: 100}, []string{"2026/a", "2026/b", "2026/c"}},
		{"prefix with start after", object.ListQuery{Prefix: "2026/", StartAfter: "2026/a", Limit: 100}, []string{"2026/b", "2026/c"}},
		{"start after before prefix", object.ListQuery{Prefix: "2026/", StartAfter: "2025/z", Limit: 100}, []string{"2026/a", "2026/b", "2026/c"}},
		{"start after past prefix", object.ListQuery{Prefix: "2026/", StartAfter: "2026/c", Limit: 100}, nil},
		{"start after absent key", object.ListQuery{StartAfter: "2026/b0", Limit: 100}, []string{"2026/c", "20260", "2027/a", "ÿ", "ÿa", "ÿĀ"}},
		{"prefix ending in continuation byte", object.ListQuery{Prefix: "ÿ", Limit: 100}, []string{"ÿ", "ÿa", "ÿĀ"}},
		{"zero limit", object.ListQuery{Limit: 0}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := repo.List(context.Background(), testChannel, tt.query)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got := keysOf(page); !slices.Equal(got, tt.want) {
				t.Fatalf("keys = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListScopesChannel(t *testing.T) {
	repo, db := newRepo(t)
	putAll(t, db, sampleMeta(testChannel, "mine", 5), sampleMeta(testChannel+1, "theirs", 6))
	page, err := repo.List(context.Background(), testChannel, object.ListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := keysOf(page); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("keys = %q", got)
	}
}

func TestListDelimiter(t *testing.T) {
	repo, db := newRepo(t)
	putKeys(t, db, "a", "b/1", "b/2", "c/d/1", "c/e", "d")
	tests := []struct {
		name         string
		query        object.ListQuery
		wantKeys     []string
		wantPrefixes []string
	}{
		{"root rollup", object.ListQuery{Delimiter: "/", Limit: 100}, []string{"a", "d"}, []string{"b/", "c/"}},
		{"nested prefix", object.ListQuery{Prefix: "c/", Delimiter: "/", Limit: 100}, []string{"c/e"}, []string{"c/d/"}},
		{"multi character delimiter", object.ListQuery{Prefix: "c/", Delimiter: "d/", Limit: 100}, []string{"c/e"}, []string{"c/d/"}},
		{"delimiter absent from keys", object.ListQuery{Delimiter: "#", Limit: 100}, []string{"a", "b/1", "b/2", "c/d/1", "c/e", "d"}, nil},
		{"counts keys and prefixes together", object.ListQuery{Delimiter: "/", Limit: 2}, []string{"a"}, []string{"b/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := repo.List(context.Background(), testChannel, tt.query)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got := keysOf(page); !slices.Equal(got, tt.wantKeys) {
				t.Fatalf("keys = %q, want %q", got, tt.wantKeys)
			}
			if !slices.Equal(page.CommonPrefixes, tt.wantPrefixes) {
				t.Fatalf("prefixes = %q, want %q", page.CommonPrefixes, tt.wantPrefixes)
			}
		})
	}
}

type countingQuerier struct {
	inner   querier
	queries int
}

func (c *countingQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.queries++
	return c.inner.QueryContext(ctx, query, args...)
}

func TestListDelimiterSkipsByIndex(t *testing.T) {
	_, db := newRepo(t)
	const rolledUp = 10000
	metas := make([]object.ObjectMeta, 0, rolledUp+3)
	for i := range rolledUp {
		metas = append(metas, sampleMeta(testChannel, fmt.Sprintf("p/a/%05d", i), int64(i+2)))
	}
	metas = append(metas,
		sampleMeta(testChannel, "p/b/x", rolledUp+2),
		sampleMeta(testChannel, "p/c", rolledUp+3),
		sampleMeta(testChannel, "q", rolledUp+4),
	)
	putAll(t, db, metas...)

	var page object.ListPage
	counter := &countingQuerier{}
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		counter.inner = tx
		var err error
		page, err = listPage(context.Background(), counter, testChannel, object.ListQuery{Prefix: "p/", Delimiter: "/", Limit: 1000})
		return err
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"p/a/", "p/b/"}; !slices.Equal(page.CommonPrefixes, want) {
		t.Fatalf("prefixes = %q, want %q", page.CommonPrefixes, want)
	}
	if got := keysOf(page); !slices.Equal(got, []string{"p/c"}) {
		t.Fatalf("keys = %q", got)
	}
	if limit := len(page.CommonPrefixes) + 1; counter.queries > limit {
		t.Fatalf("issued %d queries, want at most %d (one seek per common prefix)", counter.queries, limit)
	}
}

func TestContinuationKinds(t *testing.T) {
	repo, db := newRepo(t)
	putKeys(t, db, "a", "b/1", "b/2", "b/3", "c", "d/1", "e")

	afterKey, err := repo.List(context.Background(), testChannel, object.ListQuery{Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := keysOf(afterKey); !slices.Equal(got, []string{"a", "b/1"}) || afterKey.NextToken == "" {
		t.Fatalf("first page keys = %q token = %q", got, afterKey.NextToken)
	}
	point, err := parseToken(afterKey.NextToken)
	if err != nil || point.isPrefix || point.value != "b/1" {
		t.Fatalf("token = %+v, %v; want after key b/1", point, err)
	}

	resumed, err := repo.List(context.Background(), testChannel, object.ListQuery{ContinuationToken: afterKey.NextToken, Limit: 2})
	if err != nil {
		t.Fatalf("resume after key: %v", err)
	}
	if got := keysOf(resumed); !slices.Equal(got, []string{"b/2", "b/3"}) {
		t.Fatalf("resume after key = %q", got)
	}

	rolled, err := repo.List(context.Background(), testChannel, object.ListQuery{Delimiter: "/", Limit: 2})
	if err != nil {
		t.Fatalf("list rolled: %v", err)
	}
	if got := keysOf(rolled); !slices.Equal(got, []string{"a"}) || !slices.Equal(rolled.CommonPrefixes, []string{"b/"}) {
		t.Fatalf("rolled page = %q %q", got, rolled.CommonPrefixes)
	}
	point, err = parseToken(rolled.NextToken)
	if err != nil || !point.isPrefix || point.value != "b/" {
		t.Fatalf("token = %+v, %v; want after prefix b/", point, err)
	}
	afterPrefix, err := repo.List(context.Background(), testChannel, object.ListQuery{Delimiter: "/", ContinuationToken: rolled.NextToken, Limit: 10})
	if err != nil {
		t.Fatalf("resume after prefix: %v", err)
	}
	if got := keysOf(afterPrefix); !slices.Equal(got, []string{"c", "e"}) || !slices.Equal(afterPrefix.CommonPrefixes, []string{"d/"}) {
		t.Fatalf("resume after prefix = %q %q", got, afterPrefix.CommonPrefixes)
	}
}

func TestContinuationWalksEveryKeyOnce(t *testing.T) {
	repo, db := newRepo(t)
	var keys []string
	for i := range 40 {
		keys = append(keys, fmt.Sprintf("d%d/k%d", i%5, i), fmt.Sprintf("top%02d", i))
	}
	putKeys(t, db, keys...)
	for _, delimiter := range []string{"", "/"} {
		for _, limit := range []int{1, 3, 7, 1000} {
			t.Run(fmt.Sprintf("delimiter %q limit %d", delimiter, limit), func(t *testing.T) {
				var gotKeys, gotPrefixes []string
				token := ""
				for range 200 {
					page, err := repo.List(context.Background(), testChannel, object.ListQuery{Delimiter: delimiter, ContinuationToken: token, Limit: limit})
					if err != nil {
						t.Fatalf("list: %v", err)
					}
					gotKeys = append(gotKeys, keysOf(page)...)
					gotPrefixes = append(gotPrefixes, page.CommonPrefixes...)
					if token = page.NextToken; token == "" {
						break
					}
				}
				if token != "" {
					t.Fatal("listing did not finish")
				}
				if delimiter == "" {
					if len(gotKeys) != len(keys) || !slices.IsSorted(gotKeys) {
						t.Fatalf("walked %d keys, want %d sorted", len(gotKeys), len(keys))
					}
					return
				}
				if len(gotKeys) != 40 || len(gotPrefixes) != 5 {
					t.Fatalf("walked %d keys and %d prefixes, want 40 and 5", len(gotKeys), len(gotPrefixes))
				}
			})
		}
	}
}

func TestContinuationTokenRejected(t *testing.T) {
	repo, _ := newRepo(t)
	tests := []struct {
		name  string
		token string
	}{
		{"not base64", "!!!"},
		{"empty payload", "AQA"},
		{"wrong version", "AgBh"},
		{"unknown kind", "AQJh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := repo.List(context.Background(), testChannel, object.ListQuery{ContinuationToken: tt.token, Limit: 10})
			if !errors.Is(err, ErrInvalidContinuationToken) {
				t.Fatalf("error = %v, want ErrInvalidContinuationToken", err)
			}
		})
	}
}

func TestContinuationTokenIgnoresStartAfter(t *testing.T) {
	repo, db := newRepo(t)
	putKeys(t, db, "a", "b", "c", "d")
	token := resumePoint{value: "a"}.token()
	page, err := repo.List(context.Background(), testChannel, object.ListQuery{ContinuationToken: token, StartAfter: "c", Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := keysOf(page); !slices.Equal(got, []string{"b", "c", "d"}) {
		t.Fatalf("keys = %q", got)
	}
}

func TestContinuationAfterUnboundedPrefix(t *testing.T) {
	repo, db := newRepo(t)
	putKeys(t, db, "a", "\xff/1", "\xff/2")
	page, err := repo.List(context.Background(), testChannel, object.ListQuery{Delimiter: "/", Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := keysOf(page); !slices.Equal(got, []string{"a"}) || page.NextToken == "" {
		t.Fatalf("page = %q token %q", got, page.NextToken)
	}
	next, err := repo.List(context.Background(), testChannel, object.ListQuery{Delimiter: "/", ContinuationToken: page.NextToken, Limit: 1})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !slices.Equal(next.CommonPrefixes, []string{"\xff/"}) || next.NextToken != "" {
		t.Fatalf("prefixes = %q token %q", next.CommonPrefixes, next.NextToken)
	}
	last, err := repo.List(context.Background(), testChannel, object.ListQuery{ContinuationToken: resumePoint{value: "\xff/", isPrefix: true}.token(), Limit: 10})
	if err != nil {
		t.Fatalf("resume after unbounded prefix: %v", err)
	}
	if len(last.Objects) != 0 || last.NextToken != "" {
		t.Fatalf("page after the last prefix = %+v", last)
	}
}

func TestListCancelled(t *testing.T) {
	repo, db := newRepo(t)
	putKeys(t, db, "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.List(ctx, testChannel, object.ListQuery{Limit: 10}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestListQueryPlan(t *testing.T) {
	_, db := newRepo(t)
	tests := []struct {
		name      string
		statement string
		args      []any
	}{
		{"bounded", listBounded, []any{testChannel, "a", "b", 10}},
		{"unbounded", listUnbounded, []any{testChannel, "a", 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := strings.Join(explain(t, db, tt.statement, tt.args...), "\n")
			if !strings.Contains(plan, "SEARCH objects") || !strings.Contains(plan, "sqlite_autoindex_objects_1") {
				t.Fatalf("plan does not search the primary key index:\n%s", plan)
			}
			if strings.Contains(plan, "SCAN") || strings.Contains(plan, "TEMP B-TREE") {
				t.Fatalf("plan scans or sorts:\n%s", plan)
			}
		})
	}
}
