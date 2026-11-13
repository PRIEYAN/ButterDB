package wire

func parseBucketTarget(f headerFields, op *Op) error {
	if err := setJobID(&op.JobID, f.pairs["j"]); err != nil {
		return err
	}
	if err := setPattern(&op.Bucket, f.pairs["b"], isBucketName); err != nil {
		return err
	}
	return setPositiveInt64(&op.BucketChannelID, f.pairs["bc"])
}

func parseBucketConfig(f headerFields) (Op, error) {
	op := Op{Type: OpType(f.pairs["t"])}
	if err := parseBucketTarget(f, &op); err != nil {
		return Op{}, err
	}
	if err := setPattern(&op.Policy, f.pairs["pol"], isPolicy); err != nil {
		return Op{}, err
	}
	if err := setPattern(&op.DefaultEncryption, f.pairs["de"], isDefaultEncryption); err != nil {
		return Op{}, err
	}
	if err := setInt64Range(&op.CacheMaxAge, f.pairs["cma"], 0, MaxCacheMaxAge); err != nil {
		return Op{}, err
	}
	if err := f.applyOptionalWriter(&op); err != nil {
		return Op{}, err
	}
	if err := f.applyTimestamp(&op); err != nil {
		return Op{}, err
	}
	if err := validateBucketConfig(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

func validateBucketTarget(op Op) bool {
	return isJobID(op.JobID) && isBucketName(op.Bucket) && op.BucketChannelID > 0 &&
		validOptionalWriter(op.Writer) && validTimestamp(op.Timestamp)
}

func validateBucketConfig(op Op) error {
	return malformedUnless(validateBucketTarget(op) &&
		isPolicy(op.Policy) && isDefaultEncryption(op.DefaultEncryption) &&
		op.CacheMaxAge >= 0 && op.CacheMaxAge <= MaxCacheMaxAge)
}

func formatBucketConfig(op Op) (string, error) {
	if err := validateBucketConfig(op); err != nil {
		return "", err
	}
	h := newHeader(op.Type)
	h.raw("j", op.JobID)
	h.raw("b", op.Bucket)
	h.number("bc", op.BucketChannelID)
	h.raw("pol", op.Policy)
	h.raw("de", op.DefaultEncryption)
	h.number("cma", op.CacheMaxAge)
	h.optionalWriter(op.Writer)
	h.number("ts", op.Timestamp)
	return h.String(), nil
}

func parseBucketDelete(f headerFields) (Op, error) {
	op := Op{Type: OpBucketDelete}
	if err := parseBucketTarget(f, &op); err != nil {
		return Op{}, err
	}
	if err := f.applyOptionalWriter(&op); err != nil {
		return Op{}, err
	}
	if err := f.applyTimestamp(&op); err != nil {
		return Op{}, err
	}
	if err := validateBucketDelete(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

func validateBucketDelete(op Op) error {
	return malformedUnless(validateBucketTarget(op))
}

func formatBucketDelete(op Op) (string, error) {
	if err := validateBucketDelete(op); err != nil {
		return "", err
	}
	h := newHeader(OpBucketDelete)
	h.raw("j", op.JobID)
	h.raw("b", op.Bucket)
	h.number("bc", op.BucketChannelID)
	h.optionalWriter(op.Writer)
	h.number("ts", op.Timestamp)
	return h.String(), nil
}
