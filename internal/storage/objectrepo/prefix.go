package objectrepo

func PrefixEnd(prefix string) (string, bool) {
	end := []byte(prefix)
	for len(end) > 0 && end[len(end)-1] == 0xFF {
		end = end[:len(end)-1]
	}
	if len(end) == 0 {
		return "", false
	}
	end[len(end)-1]++
	return string(end), true
}

func successor(key string) string {
	return key + "\x00"
}
