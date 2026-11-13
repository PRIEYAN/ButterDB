package wire

import (
	"math"
	"strconv"
)

const maxInt64 = math.MaxInt64

type headerFields struct {
	pairs                map[string]string
	storedSize           int64
	plaintextSize        int64
	timestamp            int64
	encryptionVersion    int
	encrypted            bool
	hasStoredSize        bool
	hasPlaintextSize     bool
	hasTimestamp         bool
	hasEncryptionVersion bool
}

func Parse(raw string) (Op, error) {
	pairs, err := splitHeader(raw)
	if err != nil {
		return Op{}, err
	}
	rawType, ok := pairs["t"]
	if !ok || rawType == "" {
		return Op{}, ErrWireMalformed
	}
	opType := OpType(rawType)
	parse := parserFor(opType)
	if parse == nil {
		return Op{Type: opType}, ErrWireUnknownOp
	}
	fields, err := parseSharedFields(pairs)
	if err != nil {
		return Op{}, err
	}
	return parse(fields)
}

func parseSharedFields(pairs map[string]string) (headerFields, error) {
	fields := headerFields{pairs: pairs}
	if raw, ok := pairs["sz"]; ok {
		if err := setInt64Range(&fields.storedSize, raw, 0, maxInt64); err != nil {
			return headerFields{}, err
		}
		fields.hasStoredSize = true
	}
	if raw, ok := pairs["ts"]; ok {
		if err := setInt64Range(&fields.timestamp, raw, 0, MaxUnixTimestamp); err != nil {
			return headerFields{}, err
		}
		fields.hasTimestamp = true
	}
	fields.encrypted = pairs["enc"] == "1"
	if raw, ok := pairs["ev"]; ok {
		version, err := strconv.Atoi(raw)
		if err != nil {
			return headerFields{}, ErrWireMalformed
		}
		fields.encryptionVersion = version
		fields.hasEncryptionVersion = true
	}
	if raw, ok := pairs["psz"]; ok {
		if err := setInt64Range(&fields.plaintextSize, raw, 0, maxInt64); err != nil {
			return headerFields{}, err
		}
		fields.hasPlaintextSize = true
	}
	return fields, nil
}

func (f headerFields) applyStoredSize(op *Op) error {
	if err := malformedUnless(f.hasStoredSize); err != nil {
		return err
	}
	op.StoredSize = f.storedSize
	return nil
}

func (f headerFields) applyEncryption(op *Op) error {
	if f.hasEncryptionVersion && !f.encrypted {
		return ErrWireMalformed
	}
	if !f.encrypted {
		return nil
	}
	op.Encrypted = true
	op.EncryptionVersion = 1
	if f.hasEncryptionVersion {
		op.EncryptionVersion = f.encryptionVersion
	}
	return nil
}

func (f headerFields) applyTimestamp(op *Op) error {
	if err := malformedUnless(f.hasTimestamp); err != nil {
		return err
	}
	op.Timestamp = f.timestamp
	return nil
}

func (f headerFields) applyOptionalWriter(op *Op) error {
	raw, ok := f.pairs["w"]
	if !ok {
		return nil
	}
	return setPattern(&op.Writer, raw, isWriter)
}
