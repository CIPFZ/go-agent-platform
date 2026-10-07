package audit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	Time       time.Time         `json:"time"`
	Kind       string            `json:"kind"`
	Actor      string            `json:"actor,omitempty"`
	ResourceID string            `json:"resource_id,omitempty"`
	Outcome    string            `json:"outcome,omitempty"`
	Details    map[string]string `json:"details,omitempty"`
}

type Sink interface{ Record(Event) }

type MemorySink struct {
	mu     sync.RWMutex
	events []Event
	limit  int
}

// FileSink appends one JSON event per line and is safe for concurrent writers.
// It keeps the last write error so health checks and operators can detect a
// degraded audit trail instead of silently losing security events.
type FileSink struct {
	mu      sync.Mutex
	path    string
	lastErr error
}

func NewFileSink(path string) (*FileSink, error) {
	if path == "" {
		return nil, errors.New("audit path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &FileSink{path: path}, nil
}

func (s *FileSink) Record(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	event.Details = clone(event.Details)
	data, err := json.Marshal(event)
	if err == nil {
		var file *os.File
		file, err = os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = file.Write(append(data, '\n'))
			if syncErr := file.Sync(); err == nil {
				err = syncErr
			}
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
	}
	s.lastErr = err
}

func (s *FileSink) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func NewMemorySink(limit int) *MemorySink {
	if limit <= 0 {
		limit = 10000
	}
	return &MemorySink{limit: limit}
}

func (s *MemorySink) Record(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	event.Details = clone(event.Details)
	s.events = append(s.events, event)
	if len(s.events) > s.limit {
		s.events = append([]Event(nil), s.events[len(s.events)-s.limit:]...)
	}
}

func (s *MemorySink) List() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	output := make([]Event, len(s.events))
	copy(output, s.events)
	for i := range output {
		output[i].Details = clone(output[i].Details)
	}
	return output
}

func clone(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
