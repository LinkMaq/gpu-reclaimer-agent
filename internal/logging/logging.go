package logging

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type Logger struct {
	w  io.Writer
	mu sync.Mutex
}

func NewJSONLogger(w io.Writer) *Logger {
	return &Logger{w: w}
}

func (l *Logger) Info(fields map[string]any)  { l.write("info", fields) }
func (l *Logger) Warn(fields map[string]any)  { l.write("warn", fields) }
func (l *Logger) Error(fields map[string]any) { l.write("error", fields) }

func (l *Logger) write(level string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["level"] = level
	fields["ts"] = time.Now().UTC().Format(time.RFC3339Nano)

	b, err := json.Marshal(fields)
	if err != nil {
		// Last resort: drop structured fields.
		b = []byte(`{"level":"error","ts":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","msg":"failed to marshal log"}`)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}
