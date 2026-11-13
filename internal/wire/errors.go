package wire

func malformedUnless(ok bool) error {
	if ok {
		return nil
	}
	return ErrWireMalformed
}
