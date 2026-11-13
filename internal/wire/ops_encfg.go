package wire

import "math"

func parseEncConfig(f headerFields) (Op, error) {
	op := Op{Type: OpEncConfig, ConfigVersion: 1}
	if err := setJobID(&op.JobID, f.pairs["j"]); err != nil {
		return Op{}, err
	}
	if err := setPositiveInt64(&op.BucketChannelID, f.pairs["bc"]); err != nil {
		return Op{}, err
	}
	if err := setPattern(&op.KeyID, f.pairs["kid"], isKeyID); err != nil {
		return Op{}, err
	}
	if err := parseEncConfigKeys(f, &op); err != nil {
		return Op{}, err
	}
	if err := f.applyOptionalWriter(&op); err != nil {
		return Op{}, err
	}
	if err := f.applyTimestamp(&op); err != nil {
		return Op{}, err
	}
	if err := validateEncConfig(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

func parseEncConfigKeys(f headerFields, op *Op) error {
	if err := setB64Len(&op.WrappedKey, f.pairs["wrap"], wrappedKeyBytes); err != nil {
		return err
	}
	if err := setB64Len(&op.KeyCheck, f.pairs["check"], keyCheckBytes); err != nil {
		return err
	}
	if err := setPattern(&op.KEKFingerprint, f.pairs["kfp"], isKEKFingerprint); err != nil {
		return err
	}
	if raw, ok := f.pairs["cv"]; ok {
		return setBoundedInt(&op.ConfigVersion, raw, 1, math.MaxInt)
	}
	return nil
}

func validateEncConfig(op Op) error {
	return malformedUnless(isJobID(op.JobID) && op.BucketChannelID > 0 &&
		isKeyID(op.KeyID) &&
		len(op.WrappedKey) == wrappedKeyBytes && len(op.KeyCheck) == keyCheckBytes &&
		isKEKFingerprint(op.KEKFingerprint) &&
		op.ConfigVersion >= 0 &&
		validOptionalWriter(op.Writer) && validTimestamp(op.Timestamp))
}

func formatEncConfig(op Op) (string, error) {
	if err := validateEncConfig(op); err != nil {
		return "", err
	}
	h := newHeader(OpEncConfig)
	h.raw("j", op.JobID)
	h.number("bc", op.BucketChannelID)
	h.raw("kid", op.KeyID)
	h.binary("wrap", op.WrappedKey)
	h.binary("check", op.KeyCheck)
	h.raw("kfp", op.KEKFingerprint)
	if op.ConfigVersion > 1 {
		h.number("cv", int64(op.ConfigVersion))
	}
	h.optionalWriter(op.Writer)
	h.number("ts", op.Timestamp)
	return h.String(), nil
}
