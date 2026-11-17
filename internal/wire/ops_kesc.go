package wire

const (
	EscrowKDFArgon2id  = "a2id"
	EscrowSaltBytes    = 16
	EscrowWrapBytes    = wrappedKeyBytes
	EscrowMinTime      = 1
	EscrowMaxTime      = 10
	EscrowMinMemoryKiB = 19456
	EscrowMaxMemoryKiB = 1048576
	EscrowMinParallel  = 1
	EscrowMaxParallel  = 16
)

func parseKeyEscrow(f headerFields) (Op, error) {
	op := Op{Type: OpKeyEscrow}
	if err := setJobID(&op.JobID, f.pairs["j"]); err != nil {
		return Op{}, err
	}
	if err := setPattern(&op.KEKFingerprint, f.pairs["kfp"], isKEKFingerprint); err != nil {
		return Op{}, err
	}
	if f.pairs["kdf"] != EscrowKDFArgon2id {
		return Op{}, ErrWireMalformed
	}
	op.EscrowKDF = EscrowKDFArgon2id
	if err := setB64Len(&op.EscrowSalt, f.pairs["salt"], EscrowSaltBytes); err != nil {
		return Op{}, err
	}
	if err := setBoundedInt(&op.EscrowTime, f.pairs["kt"], EscrowMinTime, EscrowMaxTime); err != nil {
		return Op{}, err
	}
	if err := setBoundedInt(&op.EscrowMemoryKiB, f.pairs["km"], EscrowMinMemoryKiB, EscrowMaxMemoryKiB); err != nil {
		return Op{}, err
	}
	if err := setBoundedInt(&op.EscrowParallel, f.pairs["kp"], EscrowMinParallel, EscrowMaxParallel); err != nil {
		return Op{}, err
	}
	if err := setB64Len(&op.WrappedKey, f.pairs["wrap"], EscrowWrapBytes); err != nil {
		return Op{}, err
	}
	if err := f.applyOptionalWriter(&op); err != nil {
		return Op{}, err
	}
	if err := f.applyTimestamp(&op); err != nil {
		return Op{}, err
	}
	if err := validateKeyEscrow(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

func validateKeyEscrow(op Op) error {
	return malformedUnless(isJobID(op.JobID) && isKEKFingerprint(op.KEKFingerprint) &&
		op.EscrowKDF == EscrowKDFArgon2id && len(op.EscrowSalt) == EscrowSaltBytes &&
		op.EscrowTime >= EscrowMinTime && op.EscrowTime <= EscrowMaxTime &&
		op.EscrowMemoryKiB >= EscrowMinMemoryKiB && op.EscrowMemoryKiB <= EscrowMaxMemoryKiB &&
		op.EscrowParallel >= EscrowMinParallel && op.EscrowParallel <= EscrowMaxParallel &&
		len(op.WrappedKey) == EscrowWrapBytes &&
		validOptionalWriter(op.Writer) && validTimestamp(op.Timestamp))
}

func formatKeyEscrow(op Op) (string, error) {
	if err := validateKeyEscrow(op); err != nil {
		return "", err
	}
	h := newHeader(OpKeyEscrow)
	h.raw("j", op.JobID)
	h.raw("kfp", op.KEKFingerprint)
	h.raw("kdf", op.EscrowKDF)
	h.binary("salt", op.EscrowSalt)
	h.number("kt", int64(op.EscrowTime))
	h.number("km", int64(op.EscrowMemoryKiB))
	h.number("kp", int64(op.EscrowParallel))
	h.binary("wrap", op.WrappedKey)
	h.optionalWriter(op.Writer)
	h.number("ts", op.Timestamp)
	return h.String(), nil
}
