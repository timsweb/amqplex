package proxy

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"
)

// logCapture is a slog.Handler that records all log records for test assertions.
type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (lc *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (lc *logCapture) Handle(_ context.Context, r slog.Record) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.records = append(lc.records, r.Clone())
	return nil
}

func (lc *logCapture) WithAttrs([]slog.Attr) slog.Handler { return lc }
func (lc *logCapture) WithGroup(string) slog.Handler      { return lc }

// newCapture returns a capture handler and a logger backed by it.
func newCapture() (*logCapture, *slog.Logger) {
	lc := &logCapture{}
	return lc, slog.New(lc)
}

// discardLogger returns a logger that silences all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// messages returns all log message strings recorded so far.
func (lc *logCapture) messages() []string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	msgs := make([]string, len(lc.records))
	for i, r := range lc.records {
		msgs[i] = r.Message
	}
	return msgs
}

// attrValue returns the value of a named attribute from any recorded log entry.
func (lc *logCapture) attrValue(key string) (slog.Value, bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, r := range lc.records {
		var found slog.Value
		var ok bool
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				found = a.Value
				ok = true
				return false
			}
			return true
		})
		if ok {
			return found, true
		}
	}
	return slog.Value{}, false
}

// waitForMessage polls until a log message matching msg appears or timeout elapses.
func (lc *logCapture) waitForMessage(msg string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		lc.mu.Lock()
		for _, r := range lc.records {
			if r.Message == msg {
				lc.mu.Unlock()
				return true
			}
		}
		lc.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
