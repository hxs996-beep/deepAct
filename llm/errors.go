package llm

import "errors"

var (
	ErrRateLimit       = errors.New("rate limit")
	ErrTimeout         = errors.New("timeout")
	ErrContextCanceled = errors.New("context canceled")
	ErrInvalidResponse = errors.New("invalid response")
	// ErrStreamIdle signals a streaming response went silent for longer than
	// the client's idle timeout (no SSE data line arrived). Unlike ErrTimeout
	// (a hard deadline / context cancel, which is NOT retried), an idle stall
	// is treated as a transient network/server hiccup and IS retried — the
	// connection is almost certainly half-open or the server stalled, so a
	// fresh request is likely to succeed.
	ErrStreamIdle = errors.New("stream idle timeout")
)
