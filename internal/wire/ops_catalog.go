package wire

func parserFor(t OpType) func(headerFields) (Op, error) {
	switch t {
	case OpBody:
		return parseBody
	case OpPut:
		return parsePut
	case OpDel:
		return parseDel
	case OpRegistry:
		return parseRegistry
	case OpBucketCreate, OpBucketConfig:
		return parseBucketConfig
	case OpBucketDelete:
		return parseBucketDelete
	case OpEncConfig:
		return parseEncConfig
	}
	return nil
}

func formatterFor(t OpType) func(Op) (string, error) {
	switch t {
	case OpBody:
		return formatBody
	case OpPut:
		return formatPut
	case OpDel:
		return formatDel
	case OpRegistry:
		return formatRegistry
	case OpBucketCreate, OpBucketConfig:
		return formatBucketConfig
	case OpBucketDelete:
		return formatBucketDelete
	case OpEncConfig:
		return formatEncConfig
	}
	return nil
}

func Known(t OpType) bool {
	return parserFor(t) != nil
}

func OnDocument(t OpType) bool {
	return t == OpBody
}

func KeyOf(op Op) (string, bool) {
	if op.Type == OpPut || op.Type == OpDel {
		return op.Key, true
	}
	return "", false
}

func validTimestamp(ts int64) bool {
	return ts >= 0 && ts <= MaxUnixTimestamp
}

func validEncryption(op Op) bool {
	return !op.Encrypted || op.EncryptionVersion >= 1
}

func validOptionalWriter(writer string) bool {
	return writer == "" || isWriter(writer)
}
