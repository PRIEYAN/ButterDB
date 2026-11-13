package wire

func parseDel(f headerFields) (Op, error) {
	op := Op{Type: OpDel}
	if err := setJobID(&op.JobID, f.pairs["j"]); err != nil {
		return Op{}, err
	}
	if err := setKey(&op.Key, f.pairs["k"]); err != nil {
		return Op{}, err
	}
	if raw, ok := f.pairs["pv"]; ok {
		if err := setMsgID(&op.PutVersion, raw, 1); err != nil {
			return Op{}, err
		}
	}
	if err := setPattern(&op.Writer, f.pairs["w"], isWriter); err != nil {
		return Op{}, err
	}
	if err := f.applyTimestamp(&op); err != nil {
		return Op{}, err
	}
	if err := validateDel(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

func validateDel(op Op) error {
	return malformedUnless(isJobID(op.JobID) && isKey(op.Key) &&
		op.PutVersion >= 0 && op.PutVersion <= MaxMsgID &&
		isWriter(op.Writer) && validTimestamp(op.Timestamp))
}

func formatDel(op Op) (string, error) {
	if err := validateDel(op); err != nil {
		return "", err
	}
	h := newHeader(OpDel)
	h.raw("j", op.JobID)
	h.escaped("k", op.Key)
	if op.PutVersion > 0 {
		h.number("pv", op.PutVersion)
	}
	h.raw("w", op.Writer)
	h.number("ts", op.Timestamp)
	return h.String(), nil
}
