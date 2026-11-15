package objectops

import "github.com/PRIEYAN/ButterDB/internal/s3api/s3err"

func errInvalidPartNumber() error {
	return s3err.New(s3err.CodeInvalidPartNumber, "")
}
