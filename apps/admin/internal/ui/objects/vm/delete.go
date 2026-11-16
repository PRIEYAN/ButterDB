package vm

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	DefaultDeleteBatch = 1000
	confirmKeysListed  = 10
)

type DeleteFailure struct {
	Key     string
	Problem model.Problem
}

type DeleteResult struct {
	Deleted []string
	Failed  []DeleteFailure
}

func (r DeleteResult) FailedKeys() []string {
	keys := make([]string, 0, len(r.Failed))
	for _, f := range r.Failed {
		keys = append(keys, f.Key)
	}
	return keys
}

func (v *ViewModel) deleteBatchSize() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.limitsKnown && v.limits.MaxDeleteBatch > 0 {
		return min(v.limits.MaxDeleteBatch, DefaultDeleteBatch)
	}
	return DefaultDeleteBatch
}

func (v *ViewModel) Delete(ctx context.Context, bucket string, keys []string) DeleteResult {
	keys = uniqueKeys(keys)
	var result DeleteResult
	switch len(keys) {
	case 0:
		return result
	case 1:
		v.deleteSingle(ctx, bucket, keys[0], &result)
	default:
		v.deleteBatches(ctx, bucket, keys, &result)
	}
	v.afterDelete(ctx, bucket, result)
	return result
}

func (v *ViewModel) deleteSingle(ctx context.Context, bucket, key string, result *DeleteResult) {
	err := v.client.DeleteObject(ctx, bucket, key)
	if err != nil {
		result.Failed = append(result.Failed, v.failure(key, err))
		return
	}
	result.Deleted = append(result.Deleted, key)
}

func (v *ViewModel) deleteBatches(ctx context.Context, bucket string, keys []string, result *DeleteResult) {
	size := v.deleteBatchSize()
	for start := 0; start < len(keys); start += size {
		batch := keys[start:min(start+size, len(keys))]
		res, err := v.client.DeleteObjects(ctx, bucket, adminv1.DeleteObjectsRequest{Keys: batch})
		if err != nil {
			for _, key := range keys[start:] {
				result.Failed = append(result.Failed, v.failure(key, err))
			}
			return
		}
		result.Deleted = append(result.Deleted, res.Deleted...)
		for _, item := range res.Errors {
			apiErr := &adminv1.APIError{Body: adminv1.ErrorBody{Code: item.Code, Message: item.Message, Retryable: item.Retryable}}
			result.Failed = append(result.Failed, v.failure(item.Key, apiErr))
		}
	}
}

func (v *ViewModel) failure(key string, err error) DeleteFailure {
	p := model.Describe(err, v.now())
	return DeleteFailure{Key: key, Problem: p}
}

func (v *ViewModel) afterDelete(ctx context.Context, bucket string, result DeleteResult) {
	v.mu.Lock()
	current := v.bucket
	v.mu.Unlock()
	if current != bucket {
		return
	}
	v.mutate(func() {
		for _, key := range result.Deleted {
			delete(v.selObjects, key)
		}
	})
	v.openKeepingFilter(ctx, bucket, v.currentPrefix())
}

func (v *ViewModel) currentPrefix() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.prefix
}

func uniqueKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func DeleteConfirmation(keys []string, folders int) (title, body string) {
	keys = uniqueKeys(keys)
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	noun := "objects"
	if len(sorted) == 1 {
		noun = "object"
	}
	title = "Delete " + strconv.Itoa(len(sorted)) + " " + noun + "?"
	body = "This permanently deletes " + strconv.Itoa(len(sorted)) + " " + noun + " from Telegram. There is no trash and no undo."
	for i, key := range sorted {
		if i == confirmKeysListed {
			body += fmt.Sprintf("\n…and %d more", len(sorted)-confirmKeysListed)
			break
		}
		body += "\n" + key
	}
	if folders > 0 {
		body += "\n\nFolders disappear when their objects are deleted. Selecting a folder deletes nothing by itself."
	}
	return title, body
}
