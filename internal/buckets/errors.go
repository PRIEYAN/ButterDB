package buckets

import "errors"

var (
	ErrBucketLimitReached = errors.New("buckets: bucket limit reached")
	ErrOpRejected         = errors.New("buckets: projection rejected the op")
	ErrOpTampered         = errors.New("buckets: projection reports a changed header for the op")
	ErrOpNotProjected     = errors.New("buckets: op was not projected")
)
