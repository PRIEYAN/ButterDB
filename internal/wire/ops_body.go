package wire

func parseBody(f headerFields) (Op, error) {
	op := Op{Type: OpBody}
	if err := setJobID(&op.JobID, f.pairs["j"]); err != nil {
		return Op{}, err
	}
	if err := f.applyStoredSize(&op); err != nil {
		return Op{}, err
	}
	if err := f.applyEncryption(&op); err != nil {
		return Op{}, err
	}
	if err := validateBody(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

func validateBody(op Op) error {
	return malformedUnless(isJobID(op.JobID) &&
		op.StoredSize >= 1 && op.StoredSize <= MaxStoredBytes &&
		validEncryption(op))
}

func formatBody(op Op) (string, error) {
	if err := validateBody(op); err != nil {
		return "", err
	}
	h := newHeader(OpBody)
	h.raw("j", op.JobID)
	h.number("sz", op.StoredSize)
	h.encryption(op)
	return h.String(), nil
}
