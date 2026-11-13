package wire

import "strings"

const (
	widestJobID        = "00000000-0000-4000-8000-000000000000"
	widestKeyID        = "0000000000000000"
	widestETag         = "00000000000000000000000000000000-10000"
	widestPlaintextLen = int64(1991808062)
)

func parsePut(f headerFields) (Op, error) {
	op := Op{Type: OpPut}
	if err := parsePutIdentity(f, &op); err != nil {
		return Op{}, err
	}
	if err := parsePutContent(f, &op); err != nil {
		return Op{}, err
	}
	if err := parsePutAttributes(f, &op); err != nil {
		return Op{}, err
	}
	if err := validatePut(op); err != nil {
		return Op{}, err
	}
	return op, nil
}

func parsePutIdentity(f headerFields, op *Op) error {
	if err := setJobID(&op.JobID, f.pairs["j"]); err != nil {
		return err
	}
	if err := setKey(&op.Key, f.pairs["k"]); err != nil {
		return err
	}
	if err := setMsgID(&op.BodyMsgID, f.pairs["bm"], 0); err != nil {
		return err
	}
	if err := f.applyStoredSize(op); err != nil {
		return err
	}
	if err := f.applyEncryption(op); err != nil {
		return err
	}
	return f.applyPutSizes(op)
}

func (f headerFields) applyPutSizes(op *Op) error {
	rawKeyID, hasKeyID := f.pairs["kid"]
	if !op.Encrypted {
		if f.hasPlaintextSize || hasKeyID {
			return ErrWireMalformed
		}
		op.PlaintextSize = op.StoredSize
		return nil
	}
	if !f.hasPlaintextSize {
		return ErrWireMalformed
	}
	op.PlaintextSize = f.plaintextSize
	return setPattern(&op.KeyID, rawKeyID, isKeyID)
}

func parsePutContent(f headerFields, op *Op) error {
	if err := setPattern(&op.ETag, f.pairs["et"], isETag); err != nil {
		return err
	}
	if err := setPattern(&op.SHA256, f.pairs["h"], isSHA256); err != nil {
		return err
	}
	op.ContentType = DefaultContentType
	if raw, ok := f.pairs["ct"]; ok {
		return setContentType(&op.ContentType, raw)
	}
	return nil
}

func parsePutAttributes(f headerFields, op *Op) error {
	var err error
	if op.UserMeta, err = collectMetaEntries(f.pairs, userMetaPrefix, userMetaAccepted); err != nil {
		return err
	}
	if op.SystemMeta, err = collectMetaEntries(f.pairs, systemMetaPrefix, systemMetaAccepted); err != nil {
		return err
	}
	if err := setPattern(&op.Writer, f.pairs["w"], isWriter); err != nil {
		return err
	}
	return f.applyTimestamp(op)
}

func validatePut(op Op) error {
	return malformedUnless(isJobID(op.JobID) &&
		isKey(op.Key) &&
		op.BodyMsgID >= 0 && op.BodyMsgID <= MaxMsgID &&
		op.StoredSize >= 0 && op.StoredSize <= MaxStoredBytes &&
		validPutEncryption(op) &&
		validPutBodyReference(op) &&
		isETag(op.ETag) && isSHA256(op.SHA256) &&
		isContentType(op.ContentType) &&
		userMetaWithinLimit(op.UserMeta) && systemMetaWithinLimit(op.SystemMeta) &&
		isWriter(op.Writer) &&
		validTimestamp(op.Timestamp))
}

func validPutEncryption(op Op) bool {
	if !op.Encrypted {
		unencryptedSize := op.PlaintextSize == 0 || op.PlaintextSize == op.StoredSize
		return op.KeyID == "" && unencryptedSize
	}
	return validEncryption(op) && isKeyID(op.KeyID) && op.PlaintextSize >= 0
}

func validPutBodyReference(op Op) bool {
	if op.BodyMsgID > 0 {
		return op.StoredSize > 0
	}
	return op.StoredSize == 0 && !op.Encrypted && op.ETag == EmptyMD5 && op.SHA256 == EmptySHA256
}

func formatPut(op Op) (string, error) {
	if err := validatePut(op); err != nil {
		return "", err
	}
	return buildPut(op), nil
}

func buildPut(op Op) string {
	h := newHeader(OpPut)
	h.raw("j", op.JobID)
	h.escaped("k", op.Key)
	h.number("bm", op.BodyMsgID)
	h.number("sz", op.StoredSize)
	if op.Encrypted {
		h.number("psz", op.PlaintextSize)
	}
	h.encryption(op)
	if op.Encrypted {
		h.raw("kid", op.KeyID)
	}
	h.raw("et", op.ETag)
	h.raw("h", op.SHA256)
	if op.ContentType != DefaultContentType {
		h.escaped("ct", op.ContentType)
	}
	h.entries(userMetaPrefix, op.UserMeta)
	h.entries(systemMetaPrefix, op.SystemMeta)
	h.raw("w", op.Writer)
	h.number("ts", op.Timestamp)
	return h.String()
}

func provisionalPut(key, contentType string, userMeta, systemMeta map[string]string, writer string) Op {
	if contentType == "" {
		contentType = DefaultContentType
	}
	return Op{
		Type:              OpPut,
		JobID:             widestJobID,
		Key:               key,
		BodyMsgID:         MaxMsgID,
		StoredSize:        MaxStoredBytes,
		PlaintextSize:     widestPlaintextLen,
		Encrypted:         true,
		EncryptionVersion: 1,
		KeyID:             widestKeyID,
		ETag:              widestETag,
		SHA256:            strings.Repeat("0", 64),
		ContentType:       contentType,
		UserMeta:          userMeta,
		SystemMeta:        systemMeta,
		Writer:            writer,
		Timestamp:         MaxUnixTimestamp,
	}
}

func PutRecordLen(key, contentType string, userMeta, systemMeta map[string]string, writer string) (int, error) {
	op := provisionalPut(key, contentType, userMeta, systemMeta, writer)
	if err := validatePut(op); err != nil {
		return 0, err
	}
	return len(buildPut(op)), nil
}

func FitsPut(key, contentType string, userMeta, systemMeta map[string]string, writer string) error {
	length, err := PutRecordLen(key, contentType, userMeta, systemMeta, writer)
	if err != nil {
		return err
	}
	if length > MaxTextChars {
		return ErrWireTooLong
	}
	return nil
}
