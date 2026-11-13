package wire

import "math"

func parseRegistry(f headerFields) (Op, error) {
	op := Op{Type: OpRegistry}
	if err := setPattern(&op.DeploymentID, f.pairs["dep"], isDeploymentID); err != nil {
		return Op{}, err
	}
	if err := setBoundedInt(&op.FormatVersion, f.pairs["fv"], 1, math.MaxInt); err != nil {
		return Op{}, err
	}
	if err := setBoundedInt(&op.MinReaderVersion, f.pairs["mr"], 1, math.MaxInt); err != nil {
		return Op{}, err
	}
	if err := setPattern(&op.KEKFingerprint, f.pairs["kfp"], isKEKFingerprint); err != nil {
		return Op{}, err
	}
	if err := setPositiveInt64(&op.PrimaryUserID, f.pairs["uid"]); err != nil {
		return Op{}, err
	}
	if err := f.applyOptionalWriter(&op); err != nil {
		return Op{}, err
	}
	if err := f.applyTimestamp(&op); err != nil {
		return Op{}, err
	}
	if err := validateRegistry(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

func validateRegistry(op Op) error {
	return malformedUnless(isDeploymentID(op.DeploymentID) &&
		op.FormatVersion >= 1 &&
		op.MinReaderVersion >= 1 && op.MinReaderVersion <= op.FormatVersion &&
		isKEKFingerprint(op.KEKFingerprint) &&
		op.PrimaryUserID > 0 &&
		validOptionalWriter(op.Writer) && validTimestamp(op.Timestamp))
}

func formatRegistry(op Op) (string, error) {
	if err := validateRegistry(op); err != nil {
		return "", err
	}
	h := newHeader(OpRegistry)
	h.raw("dep", op.DeploymentID)
	h.number("fv", int64(op.FormatVersion))
	h.number("mr", int64(op.MinReaderVersion))
	h.raw("kfp", op.KEKFingerprint)
	h.number("uid", op.PrimaryUserID)
	h.optionalWriter(op.Writer)
	h.number("ts", op.Timestamp)
	return h.String(), nil
}
