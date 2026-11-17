package nativeplayer

func protectedMPVOptions(protected bool) [][2]string {
	if !protected {
		return nil
	}
	return [][2]string{
		{"input-default-bindings", "no"},
		{"cache-on-disk", "no"},
		{"stream-record", ""},
		{"save-position-on-quit", "no"},
		{"load-scripts", "no"},
	}
}

func protectedMPVArgs(protected bool) []string {
	options := protectedMPVOptions(protected)
	args := make([]string, 0, len(options))
	for _, option := range options {
		args = append(args, "--"+option[0]+"="+option[1])
	}
	return args
}
