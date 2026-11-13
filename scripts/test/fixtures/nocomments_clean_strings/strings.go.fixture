package strings

import "fmt"

const raw = `
// not a comment
/* nor this */
`

func Value() string {
	url := "https://example.test/a//b"
	star := "/* literal */"
	quote := '"'
	return fmt.Sprint(url, star, quote, raw) // trailing remark
}
