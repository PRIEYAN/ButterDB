package vm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func keysN(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("k/%05d", i)
	}
	return keys
}

func TestDeleteBatchesOf1000(t *testing.T) {
	c := newFakeClient()
	v, _ := newTestVM(t, c)
	v.Open(context.Background(), "photos", "")
	result := v.Delete(context.Background(), "photos", keysN(2500))
	var sizes []int
	for _, batch := range c.deleted {
		sizes = append(sizes, len(batch))
	}
	if fmt.Sprint(sizes) != "[1000 1000 500]" {
		t.Fatalf("batch sizes = %v", sizes)
	}
	if len(result.Deleted) != 2500 || len(result.Failed) != 0 {
		t.Fatalf("result = %d deleted, %d failed", len(result.Deleted), len(result.Failed))
	}
}

func TestDeleteSingleUsesDeleteObject(t *testing.T) {
	c := newFakeClient()
	v, _ := newTestVM(t, c)
	result := v.Delete(context.Background(), "photos", []string{"only", "only"})
	if len(c.deleteSolo) != 1 || len(c.deleted) != 0 || len(result.Deleted) != 1 {
		t.Fatalf("solo=%v batches=%v result=%+v", c.deleteSolo, c.deleted, result)
	}
}

func TestDeletePartialFailure(t *testing.T) {
	c := newFakeClient()
	c.deleteFn = func(batch []string) (adminv1.DeleteObjectsResult, error) {
		return adminv1.DeleteObjectsResult{
			Deleted: batch[:len(batch)-1],
			Errors:  []adminv1.DeleteObjectError{{Key: batch[len(batch)-1], Code: adminv1.CodeTelegramFloodWait, Message: "wait", Retryable: true}},
		}, nil
	}
	v, _ := newTestVM(t, c)
	result := v.Delete(context.Background(), "photos", []string{"a", "b", "c"})
	if len(result.Deleted) != 2 || len(result.Failed) != 1 {
		t.Fatalf("result = %+v", result)
	}
	failure := result.Failed[0]
	if failure.Key != "c" || failure.Problem.Code != adminv1.CodeTelegramFloodWait || failure.Problem.Title == "" {
		t.Fatalf("failure = %+v", failure)
	}
	if got := result.FailedKeys(); len(got) != 1 || got[0] != "c" {
		t.Fatalf("FailedKeys = %v", got)
	}
}

func TestDeleteWholeRequestFailureMarksRemainingKeys(t *testing.T) {
	c := newFakeClient()
	calls := 0
	c.deleteFn = func(batch []string) (adminv1.DeleteObjectsResult, error) {
		calls++
		if calls == 2 {
			return adminv1.DeleteObjectsResult{}, apiErr(500, adminv1.CodeInternal)
		}
		return adminv1.DeleteObjectsResult{Deleted: batch}, nil
	}
	v, _ := newTestVM(t, c)
	result := v.Delete(context.Background(), "photos", keysN(2500))
	if len(result.Deleted) != 1000 || len(result.Failed) != 1500 || calls != 2 {
		t.Fatalf("deleted=%d failed=%d calls=%d", len(result.Deleted), len(result.Failed), calls)
	}
}

func TestDeleteRelistsAndDropsSelection(t *testing.T) {
	c := newFakeClient()
	c.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "a"}, {Key: "b"}}}
	v, _ := newTestVM(t, c)
	ctx := context.Background()
	v.Open(ctx, "photos", "")
	v.ToggleObject("a")
	v.ToggleObject("b")
	before := len(c.listCalls)
	v.Delete(ctx, "photos", []string{"a", "b"})
	if len(c.listCalls) != before+1 {
		t.Fatalf("listings = %d, want %d", len(c.listCalls), before+1)
	}
	if got := v.State().Get().SelectedObjects; len(got) != 0 {
		t.Fatalf("selection kept: %v", got)
	}
}

func TestDeleteConfirmation(t *testing.T) {
	title, body := DeleteConfirmation(keysN(12), 1)
	if title != "Delete 12 objects?" {
		t.Fatalf("title = %q", title)
	}
	if !strings.Contains(body, "This permanently deletes 12 objects from Telegram. There is no trash and no undo.") {
		t.Fatalf("body = %q", body)
	}
	if strings.Count(body, "k/") != 10 || !strings.Contains(body, "and 2 more") || !strings.Contains(body, "Folders disappear") {
		t.Fatalf("body = %q", body)
	}
	if title, _ := DeleteConfirmation([]string{"x"}, 0); title != "Delete 1 object?" {
		t.Fatalf("singular title = %q", title)
	}
}
