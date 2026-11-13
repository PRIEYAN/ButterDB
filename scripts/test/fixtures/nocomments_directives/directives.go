//go:build linux

package directives

import _ "embed"

//go:embed value.txt
var raw string

// Telegram rejects range offsets that are not 4 KiB aligned.
func Value() string {
	return raw
}
