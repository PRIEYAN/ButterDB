package vm

import (
	"slices"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type SortColumn int

const (
	ColumnName SortColumn = iota
	ColumnAccess
	ColumnEncryption
	ColumnCacheMaxAge
	ColumnObjects
	ColumnSize
	ColumnCreated
)

func compareBy(column SortColumn, a, b adminv1.Bucket) int {
	switch column {
	case ColumnAccess:
		return strings.Compare(a.Policy, b.Policy)
	case ColumnEncryption:
		return strings.Compare(a.EncryptionDefault, b.EncryptionDefault)
	case ColumnCacheMaxAge:
		return a.CacheMaxAgeSeconds - b.CacheMaxAgeSeconds
	case ColumnObjects:
		return compareInt64(a.ObjectCount, b.ObjectCount)
	case ColumnSize:
		return compareInt64(a.StoredBytes, b.StoredBytes)
	case ColumnCreated:
		return a.CreatedAt.Compare(b.CreatedAt)
	}
	return strings.Compare(a.Name, b.Name)
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func sortedBuckets(in []adminv1.Bucket, column SortColumn, descending bool) []adminv1.Bucket {
	out := slices.Clone(in)
	slices.SortStableFunc(out, func(a, b adminv1.Bucket) int {
		c := compareBy(column, a, b)
		if c == 0 {
			return strings.Compare(a.Name, b.Name)
		}
		if descending {
			return -c
		}
		return c
	})
	return out
}
