package s3err

import "time"

type Code string

type Field struct {
	Name  string
	Value string
}

type APIError struct {
	Code       Code
	Status     int
	Message    string
	BucketName string
	Key        string
	Resource   string
	RetryAfter time.Duration
	Extra      []Field
}
