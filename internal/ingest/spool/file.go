package spool

type File struct {
	Path   string
	Size   int64
	MD5    [16]byte
	SHA256 [32]byte
}
