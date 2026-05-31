// Package log provides a shared debug logger for the deepact engine.
// By default it discards output. Set DEEPACT_LOG to a file path to redirect.
package log

import (
	"io"
	"log"
	"os"
	"sync"
)

var (
	mu     sync.Mutex
	logger *log.Logger
	file   *os.File
)

func init() {
	path := os.Getenv("DEEPACT_LOG")
	if path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			file = f
			logger = log.New(f, "", log.Ltime|log.Lmicroseconds)
			return
		}
	}
	logger = log.New(io.Discard, "", log.Ltime|log.Lmicroseconds)
}

// New creates a new log.Logger with the given prefix, writing to the shared
// output. Use this instead of creating independent loggers in init() functions.
func New(prefix string) *log.Logger {
	mu.Lock()
	defer mu.Unlock()
	return log.New(logger.Writer(), prefix, log.Ltime|log.Lmicroseconds)
}

// Writer returns the underlying io.Writer for use with log.New.
func Writer() io.Writer {
	mu.Lock()
	defer mu.Unlock()
	return logger.Writer()
}

// Close flushes and closes the log file if one was opened.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
	}
}
