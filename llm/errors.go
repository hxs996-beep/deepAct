package llm

import "errors"

var (
	ErrRateLimit       = errors.New("rate limit")
	ErrTimeout         = errors.New("timeout")
	ErrContextCanceled = errors.New("context canceled")
	ErrInvalidResponse = errors.New("invalid response")
)
