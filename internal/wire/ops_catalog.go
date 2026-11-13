package wire

func parserFor(OpType) func(headerFields) (Op, error) {
	return nil
}

func formatterFor(OpType) func(Op) (string, error) {
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
