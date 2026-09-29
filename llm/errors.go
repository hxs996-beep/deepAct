package llm

import "errors"

var (
	ErrRateLimit       = errors.New("rate limit")
	ErrTimeout         = errors.New("timeout")
	ErrContextCanceled = errors.New("context canceled")
	ErrInvalidResponse = errors.New("invalid response")
	// ErrInsufficientBalance signals the provider rejected the request because
	// the account has no remaining credits (HTTP 402). Retrying is pointless —
	// the failure is persistent until the user tops up. Treated as fatal, never
	// retried, and surfaced with a clear message instead of a generic network
	// error.
	ErrInsufficientBalance = errors.New("insufficient balance")
	// ErrStreamIdle signals a streaming response went silent for longer than
	// the client's idle timeout (no SSE data line arrived). Unlike ErrTimeout
	// (a hard deadline / context cancel, which is NOT retried), an idle stall
	// is treated as a transient network/server hiccup and IS retried — the
	// connection is almost certainly half-open or the server stalled, so a
	// fresh request is likely to succeed.
	ErrStreamIdle = errors.New("stream idle timeout")
	// ErrContextLength signals the provider rejected the request because it
	// exceeded the model's context window (HTTP 400). It is surfaced as a
	// distinct sentinel so the engine can react by shrinking the history and
	// retrying once, instead of failing the run. Not retried by the llm layer:
	// the same request would fail identically.
	ErrContextLength = errors.New("context length exceeded")
)
